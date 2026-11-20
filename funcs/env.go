package funcs

import (
	"context"
	"fmt"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/e2e-framework/klient/decoder"
	"sigs.k8s.io/e2e-framework/klient/k8s/resources"
	"sigs.k8s.io/e2e-framework/klient/wait"
	"sigs.k8s.io/e2e-framework/klient/wait/conditions"
	"sigs.k8s.io/e2e-framework/pkg/env"
	"sigs.k8s.io/e2e-framework/pkg/envconf"
	"sigs.k8s.io/e2e-framework/pkg/features"
)

// EnvFuncs runs env.Funcs in order, short-circuiting on the first error.
func EnvFuncs(fns ...env.Func) env.Func {
	return func(ctx context.Context, cfg *envconf.Config) (context.Context, error) {
		for _, fn := range fns {
			var err error
			if ctx, err = fn(ctx, cfg); err != nil {
				return ctx, err
			}
		}
		return ctx, nil
	}
}

// AsFeaturesFunc adapts an env.Func into a features.Func (fails the test on error).
func AsFeaturesFunc(fn env.Func) features.Func {
	return func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
		ctx, err := fn(ctx, cfg)
		if err != nil {
			t.Fatal(err)
		}
		return ctx
	}
}

// ApplyManifests applies a directory of manifests as a suite-level setup step
// (env.Func) — for shared environment infra (backends, etc.), not per-test CRs.
// Paths are relative to the test package working directory.
func ApplyManifests(dir, pattern string) env.Func {
	return func(ctx context.Context, cfg *envconf.Config) (context.Context, error) {
		r, err := resources.New(cfg.Client().RESTConfig())
		if err != nil {
			return ctx, err
		}
		_ = clientgoscheme.AddToScheme(r.GetScheme())
		if err := decoder.ApplyWithManifestDir(ctx, r, dir, pattern, nil); err != nil {
			return ctx, fmt.Errorf("apply manifests %s/%s: %w", dir, pattern, err)
		}
		return ctx, nil
	}
}

// WaitForDeploymentAvailable blocks until a Deployment reports Available (env.Func).
func WaitForDeploymentAvailable(namespace, name string, d time.Duration) env.Func {
	return func(ctx context.Context, cfg *envconf.Config) (context.Context, error) {
		dep := &appsv1.Deployment{}
		dep.SetName(name)
		dep.SetNamespace(namespace)
		err := wait.For(
			conditions.New(cfg.Client().Resources()).DeploymentConditionMatch(dep, appsv1.DeploymentAvailable, corev1.ConditionTrue),
			wait.WithTimeout(d), wait.WithInterval(3*time.Second),
		)
		if err != nil {
			return ctx, fmt.Errorf("deployment %s/%s not Available: %w", namespace, name, err)
		}
		return ctx, nil
	}
}
