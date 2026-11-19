package install

import (
	"context"
	"encoding/json"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/e2e-framework/klient/k8s"
	"sigs.k8s.io/e2e-framework/pkg/env"
	"sigs.k8s.io/e2e-framework/pkg/envconf"
)

// EnsureNamespace server-side-applies a Namespace with the given labels (idempotent:
// creates it if absent, reconciles the labels if present). Labels may be nil.
func EnsureNamespace(name string, labels map[string]string) env.Func {
	return func(ctx context.Context, cfg *envconf.Config) (context.Context, error) {
		r, err := newResources(cfg)
		if err != nil {
			return ctx, err
		}
		ns := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{Name: name, Labels: labels},
		}
		ns.SetGroupVersionKind(corev1.SchemeGroupVersion.WithKind("Namespace"))
		data, err := json.Marshal(ns)
		if err != nil {
			return ctx, err
		}
		force := true
		if err := r.Patch(ctx, ns, k8s.Patch{PatchType: types.ApplyPatchType, Data: data},
			func(o *metav1.PatchOptions) { o.FieldManager = fieldManager; o.Force = &force }); err != nil {
			return ctx, fmt.Errorf("ensure namespace %s: %w", name, err)
		}
		return ctx, nil
	}
}
