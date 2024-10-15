package sublet

import (
	"context"
	"fmt"
	"log/slog"
	"reflect"

	"github.com/wzshiming/subcluster/pkg/informer"
	coordinationv1 "k8s.io/api/coordination/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/client-go/kubernetes"
	clientset "k8s.io/client-go/kubernetes"
	"k8s.io/component-helpers/apimachinery/lease"
	"k8s.io/utils/clock"
)

type NodeController struct {
	clock clock.Clock

	nodeMapping map[string]string
	client      kubernetes.Interface

	nodePort int
	nodeIP   string

	sourceClient kubernetes.Interface
}

type NodeControllerConfig struct {
	NodePort     int
	NodeIP       string
	NodeMapping  map[string]string
	Client       kubernetes.Interface
	SourceClient kubernetes.Interface
}

func NewNodeController(conf NodeControllerConfig) (*NodeController, error) {
	s := NodeController{
		clock:        clock.RealClock{},
		nodeMapping:  conf.NodeMapping,
		client:       conf.Client,
		sourceClient: conf.SourceClient,
		nodePort:     conf.NodePort,
		nodeIP:       conf.NodeIP,
	}
	return &s, nil
}

func (s *NodeController) Start(ctx context.Context) error {
	for nodeName, sourceNodeName := range s.nodeMapping {
		err := s.start(ctx, nodeName, sourceNodeName)
		if err != nil {
			return err
		}
	}
	return nil
}

func (s *NodeController) start(ctx context.Context, nodeName string, sourceNodeName string) error {
	leaseController := lease.NewController(
		s.clock,
		s.client,
		nodeName,
		400,
		nil,
		100,
		nodeName,
		corev1.NamespaceNodeLease,
		setNodeOwnerFunc(s.client, nodeName))

	go leaseController.Run(ctx)

	srcCacheNodeInformer := informer.NewInformer[*corev1.Node, *corev1.NodeList](s.sourceClient.CoreV1().Nodes())
	srcNodeEvent := make(chan informer.Event[*corev1.Node])
	_, err := srcCacheNodeInformer.WatchWithCache(ctx, informer.Option{
		FieldSelector: fields.OneTermEqualSelector("metadata.name", sourceNodeName).String(),
	}, srcNodeEvent)
	if err != nil {
		return err
	}

	go func() {
		for srcNode := range srcNodeEvent {
			switch srcNode.Type {
			case informer.Added, informer.Modified, informer.Sync:
				err := s.SyncNodeFromSource(ctx, srcNode.Object, nodeName)
				if err != nil {
					slog.Error("sync node", "err", err)
				}
			default:
				slog.Error("sync node", "err", fmt.Errorf("invalid event type: %s", srcNode.Type))
			}
		}
	}()
	return nil
}

func (s *NodeController) SyncNodeFromSource(ctx context.Context, node *corev1.Node, nodeName string) error {
	node = node.DeepCopy()

	if s.nodeIP != "" {
		node.Status.Addresses = []corev1.NodeAddress{
			{Type: corev1.NodeInternalIP, Address: s.nodeIP},
		}
	}

	if s.nodePort != 0 {
		node.Status.DaemonEndpoints.KubeletEndpoint.Port = int32(s.nodePort)
	}

	nodeClient := s.client.CoreV1().Nodes()
	dstNode, err := nodeClient.Get(ctx, nodeName, metav1.GetOptions{})
	if err != nil {
		if !apierrors.IsNotFound(err) {
			return err
		}
		dstNode = &corev1.Node{
			ObjectMeta: metav1.ObjectMeta{
				Name:        nodeName,
				Labels:      node.Labels,
				Annotations: node.Annotations,
			},
			Spec:   node.Spec,
			Status: node.Status,
		}
		_, err = nodeClient.Create(ctx, dstNode, metav1.CreateOptions{})
		if err != nil {
			return err
		}
		return nil
	}

	if reflect.DeepEqual(dstNode.Spec, node.Spec) ||
		reflect.DeepEqual(dstNode.Labels, node.Labels) ||
		reflect.DeepEqual(dstNode.Annotations, node.Annotations) {
		dstNode.Spec = node.Spec
		dstNode.Labels = node.Labels
		dstNode.Annotations = node.Annotations
		dstNode, err = s.client.CoreV1().Nodes().Update(ctx, dstNode, metav1.UpdateOptions{})
		if err != nil {
			return err
		}
	}
	if reflect.DeepEqual(dstNode.Status, node.Status) {
		dstNode.Status = node.Status
		dstNode, err = s.client.CoreV1().Nodes().UpdateStatus(ctx, dstNode, metav1.UpdateOptions{})
		if err != nil {
			return err
		}
	}

	return nil
}

func setNodeOwnerFunc(c clientset.Interface, nodeName string) func(lease *coordinationv1.Lease) error {
	return func(lease *coordinationv1.Lease) error {
		// Setting owner reference needs node's UID. Note that it is different from
		// kubelet.nodeRef.UID. When lease is initially created, it is possible that
		// the connection between master and node is not ready yet. So try to set
		// owner reference every time when renewing the lease, until successful.
		if len(lease.OwnerReferences) == 0 {
			if node, err := c.CoreV1().Nodes().Get(context.TODO(), nodeName, metav1.GetOptions{}); err == nil {
				lease.OwnerReferences = []metav1.OwnerReference{
					{
						APIVersion: corev1.SchemeGroupVersion.WithKind("Node").Version,
						Kind:       corev1.SchemeGroupVersion.WithKind("Node").Kind,
						Name:       nodeName,
						UID:        node.UID,
					},
				}
			} else {
				return err
			}
		}
		return nil
	}
}
