package clientset

import (
	"os"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

func RestConfigFromKubeConfigPath(kubeConfigPath string) (*rest.Config, error) {
	if kubeConfigPath == "" {
		return rest.InClusterConfig()
	}

	_, err := os.Stat(kubeConfigPath)
	if err != nil {
		return nil, err
	}
	return clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		&clientcmd.ClientConfigLoadingRules{ExplicitPath: kubeConfigPath},
		&clientcmd.ConfigOverrides{},
	).ClientConfig()
}

func ClientsetFromKubeConfigPath(kubeConfigPath string) (*kubernetes.Clientset, *rest.Config, error) {
	config, err := RestConfigFromKubeConfigPath(kubeConfigPath)
	if err != nil {
		return nil, nil, err
	}
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, nil, err
	}
	return clientset, config, nil
}
