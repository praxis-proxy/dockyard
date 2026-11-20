package install

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
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
	"k8s.io/client-go/util/retry"
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

// schemeMu guards scheme registration: resources.New may hand back a shared scheme,
// and runtime.Scheme.AddKnownTypes is not safe for concurrent writes — so parallel
// installers (Parallel/WithParallel) that each build a client would otherwise race
// and panic with a concurrent map write.
var schemeMu sync.Mutex

// newResources builds a resources client with the core (client-go) types
// registered, so typed objects decode; everything else falls back to unstructured.
func newResources(cfg *envconf.Config) (*resources.Resources, error) {
	r, err := resources.New(cfg.Client().RESTConfig())
	if err != nil {
		return nil, fmt.Errorf("new resources client: %w", err)
	}
	schemeMu.Lock()
	defer schemeMu.Unlock()
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
	cl := r.GetControllerRuntimeClient()
	force := true
	return func(ctx context.Context, obj k8s.Object) error {
		if obj.GetObjectKind().GroupVersionKind().Empty() {
			gvks, _, err := sch.ObjectKinds(obj)
			if err != nil || len(gvks) == 0 {
				return fmt.Errorf("resolve GVK for %T: %w", obj, err)
			}
			obj.GetObjectKind().SetGroupVersionKind(gvks[0])
		}
		// Default a namespaced object with no namespace to "default", matching
		// `kubectl apply -f` (which uses the context namespace) — some rendered
		// bundles omit the namespace on namespaced resources.
		if obj.GetNamespace() == "" {
			if namespaced, err := cl.IsObjectNamespaced(obj); err == nil && namespaced {
				obj.SetNamespace("default")
			}
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
	keep := kindMatcher(excludeKinds, false)
	return func(ctx context.Context, cfg *envconf.Config) (context.Context, error) {
		return ctx, applyFilteredDocs(ctx, cfg, bytes.NewReader(data), keep, "filtered manifest")
	}
}

// ApplyManifestOnlyKinds server-side-applies only the documents in a file whose Kind
// is in includeKinds (the Go equivalent of `yq 'select(.kind == ...)'`), e.g. to
// apply just the AITenant objects from a mixed fixture file.
func ApplyManifestOnlyKinds(path string, includeKinds ...string) env.Func {
	keep := kindMatcher(includeKinds, true)
	return func(ctx context.Context, cfg *envconf.Config) (context.Context, error) {
		return ctx, applyManifestFiltered(ctx, cfg, path, keep)
	}
}

// ApplyManifestExcludingKinds server-side-applies every document in a file whose Kind
// is NOT in excludeKinds (the Go equivalent of `yq 'select(.kind != ...)'`), e.g. to
// apply a fixture file while skipping its AITenant (applied earlier).
func ApplyManifestExcludingKinds(path string, excludeKinds ...string) env.Func {
	keep := kindMatcher(excludeKinds, false)
	return func(ctx context.Context, cfg *envconf.Config) (context.Context, error) {
		return ctx, applyManifestFiltered(ctx, cfg, path, keep)
	}
}

// kindMatcher returns a predicate over an object's Kind. With include=true it keeps only
// kinds in the set (an OnlyKinds filter); with include=false it keeps every kind except
// those in the set (an ExcludingKinds filter).
func kindMatcher(kinds []string, include bool) func(kind string) bool {
	set := make(map[string]bool, len(kinds))
	for _, k := range kinds {
		set[k] = true
	}
	return func(kind string) bool { return set[kind] == include }
}

// applyManifestFiltered opens a manifest file and SSA-applies the documents matching keep.
func applyManifestFiltered(ctx context.Context, cfg *envconf.Config, path string, keep func(kind string) bool) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	return applyFilteredDocs(ctx, cfg, f, keep, path)
}

// applyFilteredDocs decodes every YAML document from r and server-side-applies those whose
// Kind passes keep. what names the source for error wrapping.
func applyFilteredDocs(ctx context.Context, cfg *envconf.Config, r io.Reader, keep func(kind string) bool, what string) error {
	res, err := newResources(cfg)
	if err != nil {
		return err
	}
	apply := ssaHandler(res)
	handler := func(ctx context.Context, obj k8s.Object) error {
		if !keep(obj.GetObjectKind().GroupVersionKind().Kind) {
			return nil
		}
		return apply(ctx, obj)
	}
	if err := decoder.DecodeEach(ctx, r, handler); err != nil {
		return fmt.Errorf("apply %s: %w", what, err)
	}
	return nil
}

// WaitForPhase blocks until an object's status.phase equals want (e.g. a MaaS
// Subscription or ExternalModel reaching "Active"/"Ready" before dependent steps).
func WaitForPhase(gvk schema.GroupVersionKind, namespace, name, want string, timeout time.Duration) env.Func {
	return func(ctx context.Context, cfg *envconf.Config) (context.Context, error) {
		r, err := newResources(cfg)
		if err != nil {
			return ctx, err
		}
		obj := &unstructured.Unstructured{}
		obj.SetGroupVersionKind(gvk)
		var last string
		err = wait.For(func(ctx context.Context) (bool, error) {
			if e := r.Get(ctx, name, namespace, obj); e != nil {
				if apierrors.IsNotFound(e) {
					return false, nil
				}
				return false, e
			}
			last, _, _ = unstructured.NestedString(obj.Object, "status", "phase")
			return last == want, nil
		}, wait.WithContext(ctx), wait.WithTimeout(timeout), wait.WithInterval(defaultWaitInterval))
		if err != nil {
			return ctx, fmt.Errorf("%s %s/%s phase=%q, want %q: %w", gvk.Kind, namespace, name, last, want, err)
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
	if err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var dep appsv1.Deployment
		if err := r.Get(ctx, name, ns, &dep); err != nil {
			return err
		}
		if dep.Spec.Template.Annotations == nil {
			dep.Spec.Template.Annotations = map[string]string{}
		}
		dep.Spec.Template.Annotations["dockyard.dev/restartedAt"] = time.Now().Format(time.RFC3339Nano)
		return r.Update(ctx, &dep)
	}); err != nil {
		return fmt.Errorf("restart deployment %s/%s: %w", ns, name, err)
	}
	return nil
}
