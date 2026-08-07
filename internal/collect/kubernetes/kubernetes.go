// Package kubernetes implements the Kubernetes collector: it reads the cluster
// through the caller's kubeconfig identity (never escalating) and normalizes
// state, events, container statuses, node conditions, and (optionally) log
// tails into Observations.
package kubernetes

import (
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

// Client builds a kubernetes.Interface and its rest.Config from the standard
// kubeconfig loading rules (KUBECONFIG env / ~/.kube/config), with an
// in-cluster fallback. kubeconfig and context are optional overrides ("" =
// defaults). The rest.Config lets callers build dynamic clients for CRDs.
func Client(kubeconfig, context string) (kubernetes.Interface, *rest.Config, error) {
	loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
	if kubeconfig != "" {
		loadingRules.ExplicitPath = kubeconfig
	}
	overrides := &clientcmd.ConfigOverrides{}
	if context != "" {
		overrides.CurrentContext = context
	}
	cc := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loadingRules, overrides)
	cfg, err := cc.ClientConfig()
	if err != nil {
		// In-cluster fallback (running inside a pod).
		if cfg, icErr := rest.InClusterConfig(); icErr == nil {
			c, cErr := kubernetes.NewForConfig(cfg)
			return c, cfg, cErr
		}
		return nil, nil, err
	}
	c, err := kubernetes.NewForConfig(cfg)
	return c, cfg, err
}
