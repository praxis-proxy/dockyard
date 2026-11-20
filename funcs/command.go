// Package funcs is the generic, reusable helper library: env.Func and
// features.Func builders a consumer composes in its TestMain and tests. It holds
// the dependency installers for cluster add-ons (metallb, Gateway API, istio,
// cert-manager, kuadrant), CRD apply + waits, image build/load, the Deps builder,
// per-test waits, port-forwarding, pod exec/logs, and in-process cert minting.
//
// Manifest apply and readiness waits are pure Go against the API server (klient
// decoder + resources client, server-side apply) — no kubectl binary — and both
// Helm (helm.go) and istio (istio.go, via its Helm charts) run through the Helm Go
// SDK, so there is no helm or istioctl binary either. The only shell-outs left are
// docker and kind, for building and loading images (no practical in-process Go
// path). The Helm and Script delegation primitives let a consumer stand up a
// product's own installer without the framework re-encoding it (the "full
// platform" escape hatch).
package funcs

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"sigs.k8s.io/e2e-framework/pkg/env"
	"sigs.k8s.io/e2e-framework/pkg/envconf"
)

// --- delegation primitives (the "full platform" escape hatch) ----------------
//
// So a consumer deploying a real product (its own deploy.sh, operators, a helm
// chart) doesn't force Dockyard to re-encode it, L1 ships these generic escape
// hatches the Deps builder composes. They run against the suite's kubeconfig.

// Manifest server-side-applies a manifest file (path on disk) or URL — pure Go, no
// kubectl. For a retrying variant (an admission webhook still coming up) use
// ApplyManifestFile.
func Manifest(pathOrURL string) env.Func {
	return func(ctx context.Context, cfg *envconf.Config) (context.Context, error) {
		if strings.HasPrefix(pathOrURL, "http://") || strings.HasPrefix(pathOrURL, "https://") {
			return ctx, applyURL(ctx, cfg, pathOrURL)
		}
		return ctx, applyFile(ctx, cfg, pathOrURL)
	}
}

// Script runs an executable script with the suite kubeconfig in its environment,
// plus any extra KEY=VALUE entries — e.g. a product's deploy.sh. The script is run
// directly (not through a shell), so it must be executable and self-contained.
func Script(path string, env ...string) env.Func {
	return func(ctx context.Context, cfg *envconf.Config) (context.Context, error) {
		cmd := exec.CommandContext(ctx, path)
		cmd.Env = append(append(os.Environ(), "KUBECONFIG="+cfg.KubeconfigFile()), env...)
		if out, err := cmd.CombinedOutput(); err != nil {
			return ctx, fmt.Errorf("script %s: %w\n%s", path, err, out)
		}
		return ctx, nil
	}
}

// --- shell plumbing (for the no-Go-path tools: docker, kind) -----------------

func sh(ctx context.Context, cfg *envconf.Config, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = append(os.Environ(), "KUBECONFIG="+cfg.KubeconfigFile())
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%s %s: %w\n%s", name, strings.Join(args, " "), err, out)
	}
	return nil
}

func kindSubnetBase(ctx context.Context) (string, error) {
	// The kind network is dual-stack; list all subnets and pick the IPv4 one
	// (metallb pool must be IPv4 to match the host-routable LB IP).
	out, err := exec.CommandContext(ctx, "docker", "network", "inspect", "kind",
		"-f", "{{range .IPAM.Config}}{{.Subnet}} {{end}}").Output()
	if err != nil {
		return "", fmt.Errorf("docker network inspect kind: %w", err)
	}
	for _, subnet := range strings.Fields(string(out)) {
		ip, _, _ := strings.Cut(subnet, "/")
		if strings.Contains(ip, ".") && !strings.Contains(ip, ":") { // IPv4
			if octets := strings.Split(ip, "."); len(octets) >= 3 {
				return strings.Join(octets[:3], "."), nil
			}
		}
	}
	return "", fmt.Errorf("no IPv4 subnet on the kind network (got %q)", strings.TrimSpace(string(out)))
}
