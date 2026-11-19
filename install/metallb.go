package install

import (
	"context"
	"fmt"
	"time"

	"sigs.k8s.io/e2e-framework/pkg/env"
	"sigs.k8s.io/e2e-framework/pkg/envconf"
)

const defaultMetalLBVersion = "v0.14.9"

// MetalLBOption configures InstallMetalLB.
type MetalLBOption func(*metalLBConfig)

type metalLBConfig struct {
	version     string
	addressPool string
}

// MetalLBVersion pins the metallb release. Empty is ignored (keeps the default).
func MetalLBVersion(v string) MetalLBOption {
	return func(c *metalLBConfig) {
		if v != "" {
			c.version = v
		}
	}
}

// MetalLBAddressPool sets the IPAddressPool range explicitly (e.g.
// "172.18.255.200-172.18.255.250" or a CIDR). When set, InstallMetalLB does not
// shell out to `docker network inspect` to derive it — the one docker dependency in
// the metallb path. Use it when docker isn't available or the kind network subnet
// is known ahead of time.
func MetalLBAddressPool(addresses string) MetalLBOption {
	return func(c *metalLBConfig) {
		if addresses != "" {
			c.addressPool = addresses
		}
	}
}

// InstallMetalLB installs metallb and an IPAddressPool so the gateway Service gets a
// host-routable LB IP (tests resolve the gateway's assigned IP from its Service, so
// the exact range doesn't need to be known ahead of time). The pool defaults to
// <kind-ipv4-subnet-base>.200-.250, derived from the kind docker network — the only
// docker dependency here; pass MetalLBAddressPool to supply it directly and avoid
// docker entirely.
func InstallMetalLB(opts ...MetalLBOption) env.Func {
	o := metalLBConfig{version: defaultMetalLBVersion}
	for _, opt := range opts {
		opt(&o)
	}
	return func(ctx context.Context, cfg *envconf.Config) (context.Context, error) {
		url := fmt.Sprintf("https://raw.githubusercontent.com/metallb/metallb/%s/config/manifests/metallb-native.yaml", o.version)
		if err := applyURL(ctx, cfg, url); err != nil {
			return ctx, err
		}
		r, err := newResources(cfg)
		if err != nil {
			return ctx, err
		}
		if err := waitDeploymentAvailable(ctx, r, "metallb-system", "controller", 180*time.Second); err != nil {
			return ctx, err
		}
		addresses := o.addressPool
		if addresses == "" {
			base, err := kindSubnetBase(ctx)
			if err != nil {
				return ctx, err
			}
			addresses = fmt.Sprintf("%s.200-%s.250", base, base)
		}
		pool := fmt.Sprintf(`apiVersion: metallb.io/v1beta1
kind: IPAddressPool
metadata: {name: e2e-pool, namespace: metallb-system}
spec: {addresses: ["%s"]}
---
apiVersion: metallb.io/v1beta1
kind: L2Advertisement
metadata: {name: e2e-l2, namespace: metallb-system}
`, addresses)
		var lastErr error
		for range 6 { // the validating webhook can lag the controller
			if lastErr = applyYAML(ctx, cfg, pool); lastErr == nil {
				return ctx, nil
			}
			time.Sleep(10 * time.Second)
		}
		return ctx, fmt.Errorf("configure metallb pool: %w", lastErr)
	}
}
