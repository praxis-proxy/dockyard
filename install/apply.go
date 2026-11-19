package install

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/e2e-framework/klient/decoder"
	"sigs.k8s.io/e2e-framework/klient/k8s"
	"sigs.k8s.io/e2e-framework/klient/k8s/resources"
	"sigs.k8s.io/e2e-framework/klient/wait"
	"sigs.k8s.io/e2e-framework/klient/wait/conditions"
	"sigs.k8s.io/e2e-framework/pkg/env"
	"sigs.k8s.io/e2e-framework/pkg/envconf"
)

// fieldManager is the server-side-apply field owner Dockyard's applies use.
const fieldManager = "dockyard"

// defaultWaitInterval is the poll cadence for the install waits.
const defaultWaitInterval = 3 * time.Second

// Everything in this file is pure Go against the suite's API server (klient
// decoder + resources client): no kubectl binary. The decoder falls back to
// unstructured for any kind not in the scheme, so vendor manifests (CRDs and their
// CRs) apply without registering their types.

// newResources builds a resources client with the core (client-go) types
// registered, so typed objects decode; everything else falls back to unstructured.
func newResources(cfg *envconf.Config) (*resources.Resources, error) {
	r, err := resources.New(cfg.Client().RESTConfig())
	if err != nil {
		return nil, fmt.Errorf("new resources client: %w", err)
	}
	_ = clientgoscheme.AddToScheme(r.GetScheme())
	_ = apiextensionsv1.AddToScheme(r.GetScheme())
	return r, nil
}

// ssaHandler applies each decoded object with server-side apply — idempotent, so a
// re-run against a reused cluster updates rather than failing on AlreadyExists. The
// apply body needs apiVersion/kind, so set the GVK (from the scheme for typed
// objects; already present on unstructured ones) before marshaling.
func ssaHandler(r *resources.Resources) decoder.HandlerFunc {
	sch := r.GetScheme()
	force := true
	return func(ctx context.Context, obj k8s.Object) error {
		if obj.GetObjectKind().GroupVersionKind().Empty() {
			gvks, _, err := sch.ObjectKinds(obj)
			if err != nil || len(gvks) == 0 {
				return fmt.Errorf("resolve GVK for %T: %w", obj, err)
			}
			obj.GetObjectKind().SetGroupVersionKind(gvks[0])
		}
		data, err := json.Marshal(obj)
		if err != nil {
			return fmt.Errorf("marshal %s: %w", obj.GetObjectKind().GroupVersionKind(), err)
		}
		if err := r.Patch(ctx, obj, k8s.Patch{PatchType: types.ApplyPatchType, Data: data},
			func(o *metav1.PatchOptions) { o.FieldManager = fieldManager; o.Force = &force }); err != nil {
			return fmt.Errorf("apply %s %s/%s: %w",
				obj.GetObjectKind().GroupVersionKind().Kind, obj.GetNamespace(), obj.GetName(), err)
		}
		return nil
	}
}

// applyURL fetches a manifest URL and server-side-applies every document in it.
func applyURL(ctx context.Context, cfg *envconf.Config, url string) error {
	r, err := newResources(cfg)
	if err != nil {
		return err
	}
	if err := decoder.DecodeURL(ctx, url, ssaHandler(r)); err != nil {
		return fmt.Errorf("apply %s: %w", url, err)
	}
	return nil
}

// applyYAML server-side-applies every document in an in-memory manifest string.
func applyYAML(ctx context.Context, cfg *envconf.Config, manifest string) error {
	r, err := newResources(cfg)
	if err != nil {
		return err
	}
	if err := decoder.DecodeEach(ctx, strings.NewReader(manifest), ssaHandler(r)); err != nil {
		return fmt.Errorf("apply inline manifest: %w", err)
	}
	return nil
}

// ApplyYAMLBytes server-side-applies every document in an in-memory manifest (for
// callers that render/transform YAML themselves, e.g. kustomize output).
func ApplyYAMLBytes(data []byte) env.Func {
	return func(ctx context.Context, cfg *envconf.Config) (context.Context, error) {
		return ctx, applyYAML(ctx, cfg, string(data))
	}
}

// ApplyYAMLBytesExcludingKinds server-side-applies every document except those whose
// Kind is in excludeKinds (the Go equivalent of piping through `yq 'select(.kind !=
// ...)'`). Useful for rendered bundles carrying resources a kind cluster can't honor
// (e.g. ServiceMonitor, ValidatingWebhookConfiguration without service-ca).
func ApplyYAMLBytesExcludingKinds(data []byte, excludeKinds ...string) env.Func {
	skip := make(map[string]bool, len(excludeKinds))
	for _, k := range excludeKinds {
		skip[k] = true
	}
	return func(ctx context.Context, cfg *envconf.Config) (context.Context, error) {
		r, err := newResources(cfg)
		if err != nil {
			return ctx, err
		}
		apply := ssaHandler(r)
		handler := func(ctx context.Context, obj k8s.Object) error {
			if skip[obj.GetObjectKind().GroupVersionKind().Kind] {
				return nil
			}
			return apply(ctx, obj)
		}
		if err := decoder.DecodeEach(ctx, bytes.NewReader(data), handler); err != nil {
			return ctx, fmt.Errorf("apply filtered manifest: %w", err)
		}
		return ctx, nil
	}
}

// ApplyManifestOnlyKinds server-side-applies only the documents in a file whose Kind
// is in includeKinds (the Go equivalent of `yq 'select(.kind == ...)'`), e.g. to
// apply just the AITenant objects from a mixed fixture file.
func ApplyManifestOnlyKinds(path string, includeKinds ...string) env.Func {
	keep := make(map[string]bool, len(includeKinds))
	for _, k := range includeKinds {
		keep[k] = true
	}
	return func(ctx context.Context, cfg *envconf.Config) (context.Context, error) {
		r, err := newResources(cfg)
		if err != nil {
			return ctx, err
		}
		f, err := os.Open(path)
		if err != nil {
			return ctx, err
		}
		defer func() { _ = f.Close() }()
		apply := ssaHandler(r)
		handler := func(ctx context.Context, obj k8s.Object) error {
			if !keep[obj.GetObjectKind().GroupVersionKind().Kind] {
				return nil
			}
			return apply(ctx, obj)
		}
		if err := decoder.DecodeEach(ctx, f, handler); err != nil {
			return ctx, fmt.Errorf("apply %s: %w", path, err)
		}
		return ctx, nil
	}
}

// AnnotateResource merge-patches annotations onto an object identified by GVK.
func AnnotateResource(gvk schema.GroupVersionKind, namespace, name string, annotations map[string]string) env.Func {
	return func(ctx context.Context, cfg *envconf.Config) (context.Context, error) {
		r, err := newResources(cfg)
		if err != nil {
			return ctx, err
		}
		obj := &unstructured.Unstructured{}
		obj.SetGroupVersionKind(gvk)
		patch, err := json.Marshal(map[string]any{"metadata": map[string]any{"annotations": annotations}})
		if err != nil {
			return ctx, err
		}
		obj.SetNamespace(namespace)
		obj.SetName(name)
		if err := r.Patch(ctx, obj, k8s.Patch{PatchType: types.MergePatchType, Data: patch}); err != nil {
			return ctx, fmt.Errorf("annotate %s %s/%s: %w", gvk.Kind, namespace, name, err)
		}
		return ctx, nil
	}
}

// WaitForAnnotation blocks until an object's annotation equals want (e.g. a
// controller-driven status handshake).
func WaitForAnnotation(gvk schema.GroupVersionKind, namespace, name, key, want string, timeout time.Duration) env.Func {
	return func(ctx context.Context, cfg *envconf.Config) (context.Context, error) {
		r, err := newResources(cfg)
		if err != nil {
			return ctx, err
		}
		obj := &unstructured.Unstructured{}
		obj.SetGroupVersionKind(gvk)
		err = wait.For(func(ctx context.Context) (bool, error) {
			if e := r.Get(ctx, name, namespace, obj); e != nil {
				if apierrors.IsNotFound(e) {
					return false, nil
				}
				return false, e
			}
			return obj.GetAnnotations()[key] == want, nil
		}, wait.WithContext(ctx), wait.WithTimeout(timeout), wait.WithInterval(defaultWaitInterval))
		if err != nil {
			return ctx, fmt.Errorf("%s %s/%s annotation %s!=%q: %w", gvk.Kind, namespace, name, key, want, err)
		}
		return ctx, nil
	}
}

// WaitForResource blocks until an object of the given GVK exists (e.g. a CR a
// controller creates). namespace may be empty for cluster-scoped kinds.
func WaitForResource(gvk schema.GroupVersionKind, namespace, name string, timeout time.Duration) env.Func {
	return func(ctx context.Context, cfg *envconf.Config) (context.Context, error) {
		r, err := newResources(cfg)
		if err != nil {
			return ctx, err
		}
		obj := &unstructured.Unstructured{}
		obj.SetGroupVersionKind(gvk)
		err = wait.For(func(ctx context.Context) (bool, error) {
			if e := r.Get(ctx, name, namespace, obj); e != nil {
				if apierrors.IsNotFound(e) {
					return false, nil
				}
				return false, e
			}
			return true, nil
		}, wait.WithContext(ctx), wait.WithTimeout(timeout), wait.WithInterval(defaultWaitInterval))
		if err != nil {
			return ctx, fmt.Errorf("resource %s %s/%s not found: %w", gvk.Kind, namespace, name, err)
		}
		return ctx, nil
	}
}

// applyFile server-side-applies every document in a manifest file on disk.
func applyFile(ctx context.Context, cfg *envconf.Config, path string) error {
	r, err := newResources(cfg)
	if err != nil {
		return err
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	if err := decoder.DecodeEach(ctx, f, ssaHandler(r)); err != nil {
		return fmt.Errorf("apply %s: %w", path, err)
	}
	return nil
}

// crdEstablished reports whether a CRD object has the Established condition True.
func crdEstablished(obj k8s.Object) bool {
	c, ok := obj.(*apiextensionsv1.CustomResourceDefinition)
	if !ok {
		return false
	}
	for _, cond := range c.Status.Conditions {
		if cond.Type == apiextensionsv1.Established && cond.Status == apiextensionsv1.ConditionTrue {
			return true
		}
	}
	return false
}

// waitCRDsEstablished blocks until each named CRD reports Established.
func waitCRDsEstablished(ctx context.Context, r *resources.Resources, timeout time.Duration, names ...string) error {
	for _, name := range names {
		crd := &apiextensionsv1.CustomResourceDefinition{}
		crd.SetName(name)
		if err := wait.For(
			conditions.New(r).ResourceMatch(crd, crdEstablished),
			wait.WithContext(ctx), wait.WithTimeout(timeout), wait.WithInterval(time.Second),
		); err != nil {
			return fmt.Errorf("CRD %s not Established: %w", name, err)
		}
	}
	return nil
}

// waitDeploymentAvailable blocks until a Deployment exists and reports Available.
// A not-yet-created Deployment is treated as "keep waiting", so it doubles as an
// until-exists-then-ready wait.
func waitDeploymentAvailable(ctx context.Context, r *resources.Resources, ns, name string, timeout time.Duration) error {
	dep := &appsv1.Deployment{}
	err := wait.For(func(ctx context.Context) (bool, error) {
		if e := r.Get(ctx, name, ns, dep); e != nil {
			if apierrors.IsNotFound(e) {
				return false, nil
			}
			return false, e
		}
		for _, c := range dep.Status.Conditions {
			if c.Type == appsv1.DeploymentAvailable && c.Status == corev1.ConditionTrue {
				return true, nil
			}
		}
		return false, nil
	}, wait.WithContext(ctx), wait.WithTimeout(timeout), wait.WithInterval(defaultWaitInterval))
	if err != nil {
		return fmt.Errorf("deployment %s/%s not Available: %w", ns, name, err)
	}
	return nil
}

// waitAllDeploymentsAvailable waits for every Deployment currently in a namespace
// to report Available (the Go equivalent of `kubectl wait --all deploy -n ns`).
func waitAllDeploymentsAvailable(ctx context.Context, r *resources.Resources, ns string, timeout time.Duration) error {
	var list appsv1.DeploymentList
	if err := r.WithNamespace(ns).List(ctx, &list); err != nil {
		return fmt.Errorf("list deployments in %s: %w", ns, err)
	}
	for i := range list.Items {
		if err := waitDeploymentAvailable(ctx, r, ns, list.Items[i].Name, timeout); err != nil {
			return err
		}
	}
	return nil
}

// restartDeployment forces a rollout by stamping a restart annotation on the pod
// template (the Go equivalent of `kubectl rollout restart deployment/name`).
func restartDeployment(ctx context.Context, r *resources.Resources, ns, name string) error {
	var dep appsv1.Deployment
	if err := r.Get(ctx, name, ns, &dep); err != nil {
		return fmt.Errorf("get deployment %s/%s: %w", ns, name, err)
	}
	if dep.Spec.Template.Annotations == nil {
		dep.Spec.Template.Annotations = map[string]string{}
	}
	dep.Spec.Template.Annotations["dockyard.dev/restartedAt"] = time.Now().Format(time.RFC3339Nano)
	if err := r.Update(ctx, &dep); err != nil {
		return fmt.Errorf("restart deployment %s/%s: %w", ns, name, err)
	}
	return nil
}
