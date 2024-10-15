package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/spf13/pflag"
	"github.com/wzshiming/subcluster/pkg/clientset"
	"github.com/wzshiming/subcluster/pkg/sublet"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
)

var (
	name string

	nodeMapping map[string]string

	kubeConfigPath string
	dnsServers     []string
	dnsSearches    []string

	sourceKubeConfigPath string
	sourceNamespace      string

	nodeIP   string
	nodePort int
)

func init() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{AddSource: true})))

	pflag.StringVar(&name, "name", "sublet", "name of the subcluster")
	pflag.StringVar(&kubeConfigPath, "kubeconfig", "", "kubeconfig path")
	pflag.StringVar(&sourceKubeConfigPath, "source-kubeconfig", "", "source kubeconfig path")
	pflag.StringVar(&sourceNamespace, "source-namespace", "", "source namespace")
	pflag.StringSliceVar(&dnsServers, "dns-servers", dnsServers, "dns servers")
	pflag.StringSliceVar(&dnsSearches, "dns-searches", dnsSearches, "dns searches")

	pflag.StringToStringVar(&nodeMapping, "node-mapping", map[string]string{}, "node mapping")

	pflag.StringVar(&nodeIP, "node-ip", "", "node ip")
	pflag.IntVar(&nodePort, "node-port", 0, "node port")
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

	sourceClient, sourceRestConfig, err := clientset.ClientsetFromKubeConfigPath(sourceKubeConfigPath)
	if err != nil {
		slog.Error("create source clientset", "err", err)
		os.Exit(1)
	}

	err = waitForReady(ctx, sourceClient)
	if err != nil {
		slog.Error("wait for ready source", "err", err)
		os.Exit(1)
	}

	client, _, err := clientset.ClientsetFromKubeConfigPath(kubeConfigPath)
	if err != nil {
		slog.Error("create clientset", "err", err)
		os.Exit(1)
	}

	err = waitForReady(ctx, client)
	if err != nil {
		slog.Error("wait for ready source", "err", err)
		os.Exit(1)
	}

	conf := sublet.Config{
		SubclusterName:  name,
		Client:          client,
		NodeMapping:     nodeMapping,
		SourceClient:    sourceClient,
		SourceNamespace: sourceNamespace,
		DnsServers:      dnsServers,
		DnsSearches:     dnsSearches,
		NodeIP:          nodeIP,
		NodePort:        nodePort,
	}

	cm, err := sublet.NewSublet(conf)
	if err != nil {
		slog.Error("create sublet", "err", err)
		os.Exit(1)
	}
	err = cm.Start(ctx)
	if err != nil {
		slog.Error("start sublet", "err", err)
		os.Exit(1)
	}

	server := sublet.NewServer(sublet.ServerConfig{
		SubclusterName:   name,
		SourceClient:     sourceClient,
		SourceRestConfig: sourceRestConfig,
		SourceNamespace:  sourceNamespace,
	})

	server.InstallDebuggingHandlers()

	err = server.Run(ctx, ":"+strconv.Itoa(nodePort), "", "")
	if err != nil {
		slog.Error("start server", "err", err)
		os.Exit(1)
	}
}

func waitForReady(ctx context.Context, clientset kubernetes.Interface) error {
	err := wait.PollUntilContextTimeout(ctx, time.Second, 30*time.Second, false,
		func(ctx context.Context) (bool, error) {
			_, err := clientset.CoreV1().Nodes().List(ctx,
				metav1.ListOptions{
					Limit: 1,
				})
			if err != nil {
				return false, nil
			}
			return true, nil
		},
	)
	if err != nil {
		return err
	}
	return nil
}
