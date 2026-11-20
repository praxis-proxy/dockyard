package funcs

import (
	"context"
	"fmt"
	"strings"

	"sigs.k8s.io/e2e-framework/klient/decoder"
	"sigs.k8s.io/e2e-framework/pkg/env"
	"sigs.k8s.io/e2e-framework/pkg/envconf"
	"sigs.k8s.io/kustomize/api/krusty"
	"sigs.k8s.io/kustomize/kyaml/filesys"
)

// Kustomize renders a kustomize directory in-process (no kustomize binary) and
// server-side-applies the result. A generic replacement for `kustomize build dir |
// kubectl apply -f -`. For image/namespace rewrites, prefer encoding them in the
// kustomization (images:, namespace:) so the render is self-contained.
func Kustomize(dir string) env.Func {
	return func(ctx context.Context, cfg *envconf.Config) (context.Context, error) {
		rendered, err := RenderKustomize(dir)
		if err != nil {
			return ctx, err
		}
		r, err := newResources(cfg)
		if err != nil {
			return ctx, err
		}
		if err := decoder.DecodeEach(ctx, strings.NewReader(string(rendered)), ssaHandler(r)); err != nil {
			return ctx, fmt.Errorf("apply kustomize %s: %w", dir, err)
		}
		return ctx, nil
	}
}

// RenderKustomize builds a kustomize directory to YAML in-process, without applying
// it — for callers that need to transform the output before apply.
func RenderKustomize(dir string) ([]byte, error) {
	k := krusty.MakeKustomizer(krusty.MakeDefaultOptions())
	resMap, err := k.Run(filesys.MakeFsOnDisk(), dir)
	if err != nil {
		return nil, fmt.Errorf("kustomize build %s: %w", dir, err)
	}
	y, err := resMap.AsYaml()
	if err != nil {
		return nil, fmt.Errorf("kustomize render %s: %w", dir, err)
	}
	return y, nil
}
