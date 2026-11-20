package funcs

import (
	"context"
	"time"

	"sigs.k8s.io/e2e-framework/pkg/env"
	"sigs.k8s.io/e2e-framework/pkg/envconf"
)

const defaultKuadrantVersion = "1.3.1"

// kuadrantCR bootstraps Authorino + Limitador once the operator is installed.
const kuadrantCR = `apiVersion: kuadrant.io/v1beta1
kind: Kuadrant
metadata:
  name: kuadrant
  namespace: kuadrant-system
spec: {}
`

// KuadrantOption configures InstallKuadrant.
type KuadrantOption func(*kuadrantConfig)

type kuadrantConfig struct{ version string }

// KuadrantVersion pins the kuadrant-operator chart version. Empty is ignored.
func KuadrantVersion(v string) KuadrantOption {
	return func(c *kuadrantConfig) {
		if v != "" {
			c.version = v
		}
	}
}

// InstallKuadrant installs the kuadrant-operator (Authorino + Limitador) and
// bootstraps it. Kuadrant is what attaches the WasmPlugin to the gateway that the
// ext-proc EnvoyFilters anchor to (INSERT_BEFORE/AFTER the kuadrant wasm filter);
// without it the ext-proc filters are never inserted. Installed after istio, so
// the operator is restarted to pick up istio's gateway provider.
func InstallKuadrant(opts ...KuadrantOption) env.Func {
	o := kuadrantConfig{version: defaultKuadrantVersion}
	for _, opt := range opts {
		opt(&o)
	}
	return func(ctx context.Context, cfg *envconf.Config) (context.Context, error) {
		// The operator install runs through the Helm Go SDK (no helm binary); the
		// chart resolves directly from the repo URL, so no `helm repo add` is needed.
		if err := helmUpgradeInstall(ctx, cfg, HelmChart{
			Release:         "kuadrant-operator",
			Chart:           "kuadrant-operator",
			RepoURL:         "https://kuadrant.io/helm-charts/",
			Version:         o.version,
			Namespace:       "kuadrant-system",
			CreateNamespace: true,
			Wait:            true,
			Timeout:         350 * time.Second,
		}); err != nil {
			return ctx, err
		}
		r, err := newResources(cfg)
		if err != nil {
			return ctx, err
		}
		// Restart so the operator rediscovers istio (installed before this step).
		_ = restartDeployment(ctx, r, "kuadrant-system", "kuadrant-operator-controller-manager")
		if err := waitDeploymentAvailable(ctx, r, "kuadrant-system", "kuadrant-operator-controller-manager", 120*time.Second); err != nil {
			return ctx, err
		}
		if err := applyYAML(ctx, cfg, kuadrantCR); err != nil {
			return ctx, err
		}
		// The operator creates authorino + limitador from the CR; wait for both
		// (waitDeploymentAvailable treats a not-yet-created deployment as "keep
		// waiting", so this also rides out the creation delay).
		if err := waitDeploymentAvailable(ctx, r, "kuadrant-system", "authorino", 180*time.Second); err != nil {
			return ctx, err
		}
		return ctx, waitDeploymentAvailable(ctx, r, "kuadrant-system", "limitador-limitador", 180*time.Second)
	}
}
