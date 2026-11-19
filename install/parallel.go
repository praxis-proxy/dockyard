package install

import (
	"context"

	"golang.org/x/sync/errgroup"
	"sigs.k8s.io/e2e-framework/pkg/env"
	"sigs.k8s.io/e2e-framework/pkg/envconf"
)

// Parallel runs env.Funcs concurrently as a single stage, returning when all finish
// or on the first error (which cancels the rest via the derived context). It slots
// into a sequential Deps chain as one step, so provisioning becomes "sequential
// stages, each stage a concurrent group".
//
// Because the funcs run concurrently their context mutations cannot be threaded, so
// the original ctx is returned unchanged. Only group steps that are independent of
// one another's ctx values and ordering — e.g. the six image loads, or Gateway API +
// cert-manager. Keep ordered steps (istio before kuadrant; a cluster before the loads
// that target it) as separate sequential Deps entries.
func Parallel(fns ...env.Func) env.Func {
	return func(ctx context.Context, cfg *envconf.Config) (context.Context, error) {
		if len(fns) == 1 {
			return fns[0](ctx, cfg)
		}
		g, gctx := errgroup.WithContext(ctx)
		for _, fn := range fns {
			g.Go(func() error {
				_, err := fn(gctx, cfg)
				return err
			})
		}
		return ctx, g.Wait()
	}
}
