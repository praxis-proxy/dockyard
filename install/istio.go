package install

import (
	"context"
	"maps"
	"time"

	"sigs.k8s.io/e2e-framework/pkg/env"
	"sigs.k8s.io/e2e-framework/pkg/envconf"
)

const (
	// defaultIstioVersion pins the istio Helm chart version. Override with
	// IstioVersion — there is no istioctl binary to fall back on a cluster default.
	defaultIstioVersion = "1.24.2"
	istioChartRepo      = "https://istio-release.storage.googleapis.com/charts"
)

// IstioOption configures InstallIstio.
type IstioOption func(*istioConfig)

type istioConfig struct {
	version            string
	inferenceExtension bool
	values             map[string]any
}

// IstioVersion pins the istio chart version (base + istiod). Empty is ignored.
func IstioVersion(v string) IstioOption {
	return func(c *istioConfig) {
		if v != "" {
			c.version = v
		}
	}
}

// IstioInferenceExtension toggles the Gateway API Inference Extension (GIE).
func IstioInferenceExtension(on bool) IstioOption {
	return func(c *istioConfig) { c.inferenceExtension = on }
}

// IstioValues merges extra top-level istiod chart values (shallow override).
func IstioValues(v map[string]any) IstioOption {
	return func(c *istioConfig) { c.values = v }
}

// InstallIstio installs istio through its official Helm charts via the Helm Go SDK
// — no istioctl binary. It applies the `base` chart (CRDs + cluster roles) then the
// `istiod` chart (the control plane), which together are the equivalent of the
// istioctl "minimal" profile. meshConfig.accessLogFile surfaces request failures
// (response flags + code details) in the proxy logs during e2e debugging; the
// Gateway API Inference Extension is on by default.
func InstallIstio(opts ...IstioOption) env.Func {
	o := istioConfig{version: defaultIstioVersion, inferenceExtension: true}
	for _, opt := range opts {
		opt(&o)
	}
	return func(ctx context.Context, cfg *envconf.Config) (context.Context, error) {
		if err := helmUpgradeInstall(ctx, cfg, HelmChart{
			Release:         "istio-base",
			Chart:           "base",
			RepoURL:         istioChartRepo,
			Version:         o.version,
			Namespace:       "istio-system",
			CreateNamespace: true,
			Wait:            true,
			Timeout:         3 * time.Minute,
		}); err != nil {
			return ctx, err
		}

		values := map[string]any{
			"meshConfig": map[string]any{"accessLogFile": "/dev/stdout"},
		}
		if o.inferenceExtension {
			values["pilot"] = map[string]any{
				"env": map[string]any{"ENABLE_GATEWAY_API_INFERENCE_EXTENSION": "true"},
			}
		}
		maps.Copy(values, o.values) // caller overrides win

		if err := helmUpgradeInstall(ctx, cfg, HelmChart{
			Release:   "istiod",
			Chart:     "istiod",
			RepoURL:   istioChartRepo,
			Version:   o.version,
			Namespace: "istio-system",
			Values:    values,
			Wait:      true,
			Timeout:   3 * time.Minute,
		}); err != nil {
			return ctx, err
		}

		r, err := newResources(cfg)
		if err != nil {
			return ctx, err
		}
		return ctx, waitDeploymentAvailable(ctx, r, "istio-system", "istiod", 180*time.Second)
	}
}
