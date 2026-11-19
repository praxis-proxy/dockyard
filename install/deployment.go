package install

import (
	"context"
	"fmt"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/e2e-framework/pkg/env"
	"sigs.k8s.io/e2e-framework/pkg/envconf"
)

// Generic Deployment mutation helpers (get → modify → update). They target a
// container by name, falling back to the first container when name is empty.

// RestartDeployment forces a rollout by stamping a restart annotation on the pod
// template (the Go equivalent of `kubectl rollout restart deployment/name`).
func RestartDeployment(namespace, name string) env.Func {
	return func(ctx context.Context, cfg *envconf.Config) (context.Context, error) {
		r, err := newResources(cfg)
		if err != nil {
			return ctx, err
		}
		return ctx, restartDeployment(ctx, r, namespace, name)
	}
}

// SetDeploymentEnv adds or overrides environment variables on a container.
func SetDeploymentEnv(namespace, name, container string, vars map[string]string) env.Func {
	return mutateDeployment(namespace, name, func(c *corev1.Container) {
		for k, v := range vars {
			setEnvVar(c, k, v)
		}
	}, container)
}

// SetDeploymentArgs replaces a container's args wholesale.
func SetDeploymentArgs(namespace, name, container string, args []string) env.Func {
	return mutateDeployment(namespace, name, func(c *corev1.Container) {
		c.Args = args
	}, container)
}

// SetDeploymentCommand replaces a container's command (entrypoint).
func SetDeploymentCommand(namespace, name, container string, command []string) env.Func {
	return mutateDeployment(namespace, name, func(c *corev1.Container) {
		c.Command = command
	}, container)
}

// SetDeploymentImage sets a container's image.
func SetDeploymentImage(namespace, name, container, image string) env.Func {
	return mutateDeployment(namespace, name, func(c *corev1.Container) {
		c.Image = image
	}, container)
}

// SetDeploymentImagePullPolicy sets a container's imagePullPolicy.
func SetDeploymentImagePullPolicy(namespace, name, container string, policy corev1.PullPolicy) env.Func {
	return mutateDeployment(namespace, name, func(c *corev1.Container) {
		c.ImagePullPolicy = policy
	}, container)
}

// mutateDeployment gets a Deployment, applies fn to the selected container, and
// updates it. The target container is matched by name, or the first container when
// name is empty.
func mutateDeployment(namespace, name string, fn func(*corev1.Container), container string) env.Func {
	return func(ctx context.Context, cfg *envconf.Config) (context.Context, error) {
		r, err := newResources(cfg)
		if err != nil {
			return ctx, err
		}
		var dep appsv1.Deployment
		if err := r.Get(ctx, name, namespace, &dep); err != nil {
			return ctx, fmt.Errorf("get deployment %s/%s: %w", namespace, name, err)
		}
		idx, err := containerIndex(dep.Spec.Template.Spec.Containers, container)
		if err != nil {
			return ctx, fmt.Errorf("deployment %s/%s: %w", namespace, name, err)
		}
		fn(&dep.Spec.Template.Spec.Containers[idx])
		if err := r.Update(ctx, &dep); err != nil {
			return ctx, fmt.Errorf("update deployment %s/%s: %w", namespace, name, err)
		}
		return ctx, nil
	}
}

func containerIndex(containers []corev1.Container, name string) (int, error) {
	if len(containers) == 0 {
		return 0, fmt.Errorf("no containers")
	}
	if name == "" {
		return 0, nil
	}
	for i := range containers {
		if containers[i].Name == name {
			return i, nil
		}
	}
	return 0, fmt.Errorf("container %q not found", name)
}

func setEnvVar(c *corev1.Container, key, value string) {
	for i := range c.Env {
		if c.Env[i].Name == key {
			c.Env[i].Value = value
			c.Env[i].ValueFrom = nil
			return
		}
	}
	c.Env = append(c.Env, corev1.EnvVar{Name: key, Value: value})
}

// WaitForDeploymentRolledOut blocks until a Deployment is Available (alias that
// reads naturally after a restart). See WaitForDeploymentAvailable.
func WaitForDeploymentRolledOut(namespace, name string, timeout time.Duration) env.Func {
	return WaitForDeploymentAvailable(namespace, name, timeout)
}
