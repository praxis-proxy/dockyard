package funcs

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/e2e-framework/klient/wait"
	"sigs.k8s.io/e2e-framework/pkg/env"
	"sigs.k8s.io/e2e-framework/pkg/envconf"
)

const defaultCertManagerVersion = "v1.15.3"

// CertManagerOption configures InstallCertManager.
type CertManagerOption func(*certManagerConfig)

type certManagerConfig struct{ version string }

// CertManagerVersion pins the cert-manager release. Empty is ignored.
func CertManagerVersion(v string) CertManagerOption {
	return func(c *certManagerConfig) {
		if v != "" {
			c.version = v
		}
	}
}

// InstallCertManager installs cert-manager (the standard kind/k8s way to issue
// TLS) and waits for its CRDs + deployments to be ready, so Issuer/Certificate
// resources can be applied immediately after.
func InstallCertManager(opts ...CertManagerOption) env.Func {
	o := certManagerConfig{version: defaultCertManagerVersion}
	for _, opt := range opts {
		opt(&o)
	}
	return func(ctx context.Context, cfg *envconf.Config) (context.Context, error) {
		url := fmt.Sprintf("https://github.com/cert-manager/cert-manager/releases/download/%s/cert-manager.yaml", o.version)
		if err := applyURL(ctx, cfg, url); err != nil {
			return ctx, err
		}
		r, err := newResources(cfg)
		if err != nil {
			return ctx, err
		}
		if err := waitCRDsEstablished(ctx, r, 120*time.Second,
			"certificates.cert-manager.io", "issuers.cert-manager.io", "clusterissuers.cert-manager.io"); err != nil {
			return ctx, err
		}
		return ctx, waitAllDeploymentsAvailable(ctx, r, "cert-manager", 180*time.Second)
	}
}

// ApplyManifestFile server-side-applies a manifest file, retrying to ride out an
// admission webhook that is still coming up (e.g. cert-manager can reject the first
// Issuer/Certificate writes). Path is relative to the test package working directory.
func ApplyManifestFile(path string) env.Func {
	return func(ctx context.Context, cfg *envconf.Config) (context.Context, error) {
		return ctx, Retry(ctx, time.Minute, 10*time.Second, "apply "+path, func(ctx context.Context) error {
			return applyFile(ctx, cfg, path)
		})
	}
}

// PublishCABundle copies the ca.crt from a cert-manager CA secret into a ConfigMap
// (key service-ca.crt) in another namespace — the CA the gateway mounts to trust
// the ext-proc serving certs. Waits for the CA secret to be issued first.
func PublishCABundle(caSecretNS, caSecretName, targetNS, targetCM string) env.Func {
	return func(ctx context.Context, cfg *envconf.Config) (context.Context, error) {
		r := cfg.Client().Resources()
		var secret corev1.Secret
		err := wait.For(func(context.Context) (bool, error) {
			if e := r.Get(ctx, caSecretName, caSecretNS, &secret); e != nil {
				return false, nil
			}
			return len(secret.Data["ca.crt"]) > 0, nil
		}, wait.WithTimeout(120*time.Second), wait.WithInterval(3*time.Second))
		if err != nil {
			return ctx, fmt.Errorf("CA secret %s/%s not issued: %w", caSecretNS, caSecretName, err)
		}
		cm := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: targetCM, Namespace: targetNS},
			Data:       map[string]string{"service-ca.crt": string(secret.Data["ca.crt"])},
		}
		if err := r.Create(ctx, cm); err != nil && !apierrors.IsAlreadyExists(err) {
			return ctx, fmt.Errorf("publish CA configmap %s/%s: %w", targetNS, targetCM, err)
		}
		return ctx, nil
	}
}
