package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/pflag"
	"github.com/wzshiming/subcluster/pkg/sublet"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

var (
	nodeName       string
	kubeConfigPath string

	sourceNodeName       string
	sourceKubeConfigPath string
)

func init() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{AddSource: true})))

	pflag.StringVar(&nodeName, "node-name", "", "node name")
	pflag.StringVar(&kubeConfigPath, "kubeconfig", "", "kubeconfig path")
	pflag.StringVar(&sourceNodeName, "source-node-name", "", "source node name")
	pflag.StringVar(&sourceKubeConfigPath, "source-kubeconfig", "", "source kubeconfig path")
	pflag.Parse()
}

func main() {
	ctx, cancel := context.WithCancel(context.Background())
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sig
		cancel()
	}()

	sourceClient, err := clientsetFromKubeConfigPath(sourceKubeConfigPath)
	if err != nil {
		slog.Error("create source clientset", "err", err)
		os.Exit(1)
	}

	client, err := clientsetFromKubeConfigPath(kubeConfigPath)
	if err != nil {
		slog.Error("create clientset", "err", err)
		os.Exit(1)
	}

	cm, err := sublet.NewSublet(
		nodeName,
		client,
		sourceNodeName,
		sourceClient,
	)
	if err != nil {
		slog.Error("create sublet", "err", err)
		os.Exit(1)
	}
	err = cm.Start(ctx)
	if err != nil {
		slog.Error("start sublet", "err", err)
		os.Exit(1)
	}

	<-ctx.Done()
}

func restConfigFromKubeConfigPath(kubeConfigPath string) (*rest.Config, error) {
	_, err := os.Stat(kubeConfigPath)
	if os.IsNotExist(err) {
		return rest.InClusterConfig()
	}
	if err != nil {
		return nil, err
	}
	return clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		&clientcmd.ClientConfigLoadingRules{ExplicitPath: kubeConfigPath},
		&clientcmd.ConfigOverrides{},
	).ClientConfig()
}

func clientsetFromKubeConfigPath(kubeConfigPath string) (*kubernetes.Clientset, error) {
	config, err := restConfigFromKubeConfigPath(kubeConfigPath)
	if err != nil {
		return nil, err
	}
	return kubernetes.NewForConfig(config)
}
