package feature

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strconv"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/portforward"
	"k8s.io/client-go/transport/spdy"
	"sigs.k8s.io/e2e-framework/klient/k8s/resources"
	"sigs.k8s.io/e2e-framework/pkg/envconf"
)

// ForwardedPort is an open port-forward to a pod. Close it when done.
type ForwardedPort struct {
	// Local is the local port forwarded to the target.
	Local int
	stop  chan struct{}
}

// Addr returns the local dial address, e.g. "127.0.0.1:34567".
func (f *ForwardedPort) Addr() string { return fmt.Sprintf("127.0.0.1:%d", f.Local) }

// URL returns a base URL for scheme (http/https) over the forward.
func (f *ForwardedPort) URL(scheme string) string { return scheme + "://" + f.Addr() }

// Close tears down the port-forward.
func (f *ForwardedPort) Close() {
	if f.stop != nil {
		close(f.stop)
		f.stop = nil
	}
}

// PortForwardService opens a port-forward to a ready pod backing a Service (the Go
// equivalent of `kubectl port-forward svc/<service> :<remotePort>`), picking a random
// local port. Clusters without a LoadBalancer (e.g. kind without metallb) reach
// in-cluster Services this way. The caller must Close the returned ForwardedPort.
func PortForwardService(ctx context.Context, cfg *envconf.Config, namespace, service string, remotePort int) (*ForwardedPort, error) {
	r := cfg.Client().Resources()

	var svc corev1.Service
	if err := r.Get(ctx, service, namespace, &svc); err != nil {
		return nil, fmt.Errorf("get service %s/%s: %w", namespace, service, err)
	}
	if len(svc.Spec.Selector) == 0 {
		return nil, fmt.Errorf("service %s/%s has no selector to resolve a pod", namespace, service)
	}

	var pods corev1.PodList
	sel := labels.SelectorFromSet(svc.Spec.Selector).String()
	if err := r.WithNamespace(namespace).List(ctx, &pods, resources.WithLabelSelector(sel)); err != nil {
		return nil, fmt.Errorf("list pods for %s/%s: %w", namespace, service, err)
	}
	var podName string
	for i := range pods.Items {
		if podReady(&pods.Items[i]) {
			podName = pods.Items[i].Name
			break
		}
	}
	if podName == "" {
		return nil, fmt.Errorf("no ready pod backing service %s/%s", namespace, service)
	}

	restCfg := cfg.Client().RESTConfig()
	clientset, err := kubernetes.NewForConfig(restCfg)
	if err != nil {
		return nil, err
	}
	reqURL := clientset.CoreV1().RESTClient().Post().
		Resource("pods").Namespace(namespace).Name(podName).SubResource("portforward").URL()

	roundTripper, upgrader, err := spdy.RoundTripperFor(restCfg)
	if err != nil {
		return nil, err
	}
	dialer := spdy.NewDialer(upgrader, &http.Client{Transport: roundTripper}, http.MethodPost, reqURL)

	stopCh := make(chan struct{})
	readyCh := make(chan struct{})
	fw, err := portforward.NewOnAddresses(dialer, []string{"127.0.0.1"},
		[]string{"0:" + strconv.Itoa(remotePort)}, stopCh, readyCh, io.Discard, io.Discard)
	if err != nil {
		return nil, err
	}
	errCh := make(chan error, 1)
	go func() { errCh <- fw.ForwardPorts() }()

	select {
	case <-readyCh:
	case err := <-errCh:
		return nil, fmt.Errorf("port-forward %s/%s: %w", namespace, service, err)
	case <-ctx.Done():
		close(stopCh)
		return nil, ctx.Err()
	}

	ports, err := fw.GetPorts()
	if err != nil {
		close(stopCh)
		return nil, err
	}
	return &ForwardedPort{Local: int(ports[0].Local), stop: stopCh}, nil
}

func podReady(p *corev1.Pod) bool {
	for _, c := range p.Status.Conditions {
		if c.Type == corev1.PodReady && c.Status == corev1.ConditionTrue {
			return true
		}
	}
	return false
}
