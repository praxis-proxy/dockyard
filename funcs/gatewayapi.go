package funcs

import (
	"context"
	"fmt"
	"time"

	"sigs.k8s.io/e2e-framework/pkg/env"
	"sigs.k8s.io/e2e-framework/pkg/envconf"
)

const defaultGatewayAPIVersion = "v1.2.1"

// GatewayAPIOption configures InstallGatewayAPI.
type GatewayAPIOption func(*gatewayAPIConfig)

type gatewayAPIConfig struct{ version string }

// GatewayAPIVersion pins the Gateway API release. Empty is ignored.
func GatewayAPIVersion(v string) GatewayAPIOption {
	return func(c *gatewayAPIConfig) {
		if v != "" {
			c.version = v
		}
	}
}

// InstallGatewayAPI installs the upstream Gateway API CRDs (istio needs them to
// serve Gateway/HTTPRoute, which the suite's manifests are).
func InstallGatewayAPI(opts ...GatewayAPIOption) env.Func {
	o := gatewayAPIConfig{version: defaultGatewayAPIVersion}
	for _, opt := range opts {
		opt(&o)
	}
	return func(ctx context.Context, cfg *envconf.Config) (context.Context, error) {
		url := fmt.Sprintf("https://github.com/kubernetes-sigs/gateway-api/releases/download/%s/standard-install.yaml", o.version)
		if err := applyURL(ctx, cfg, url); err != nil {
			return ctx, err
		}
		r, err := newResources(cfg)
		if err != nil {
			return ctx, err
		}
		return ctx, waitCRDsEstablished(ctx, r, 60*time.Second,
			"gateways.gateway.networking.k8s.io",
			"httproutes.gateway.networking.k8s.io",
			"gatewayclasses.gateway.networking.k8s.io",
		)
	}
}
