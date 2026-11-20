package funcs

import (
	"context"
	"fmt"
	"time"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"sigs.k8s.io/e2e-framework/klient/decoder"
	"sigs.k8s.io/e2e-framework/klient/k8s/resources"
	"sigs.k8s.io/e2e-framework/klient/wait"
	"sigs.k8s.io/e2e-framework/klient/wait/conditions"
	"sigs.k8s.io/e2e-framework/pkg/env"
	"sigs.k8s.io/e2e-framework/pkg/envconf"
)

// InstallCRDs applies every given CRD directory and waits for each CRD to report
// Established, so the manager's/istio's REST mapper resolves every kind on first
// use (avoids the discovery-race retry storm). Paths are relative to the test
// package working directory.
func InstallCRDs(dirs ...string) env.Func {
	return func(ctx context.Context, cfg *envconf.Config) (context.Context, error) {
		r, err := resources.New(cfg.Client().RESTConfig())
		if err != nil {
			return ctx, fmt.Errorf("new resources client: %w", err)
		}
		if err := apiextensionsv1.AddToScheme(r.GetScheme()); err != nil {
			return ctx, fmt.Errorf("add apiextensions scheme: %w", err)
		}
		for _, dir := range dirs {
			if err := decoder.ApplyWithManifestDir(ctx, r, dir, "*.yaml", nil); err != nil {
				return ctx, fmt.Errorf("apply CRDs from %s: %w", dir, err)
			}
		}
		var crds apiextensionsv1.CustomResourceDefinitionList
		if err := r.List(ctx, &crds); err != nil {
			return ctx, fmt.Errorf("list CRDs: %w", err)
		}
		for i := range crds.Items {
			crd := &crds.Items[i]
			err := wait.For(
				conditions.New(r).ResourceMatch(crd, crdEstablished),
				wait.WithContext(ctx), wait.WithTimeout(60*time.Second), wait.WithInterval(time.Second),
			)
			if err != nil {
				return ctx, fmt.Errorf("CRD %s not Established: %w", crd.Name, err)
			}
		}
		return ctx, nil
	}
}
