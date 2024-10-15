package sublet

import (
	"context"
	"fmt"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

const (
	subletPrefix       = "sublet"
	subletNamespaceKey = subletPrefix + "-namespace"
	subletNodeKey      = subletPrefix + "-node"
	subletClusterKey   = subletPrefix + "-cluster"
)

var deleteOpt = metav1.NewDeleteOptions(0)

type Sublet struct {
	node      *NodeController
	pod       *PodController
	service   *ServiceController
	endpoints *EndpointsController
}

type Config struct {
	NodePort       int
	NodeIP         string
	SubclusterName string

	NodeMapping map[string]string

	Client          kubernetes.Interface
	SourceClient    kubernetes.Interface
	SourceNamespace string
	DnsServers      []string
}

func NewSublet(conf Config) (*Sublet, error) {
	node, err := NewNodeController(NodeControllerConfig{
		NodeMapping:  conf.NodeMapping,
		NodePort:     conf.NodePort,
		NodeIP:       conf.NodeIP,
		Client:       conf.Client,
		SourceClient: conf.SourceClient,
	})
	if err != nil {
		return nil, err
	}

	pod, err := NewPodController(PodControllerConfig{
		SubclusterName:  conf.SubclusterName,
		NodeMapping:     conf.NodeMapping,
		NodePort:        conf.NodePort,
		NodeIP:          conf.NodeIP,
		Client:          conf.Client,
		SourceClient:    conf.SourceClient,
		SourceNamespace: conf.SourceNamespace,
		DnsServers:      conf.DnsServers,
	})
	if err != nil {
		return nil, err
	}

	service, err := NewServiceController(ServiceControllerConfig{
		SubclusterName:  conf.SubclusterName,
		Client:          conf.Client,
		SourceClient:    conf.SourceClient,
		SourceNamespace: conf.SourceNamespace,
	})

	endpoints, err := NewEndpointsController(EndpointsControllerConfig{
		SubclusterName:  conf.SubclusterName,
		Client:          conf.Client,
		SourceClient:    conf.SourceClient,
		SourceNamespace: conf.SourceNamespace,
	})
	if err != nil {
		return nil, err
	}

	s := Sublet{
		node:      node,
		pod:       pod,
		service:   service,
		endpoints: endpoints,
	}
	return &s, nil
}

func nameToSource(subclusterName, namespace, name string) string {
	return strings.Join([]string{subletPrefix, subclusterName, namespace, name}, ".")
}

func nameFromSource(sourceName string) (subclusterName, namespace, name string, err error) {
	slice := strings.SplitN(sourceName, ".", 4)
	if slice[0] != subletPrefix || len(slice) != 4 {
		return "", "", "", fmt.Errorf("invalid sublet name: %s", sourceName)
	}
	subclusterName = slice[1]
	namespace = slice[2]
	name = slice[3]
	return subclusterName, namespace, name, nil
}

func svcNameToSource(name string) string {
	return strings.Join([]string{subletPrefix, name}, "-")
}

func svcNameFromSource(sourceName string) (name string, err error) {
	slice := strings.SplitN(sourceName, "-", 2)
	if slice[0] != subletPrefix || len(slice) != 2 {
		return "", fmt.Errorf("invalid sublet name: %s", sourceName)
	}
	name = slice[1]
	return name, nil
}

func (s *Sublet) Start(ctx context.Context) error {
	err := s.node.Start(ctx)
	if err != nil {
		return err
	}

	err = s.pod.Start(ctx)
	if err != nil {
		return err
	}

	err = s.service.Start(ctx)
	if err != nil {
		return err
	}

	err = s.endpoints.Start(ctx)
	if err != nil {
		return err
	}
	return nil
}
