package install

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// DefaultRegistryImage is the registry server image LocalRegistry runs.
const DefaultRegistryImage = "registry:2.8.3"

// LocalRegistry is a kind-reachable local image registry: a registry:2 container on
// a dedicated docker network, with each kind node's containerd configured to pull
// from it. It is the standard "kind + local registry" pattern, needed when a
// controller reconciles workloads with imagePullPolicy: Always (the image must come
// from a registry the node can resolve). Orchestration is docker/kind shell-outs —
// there is no in-process path for the container runtime.
type LocalRegistry struct {
	// Name is the registry container name and its network alias.
	Name string
	// Network is the docker network the registry and kind nodes share.
	Network string
	// Image is the registry server image (default DefaultRegistryImage).
	Image string
	// hostPort is the published 127.0.0.1 port, resolved by Start.
	hostPort string
}

// NewLocalRegistry builds a LocalRegistry spec (call Start to create it).
func NewLocalRegistry(name, network string) *LocalRegistry {
	return &LocalRegistry{Name: name, Network: network, Image: DefaultRegistryImage}
}

// HostPort returns the published registry port (valid after Start).
func (lr *LocalRegistry) HostPort() string { return lr.hostPort }

// Ref returns a pushable image reference in this registry, e.g.
// "127.0.0.1:<port>/maas-api:dev" (valid after Start).
func (lr *LocalRegistry) Ref(repoTag string) string {
	return fmt.Sprintf("127.0.0.1:%s/%s", lr.hostPort, repoTag)
}

// Start creates the network (if absent) and the registry container (if absent, else
// starts it), then resolves the published host port. Idempotent.
func (lr *LocalRegistry) Start(ctx context.Context) error {
	if lr.Image == "" {
		lr.Image = DefaultRegistryImage
	}
	if err := docker(ctx, "network", "inspect", lr.Network); err != nil {
		if err := docker(ctx, "network", "create", "--driver", "bridge", lr.Network); err != nil {
			return fmt.Errorf("create registry network %s: %w", lr.Network, err)
		}
	}
	if err := docker(ctx, "image", "inspect", lr.Image); err != nil {
		if err := docker(ctx, "pull", lr.Image); err != nil {
			return fmt.Errorf("pull registry image %s: %w", lr.Image, err)
		}
	}
	if err := docker(ctx, "container", "inspect", lr.Name); err == nil {
		if err := docker(ctx, "start", lr.Name); err != nil {
			return fmt.Errorf("start registry %s: %w", lr.Name, err)
		}
	} else if err := docker(ctx, "run", "--detach", "--restart", "unless-stopped",
		"--name", lr.Name, "--network", lr.Network, "--network-alias", lr.Name,
		"--publish", "127.0.0.1::5000", lr.Image); err != nil {
		return fmt.Errorf("run registry %s: %w", lr.Name, err)
	}
	port, err := dockerOut(ctx, "container", "inspect", "--format",
		`{{(index (index .NetworkSettings.Ports "5000/tcp") 0).HostPort}}`, lr.Name)
	if err != nil {
		return fmt.Errorf("resolve registry host port: %w", err)
	}
	lr.hostPort = strings.TrimSpace(port)
	if lr.hostPort == "" {
		return fmt.Errorf("registry %s has no published host port", lr.Name)
	}
	return nil
}

// Push tags a local image to a registry ref and pushes it.
func (lr *LocalRegistry) Push(ctx context.Context, localImage, ref string) error {
	if err := docker(ctx, "tag", localImage, ref); err != nil {
		return fmt.Errorf("tag %s -> %s: %w", localImage, ref, err)
	}
	if err := docker(ctx, "push", ref); err != nil {
		return fmt.Errorf("push %s: %w", ref, err)
	}
	return nil
}

// WireCluster connects each kind node of clusterName to the registry network and
// configures its containerd to pull from the registry (certs.d hosts.toml +
// config_path), restarting containerd when the config changes.
func (lr *LocalRegistry) WireCluster(ctx context.Context, clusterName string) error {
	nodesOut, err := kindOut(ctx, "get", "nodes", "--name", clusterName)
	if err != nil {
		return fmt.Errorf("list kind nodes for %s: %w", clusterName, err)
	}
	hostsDir := fmt.Sprintf("/etc/containerd/certs.d/127.0.0.1:%s", lr.hostPort)
	hostsTOML := fmt.Sprintf("[host.\"http://%s:5000\"]\n  capabilities = [\"pull\", \"resolve\"]\n", lr.Name)

	for _, node := range strings.Fields(nodesOut) {
		if err := lr.connectNode(ctx, node); err != nil {
			return err
		}
		if err := dockerExec(ctx, node, "mkdir", "-p", hostsDir); err != nil {
			return fmt.Errorf("node %s: mkdir certs.d: %w", node, err)
		}
		if err := dockerExecStdin(ctx, node, hostsTOML, "sh", "-c", "cat > '"+hostsDir+"/hosts.toml'"); err != nil {
			return fmt.Errorf("node %s: write hosts.toml: %w", node, err)
		}
		if err := lr.ensureContainerdConfigPath(ctx, node); err != nil {
			return err
		}
	}
	return nil
}

// connectNode attaches a kind node container to the registry network if not already.
func (lr *LocalRegistry) connectNode(ctx context.Context, node string) error {
	nets, err := dockerOut(ctx, "inspect", "--format",
		`{{range $name, $value := .NetworkSettings.Networks}}{{$name}} {{end}}`, node)
	if err != nil {
		return fmt.Errorf("inspect node %s networks: %w", node, err)
	}
	for _, n := range strings.Fields(nets) {
		if n == lr.Network {
			return nil
		}
	}
	if err := docker(ctx, "network", "connect", lr.Network, node); err != nil {
		return fmt.Errorf("connect node %s to %s: %w", node, lr.Network, err)
	}
	return nil
}

// ensureContainerdConfigPath makes containerd read /etc/containerd/certs.d, adding
// the config_path to the CRI registry section and restarting containerd if changed.
func (lr *LocalRegistry) ensureContainerdConfigPath(ctx context.Context, node string) error {
	cfg, err := dockerOut(ctx, "exec", node, "cat", "/etc/containerd/config.toml")
	if err != nil {
		return fmt.Errorf("node %s: read containerd config: %w", node, err)
	}
	const setting = `  config_path = "/etc/containerd/certs.d"`
	if strings.Contains(cfg, setting) {
		return nil
	}
	const section = `[plugins."io.containerd.grpc.v1.cri".registry]`
	var updated string
	if strings.Contains(cfg, section) {
		updated = strings.Replace(cfg, section, section+"\n"+setting, 1)
	} else {
		updated = strings.TrimRight(cfg, "\n") + "\n\n" + section + "\n" + setting + "\n"
	}
	if err := dockerExecStdin(ctx, node, updated, "sh", "-c", "cat > /etc/containerd/config.toml"); err != nil {
		return fmt.Errorf("node %s: write containerd config: %w", node, err)
	}
	if err := dockerExec(ctx, node, "systemctl", "restart", "containerd"); err != nil {
		return fmt.Errorf("node %s: restart containerd: %w", node, err)
	}
	return nil
}

// --- docker/kind plumbing (no kubeconfig needed) -----------------------------

func docker(ctx context.Context, args ...string) error {
	return run(ctx, "docker", args...)
}

func dockerExec(ctx context.Context, node string, args ...string) error {
	return run(ctx, "docker", append([]string{"exec", node}, args...)...)
}

func dockerExecStdin(ctx context.Context, node, stdin string, args ...string) error {
	cmd := exec.CommandContext(ctx, "docker", append([]string{"exec", "-i", node}, args...)...)
	cmd.Stdin = strings.NewReader(stdin)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("docker exec -i %s: %w\n%s", node, err, out)
	}
	return nil
}

func dockerOut(ctx context.Context, args ...string) (string, error) {
	var stdout bytes.Buffer
	cmd := exec.CommandContext(ctx, "docker", args...)
	cmd.Stdout = &stdout
	if err := cmd.Run(); err != nil {
		return "", err
	}
	return stdout.String(), nil
}

func kindOut(ctx context.Context, args ...string) (string, error) {
	var stdout bytes.Buffer
	cmd := exec.CommandContext(ctx, "kind", args...)
	cmd.Stdout = &stdout
	if err := cmd.Run(); err != nil {
		return "", err
	}
	return stdout.String(), nil
}

func run(ctx context.Context, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%s %s: %w\n%s", name, strings.Join(args, " "), err, out)
	}
	return nil
}
