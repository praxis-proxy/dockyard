package install

import (
	"context"

	"sigs.k8s.io/e2e-framework/pkg/env"
	"sigs.k8s.io/e2e-framework/pkg/envconf"
)

// DefaultRecorderRegistry is where Dockyard CI publishes the recorder sim image
// (see sim/recorder). Consumers reference the published image via WithRecorder
// rather than building the recorder from source per run.
const DefaultRecorderRegistry = "ghcr.io/praxis-proxy/dockyard-recorder"

// DefaultRecorderTag is the recorder image tag WithRecorder references by default.
const DefaultRecorderTag = "latest"

// BuildImage builds a container image with `docker build -t tag contextDir`
// (Dockerfile at contextDir/Dockerfile). A sanctioned shell-out: building images
// has no pure-Go path. contextDir is relative to the test package working dir.
// Prefer referencing a published image; keep BuildImage for a consumer's own sims.
func BuildImage(contextDir, tag string) env.Func {
	return func(ctx context.Context, cfg *envconf.Config) (context.Context, error) {
		return ctx, sh(ctx, cfg, "docker", "build", "-t", tag, contextDir)
	}
}

// BuildImageFrom builds a container image with an explicit Dockerfile/Containerfile
// path: `docker build -t tag -f dockerfile contextDir`. For products whose image
// definition is not at contextDir/Dockerfile (e.g. ext-proc's Containerfile). A
// sanctioned shell-out — building images has no pure-Go path.
func BuildImageFrom(contextDir, dockerfile, tag string) env.Func {
	return func(ctx context.Context, cfg *envconf.Config) (context.Context, error) {
		return ctx, sh(ctx, cfg, "docker", "build", "-t", tag, "-f", dockerfile, contextDir)
	}
}

// PullImage pulls a published image with `docker pull` (so a subsequent kind load
// has it locally). A no-op the kind load tolerates if the image is already present.
func PullImage(image string) env.Func {
	return func(ctx context.Context, cfg *envconf.Config) (context.Context, error) {
		return ctx, sh(ctx, cfg, "docker", "pull", image)
	}
}

// TagImage retags a local image (`docker tag src dst`), e.g. to give a pulled
// digest reference a plain tag kind can load.
func TagImage(src, dst string) env.Func {
	return func(ctx context.Context, cfg *envconf.Config) (context.Context, error) {
		return ctx, sh(ctx, cfg, "docker", "tag", src, dst)
	}
}

// LoadImage loads an image into the kind cluster (no-op when we don't own the
// cluster, i.e. isKind is false).
func LoadImage(clusterName, image string, isKind bool) env.Func {
	return func(ctx context.Context, cfg *envconf.Config) (context.Context, error) {
		if !isKind {
			return ctx, nil
		}
		return ctx, sh(ctx, cfg, "kind", "load", "docker-image", image, "--name", clusterName)
	}
}
