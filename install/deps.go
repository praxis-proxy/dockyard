package install

import (
	"time"

	"sigs.k8s.io/e2e-framework/pkg/env"
)

// Deps builds the ordered dependency stack a product installs before its tests
// run (the vessel's fitting-out). Each With* appends one install step from the
// generic installers in this package; Build returns them in call order. This is
// the builder the Vessel's InstallDeps composes — the generic layer owns the
// building blocks, the product owns the order (e.g. the GIE CRD must land before
// istio):
//
//	install.NewDeps().
//	    WithMetalLB(install.MetalLBVersion(v)).
//	    WithGatewayAPI(install.GatewayAPIVersion(v)).
//	    WithCRDs("manifests/gie").
//	    WithIstio(install.IstioInferenceExtension(true)).
//	    WithImage(clusterName, image, isKind).
//	    WithManifests("manifests/backends", "*.yaml").
//	    WithDeploymentAvailable("llm", "internal-model", 3*time.Minute).
//	    Build()
//
// For a product with its own installer (a deploy.sh, a helm chart, operators), use
// the delegation methods (WithHelm/WithScript/WithManifest) so L1 does
// not re-encode the deploy. A consumer picks per chain: lightweight (Dockyard
// installs infra + an in-process controller) vs full platform (the product's own
// installer stands everything up; Dockyard just drives trials).
type Deps struct{ fns []env.Func }

// NewDeps starts an empty dependency stack.
func NewDeps() *Deps { return &Deps{} }

// With appends an arbitrary env.Func, for a step with no dedicated builder method.
func (d *Deps) With(fn env.Func) *Deps {
	d.fns = append(d.fns, fn)
	return d
}

// WithMetalLB appends the metallb install (LoadBalancer IPs for the gateway).
func (d *Deps) WithMetalLB(opts ...MetalLBOption) *Deps { return d.With(InstallMetalLB(opts...)) }

// WithGatewayAPI appends the upstream Gateway API CRD install.
func (d *Deps) WithGatewayAPI(opts ...GatewayAPIOption) *Deps {
	return d.With(InstallGatewayAPI(opts...))
}

// WithIstio appends the istio install.
func (d *Deps) WithIstio(opts ...IstioOption) *Deps { return d.With(InstallIstio(opts...)) }

// WithCertManager appends the cert-manager install (TLS issuance for kind).
func (d *Deps) WithCertManager(opts ...CertManagerOption) *Deps {
	return d.With(InstallCertManager(opts...))
}

// WithKuadrant appends the kuadrant-operator install (the ext-proc filters anchor
// to the WasmPlugin Kuadrant attaches to the gateway).
func (d *Deps) WithKuadrant(opts ...KuadrantOption) *Deps {
	return d.With(InstallKuadrant(opts...))
}

// WithManifestFile appends a retrying server-side-apply of a manifest file (rides
// out an admission webhook that is still coming up).
func (d *Deps) WithManifestFile(path string) *Deps { return d.With(ApplyManifestFile(path)) }

// WithCABundle appends a step that publishes a cert-manager CA secret's ca.crt as
// a ConfigMap (key service-ca.crt) for the gateway to mount.
func (d *Deps) WithCABundle(caSecretNS, caSecretName, targetNS, targetCM string) *Deps {
	return d.With(PublishCABundle(caSecretNS, caSecretName, targetNS, targetCM))
}

// WithCRDs appends a CRD-apply-and-wait-Established step for the given dirs.
func (d *Deps) WithCRDs(dirs ...string) *Deps { return d.With(InstallCRDs(dirs...)) }

// WithImage appends a kind image-load step (no-op when the cluster isn't kind).
func (d *Deps) WithImage(clusterName, image string, isKind bool) *Deps {
	return d.With(LoadImage(clusterName, image, isKind))
}

// WithBuiltImage appends a docker build of contextDir, then loads the resulting
// tag into the kind cluster — for sims a consumer builds from its own source.
func (d *Deps) WithBuiltImage(contextDir, tag, clusterName string, isKind bool) *Deps {
	return d.With(BuildImage(contextDir, tag)).With(LoadImage(clusterName, tag, isKind))
}

// WithBuiltImageFrom appends a docker build with an explicit Dockerfile/Containerfile
// path, then loads the tag into the kind cluster.
func (d *Deps) WithBuiltImageFrom(contextDir, dockerfile, tag, clusterName string, isKind bool) *Deps {
	return d.With(BuildImageFrom(contextDir, dockerfile, tag)).With(LoadImage(clusterName, tag, isKind))
}

// WithPulledImage appends a docker pull of a published image, then loads it into the
// kind cluster — for public fixture images (e.g. a pinned llm-katan digest).
func (d *Deps) WithPulledImage(image, clusterName string, isKind bool) *Deps {
	return d.With(PullImage(image)).With(LoadImage(clusterName, image, isKind))
}

// WithRecorder appends a pull + kind-load of the published recorder sim image
// (DefaultRecorderRegistry:DefaultRecorderTag, overridable via image), so a
// consumer uses the recorder without building it from source. The consumer still
// references the image name (RecorderImage) in its own backend manifest.
func (d *Deps) WithRecorder(clusterName string, isKind bool, image string) *Deps {
	if image == "" {
		image = RecorderImage(DefaultRecorderTag)
	}
	return d.With(PullImage(image)).With(LoadImage(clusterName, image, isKind))
}

// RecorderImage returns the published recorder image reference for a tag.
func RecorderImage(tag string) string {
	if tag == "" {
		tag = DefaultRecorderTag
	}
	return DefaultRecorderRegistry + ":" + tag
}

// WithNamespace appends an idempotent Namespace apply (create-or-reconcile labels).
func (d *Deps) WithNamespace(name string, labels map[string]string) *Deps {
	return d.With(EnsureNamespace(name, labels))
}

// WithManifests appends a suite-level manifest-apply step (shared infra).
func (d *Deps) WithManifests(dir, pattern string) *Deps { return d.With(ApplyManifests(dir, pattern)) }

// WithDeploymentAvailable appends a wait-for-Deployment-Available step.
func (d *Deps) WithDeploymentAvailable(namespace, name string, timeout time.Duration) *Deps {
	return d.With(WaitForDeploymentAvailable(namespace, name, timeout))
}

// WithHelm appends a chart upgrade-or-install step via the Helm Go SDK (no helm
// binary).
func (d *Deps) WithHelm(c HelmChart) *Deps { return d.With(Helm(c)) }

// WithManifest appends a one-shot server-side-apply of a manifest file or URL
// (pure Go, no retry). For the retrying variant use WithManifestFile.
func (d *Deps) WithManifest(pathOrURL string) *Deps { return d.With(Manifest(pathOrURL)) }

// WithScript appends a step that runs a product's own installer script (deploy.sh)
// with the suite kubeconfig plus any KEY=VALUE environment entries.
func (d *Deps) WithScript(path string, env ...string) *Deps { return d.With(Script(path, env...)) }

// Build returns the accumulated steps in call order.
func (d *Deps) Build() []env.Func { return d.fns }
