<p align="center">
  <img src="assets/gophers/dockyard.svg" alt="A hard-hatted Go gopher riding a shipping container onto a ship" width="320">
</p>

# Dockyard

A shared, cluster-agnostic Go e2e framework. It provisions a real platform (a
`kind` cluster or an existing kubeconfig), installs the dependencies your control
plane needs, and runs the tests. Built on
[`sigs.k8s.io/e2e-framework`](https://github.com/kubernetes-sigs/e2e-framework) and
structured after [Crossplane's `test/e2e`](https://github.com/crossplane/crossplane/tree/main/test/e2e).

## How a consumer uses it

Dockyard is generic, with no product-specific knowledge. A consumer's `TestMain`
builds an `Environment` from `config`, registers suites, and composes setup from the
reusable helpers in `funcs` — then owns its control plane, CRD types, and dependency
stack directly. There is no framework-level seam to implement:

```go
var environment = config.NewEnvironmentFromFlags()

func TestMain(m *testing.M) {
    cfg, _ := envconf.NewFromFlags()
    environment.AddDefaultTestSuite(/* labels, conditional setup */)

    var setup []env.Func
    if environment.ShouldCreateCluster() {
        setup = append(setup, environment.CreateClusterFunc())
    } else {
        cfg.WithKubeconfigFile(conf.ResolveKubeConfigFile())
    }
    environment.SetEnvironment(env.NewWithConfig(cfg))

    // Compose the dependency stack from the generic helpers.
    if environment.ShouldInstallDeps() {
        setup = append(setup, funcs.NewDeps().
            WithGatewayAPI(funcs.GatewayAPIVersion(environment.GatewayAPIVersion())).
            WithCertManager().
            WithIstio().
            Build()...)
    }
    setup = append(setup, addTypesToScheme())   // the consumer's CRD types

    environment.Setup(setup...)
    os.Exit(environment.Run(m))
}
```

## Packages

| Package        | What it holds                                                        |
|----------------|---------------------------------------------------------------------|
| `config`       | `Environment` (flag-driven cluster lifecycle), suites, labels       |
| `funcs`        | Reusable `env.Func`/`features.Func` helpers: the generic installers (metallb, Gateway API, istio, cert-manager, kuadrant), CRD/image/manifest steps, delegation primitives (`Helm`/`Script`/`Manifest`), the `Deps` builder, per-test waits, port-forward, pod exec/logs, cert minting |
| `gateway`      | A thin HTTP SDK for OpenAI-compatible gateway endpoints              |
| `stats`        | Dependency-free statistics for tests (Pearson chi-square goodness-of-fit for weighted-routing) |
| `sim/recorder` | A self-identifying sim backend (own module; published as an image)   |

## Two modes

A consumer picks per `Deps` chain:

- **Lightweight** — Dockyard installs the infra add-ons and the consumer runs its
  controller in-process.
- **Full platform** — the product's own installer (a `deploy.sh`, a helm chart,
  operators) stands everything up via the delegation primitives
  (`WithScript`/`WithHelm`/`WithManifest`); Dockyard just drives the tests. Dockyard
  never double-installs because the consumer composes the chain.

## External tools

Manifest apply and readiness waits run in-process against the API server (klient
decoder + resources client, server-side apply) — **no `kubectl` binary** — and both
Helm releases and istio (via its official Helm charts) go through the Helm Go SDK, so
there is **no `helm` or `istioctl` binary** either. The only external tools Dockyard
shells out to are `docker` and `kind` (building and loading images — no practical Go
path), and only when the installer that needs them is composed into the chain:

| Tool     | Needed by                                           |
|----------|-----------------------------------------------------|
| `docker` | `WithBuiltImage`, `WithRecorder`; `WithMetalLB` only to derive the pool (avoid with `MetalLBAddressPool`) |
| `kind`   | cluster create (`-create-cluster=true`), and image load (`WithImage`/`WithBuiltImage`/`WithRecorder`) |

These are inherent to the **local-kind** workflow: a kind cluster runs Kubernetes
nodes as containers, so it needs a container runtime regardless. Running against an
existing cluster (`-create-cluster=false`) with images referenced from a registry
needs neither tool, and a control-plane-only consumer never touches them.

## Recorder

The recorder sim is published as an image (`ghcr.io/praxis-proxy/dockyard-recorder`)
so consumers reference it via `Deps.WithRecorder(...)` instead of building it from
source. `WithBuiltImage` remains for a consumer's own sims.
