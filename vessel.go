package dockyard

import "sigs.k8s.io/e2e-framework/pkg/env"

// Vessel is the per-repo half of the harness (the vessel fitting out): the thing
// under test (its control plane) together with the dependencies and CRD types it
// needs. The generic Environment owns cluster lifecycle, suites and labels, and
// delegates everything vessel-specific to a Vessel set via
// Environment.WithVessel.
//
// This is the seam the framework rests on: dockyard, install, feature and gateway
// are the generic L1 layer every control-plane repo reuses; each repo supplies its
// own Vessel (L3). Compose the dependency stack from the generic installers via
// install.NewDeps().
type Vessel interface {
	// InstallDeps returns the ordered dependency stack to install before tests
	// run, composed from the generic installers via install.NewDeps(): cluster
	// add-ons (metallb, Gateway API, istio), CRDs, images, and shared infra. It
	// receives the Environment for cluster-derived inputs (name, is-kind, flag
	// values). Runs only when Environment.ShouldInstallDeps() is true.
	InstallDeps(e *Environment) []env.Func

	// AddTypesToScheme registers the vessel's CRD types on the suite client
	// scheme, so typed Create/Get work for its kinds. Always run.
	AddTypesToScheme() env.Func

	// Enabled reports whether the vessel's control plane should run (e.g. the
	// -run-controller gate). When false, Install is skipped.
	Enabled() bool

	// Install returns setup funcs that run the vessel's control plane and a
	// teardown func. Called only when Enabled() is true. The teardown is
	// registered to run before the cluster is destroyed.
	Install() (setup []env.Func, teardown env.Func)
}
