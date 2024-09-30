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
)

var deleteOpt = metav1.NewDeleteOptions(0)

type Sublet struct {
	node *NodeController
	pod  *PodController
}

type Config struct {
	NodeName        string
	Client          kubernetes.Interface
	SourceNodeName  string
	SourceClient    kubernetes.Interface
	SourceNamespace string
	DnsServers      []string
	DnsSearches     []string
}

func NewSublet(conf Config) (*Sublet, error) {
	node, err := NewNodeController(NodeControllerConfig{
		NodeName:       conf.NodeName,
		Client:         conf.Client,
		SourceNodeName: conf.SourceNodeName,
		SourceClient:   conf.SourceClient,
	})
	if err != nil {
		return nil, err
	}

	pod, err := NewPodController(PodControllerConfig{
		NodeName:        conf.NodeName,
		Client:          conf.Client,
		SourceNodeName:  conf.SourceNodeName,
		SourceClient:    conf.SourceClient,
		SourceNamespace: conf.SourceNamespace,
		DnsServers:      conf.DnsServers,
		DnsSearches:     conf.DnsSearches,
	})
	if err != nil {
		return nil, err
	}

	s := Sublet{
		node: node,
		pod:  pod,
	}
	return &s, nil
}

func nameToSrc(name, namespace string) string {
	return strings.Join([]string{subletPrefix, namespace, name}, ".")
}

func nameToDst(name string) (string, string, error) {
	slice := strings.SplitN(name, ".", 3)
	if slice[0] != subletPrefix || len(slice) != 3 {
		return "", "", fmt.Errorf("invalid sublet name: %s", name)
	}
	return slice[2], slice[1], nil
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
	return nil
}
