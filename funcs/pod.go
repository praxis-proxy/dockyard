package funcs

import (
	"bytes"
	"context"
	"fmt"
	"io"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/e2e-framework/klient/k8s/resources"
	"sigs.k8s.io/e2e-framework/pkg/envconf"
)

// DeploymentPod returns the first non-terminating pod backing a Deployment (resolved
// via the Deployment's own selector), or an error if none exists. Read-side helper
// for assessments that need to exec into, read logs from, or inspect the pod a
// Deployment currently runs — the Go equivalent of `kubectl -n ns get pod -l <sel>`.
func DeploymentPod(ctx context.Context, cfg *envconf.Config, namespace, deployment string) (*corev1.Pod, error) {
	r := cfg.Client().Resources()

	var dep appsv1.Deployment
	if err := r.Get(ctx, deployment, namespace, &dep); err != nil {
		return nil, fmt.Errorf("get deployment %s/%s: %w", namespace, deployment, err)
	}
	sel, err := metav1.LabelSelectorAsSelector(dep.Spec.Selector)
	if err != nil {
		return nil, fmt.Errorf("deployment %s/%s selector: %w", namespace, deployment, err)
	}

	var pods corev1.PodList
	if err := r.WithNamespace(namespace).List(ctx, &pods, resources.WithLabelSelector(sel.String())); err != nil {
		return nil, fmt.Errorf("list pods for %s/%s: %w", namespace, deployment, err)
	}
	for i := range pods.Items {
		if pods.Items[i].DeletionTimestamp == nil {
			return &pods.Items[i], nil
		}
	}
	return nil, fmt.Errorf("no running pod backing deployment %s/%s", namespace, deployment)
}

// DeploymentLogs returns the last tailLines of logs from a Deployment's first pod
// (container by name, or the first container when empty) — the Go equivalent of
// `kubectl -n ns logs deploy/<name> --tail=<n>`. A convergence probe reads these to
// observe what a controller has accepted.
func DeploymentLogs(ctx context.Context, cfg *envconf.Config, namespace, deployment, container string, tailLines int64) (string, error) {
	pod, err := DeploymentPod(ctx, cfg, namespace, deployment)
	if err != nil {
		return "", err
	}
	if container == "" && len(pod.Spec.Containers) > 0 {
		container = pod.Spec.Containers[0].Name
	}

	clientset, err := kubernetes.NewForConfig(cfg.Client().RESTConfig())
	if err != nil {
		return "", err
	}
	opts := &corev1.PodLogOptions{Container: container}
	if tailLines > 0 {
		opts.TailLines = &tailLines
	}
	stream, err := clientset.CoreV1().Pods(namespace).GetLogs(pod.Name, opts).Stream(ctx)
	if err != nil {
		return "", fmt.Errorf("stream logs for %s/%s: %w", namespace, pod.Name, err)
	}
	defer func() { _ = stream.Close() }()

	var buf bytes.Buffer
	if _, err := io.Copy(&buf, stream); err != nil {
		return "", fmt.Errorf("read logs for %s/%s: %w", namespace, pod.Name, err)
	}
	return buf.String(), nil
}

// ExecInDeployment runs command in a Deployment's first pod/container and returns its
// stdout (the Go equivalent of `kubectl -n ns exec deploy/<name> -- <command>`). It
// wraps klient's ExecInDeployment for the common string-output case; stderr is folded
// into the error on failure.
func ExecInDeployment(ctx context.Context, cfg *envconf.Config, namespace, deployment string, command ...string) (string, error) {
	var stdout, stderr bytes.Buffer
	if err := cfg.Client().Resources().ExecInDeployment(ctx, namespace, deployment, command, &stdout, &stderr); err != nil {
		return "", fmt.Errorf("exec in %s/%s %v: %w (stderr: %s)", namespace, deployment, command, err, stderr.String())
	}
	return stdout.String(), nil
}
