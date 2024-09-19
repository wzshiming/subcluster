package sublet

import (
	"context"
	"fmt"
	"log/slog"
	"reflect"
	"strings"

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
	"k8s.io/apimachinery/pkg/labels"
)

const (
	SubletSystem  = "sublet-system"
	separate      = ".sublet."
	subletNsKey   = "sublet-ns"
	subletNodeKey = "sublet-node"
)

var deleteOpt = metav1.NewDeleteOptions(0)

type Sublet struct {
	clock clock.Clock

	leaseController lease.Controller

	nodeName          string
	client            kubernetes.Interface
	cachePodsInformer *informer.Informer[*corev1.Pod, *corev1.PodList]
	podsGetter        informer.Getter[*corev1.Pod]

	srcNodeName          string
	srcClient            kubernetes.Interface
	srcCacheNodeInformer *informer.Informer[*corev1.Node, *corev1.NodeList]
	srcNodeGetter        informer.Getter[*corev1.Node]
	srcCachePodsInformer *informer.Informer[*corev1.Pod, *corev1.PodList]
	srcPodsGetter        informer.Getter[*corev1.Pod]
}

func NewSublet(nodeName string, client kubernetes.Interface, srcNodeName string, srcClient kubernetes.Interface) (*Sublet, error) {
	s := Sublet{
		clock:       clock.RealClock{},
		nodeName:    nodeName,
		client:      client,
		srcNodeName: srcNodeName,
		srcClient:   srcClient,
	}
	return &s, nil
}

func nameToSrc(name, namespace string) string {
	return namespace + separate + name
}

func nameToDst(name string) (string, string, error) {
	slice := strings.SplitN(name, separate, 3)
	if len(slice) != 2 {
		return "", "", fmt.Errorf("invalid sublet name: %s", name)
	}

	return slice[1], slice[0], nil
}

func (s *Sublet) Start(ctx context.Context) error {
	s.leaseController = lease.NewController(
		s.clock,
		s.client,
		s.nodeName,
		40,
		nil,
		10,
		s.nodeName,
		corev1.NamespaceNodeLease,
		setNodeOwnerFunc(s.client, s.nodeName))

	go s.leaseController.Run(ctx)

	cachePodsInformer := informer.NewInformer[*corev1.Pod, *corev1.PodList](s.client.CoreV1().Pods(""))
	podsEvent := make(chan informer.Event[*corev1.Pod])
	podsGetter, err := cachePodsInformer.WatchWithCache(ctx, informer.Option{
		FieldSelector: fields.OneTermEqualSelector("spec.nodeName", s.nodeName).String(),
	}, podsEvent)
	if err != nil {
		return err
	}

	s.cachePodsInformer = cachePodsInformer
	s.podsGetter = podsGetter

	srcCachePodsInformer := informer.NewInformer[*corev1.Pod, *corev1.PodList](s.srcClient.CoreV1().Pods(SubletSystem))
	srcPodsEvent := make(chan informer.Event[*corev1.Pod])
	srcPodsGetter, err := srcCachePodsInformer.WatchWithCache(ctx, informer.Option{
		LabelSelector: labels.SelectorFromSet(map[string]string{
			subletNodeKey: s.nodeName,
		}).String(),
		FieldSelector: fields.OneTermEqualSelector("spec.nodeName", s.srcNodeName).String(),
	}, srcPodsEvent)
	if err != nil {
		return err
	}
	s.srcCachePodsInformer = srcCachePodsInformer
	s.srcPodsGetter = srcPodsGetter

	srcCacheNodeInformer := informer.NewInformer[*corev1.Node, *corev1.NodeList](s.srcClient.CoreV1().Nodes())
	srcNodeEvent := make(chan informer.Event[*corev1.Node])
	srcNodeGetter, err := srcCacheNodeInformer.WatchWithCache(ctx, informer.Option{
		FieldSelector: fields.OneTermEqualSelector("metadata.name", s.srcNodeName).String(),
	}, srcNodeEvent)
	if err != nil {
		return err
	}

	s.srcCacheNodeInformer = srcCacheNodeInformer
	s.srcNodeGetter = srcNodeGetter

	go func() {
		for pod := range podsEvent {
			slog.Info("Pod Event", "type", pod.Type, "name", pod.Object.Name, "namespace", pod.Object.Namespace)
			switch pod.Type {
			case informer.Added, informer.Modified:
				err := s.SyncPodToSrc(ctx, pod.Object)
				if err != nil {
					slog.Error("sync pod", "err", err)
				}
			case informer.Deleted:
				err := s.DeletePodToSrc(ctx, pod.Object)
				if err != nil {
					slog.Error("delete pod", "err", err)
				}
			default:
				slog.Error("sync pod", "err", fmt.Errorf("invalid event type: %s", pod.Type))
			}
		}
	}()

	go func() {
		for srcPod := range srcPodsEvent {
			slog.Info("Source Pod Event", "type", srcPod.Type, "name", srcPod.Object.Name, "namespace", srcPod.Object.Namespace)
			switch srcPod.Type {
			case informer.Added, informer.Modified:
				err := s.SyncPodStatusFromSrc(ctx, srcPod.Object)
				if err != nil {
					slog.Error("sync pod status from src", "err", err)
				}
			case informer.Deleted:
				err := s.DeletePodFromSrc(ctx, srcPod.Object)
				if err != nil {
					slog.Error("delete pod from src", "err", err)
				}
			default:
				slog.Error("sync pod status", "err", fmt.Errorf("invalid event type: %s", srcPod.Type))
			}
		}
	}()
	go func() {
		for srcNode := range srcNodeEvent {
			switch srcNode.Type {
			case informer.Added, informer.Modified, informer.Sync:
				err := s.SyncNodeFromSrc(ctx, srcNode.Object)
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

func (s *Sublet) SyncNodeFromSrc(ctx context.Context, node *corev1.Node) error {
	nodeClient := s.client.CoreV1().Nodes()
	dstNode, err := nodeClient.Get(ctx, s.nodeName, metav1.GetOptions{})
	if err != nil {
		if !apierrors.IsNotFound(err) {
			return err
		}
		dstNode = &corev1.Node{
			ObjectMeta: metav1.ObjectMeta{
				Name:        s.nodeName,
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

func (s *Sublet) SyncPodToSrc(ctx context.Context, pod *corev1.Pod) error {
	name := nameToSrc(pod.Name, pod.Namespace)
	if pod.DeletionTimestamp != nil {
		err := s.srcClient.CoreV1().Pods(SubletSystem).Delete(ctx, name, metav1.DeleteOptions{})
		if err != nil {
			if apierrors.IsNotFound(err) {
				err = s.client.CoreV1().Pods(pod.Namespace).Delete(ctx, pod.Name, *deleteOpt)
				if err != nil {
					return err
				}
				return nil
			}
			return err
		}
		return nil
	}
	srcPod, ok := s.srcPodsGetter.GetWithNamespace(name, SubletSystem)
	if !ok {
		srcPod = pod.DeepCopy()
		srcPod.Labels[subletNsKey] = pod.Namespace
		srcPod.Labels[subletNodeKey] = s.nodeName
		srcPod.Name = name
		srcPod.Namespace = SubletSystem
		srcPod.Spec.NodeName = s.srcNodeName
		srcPod.ResourceVersion = ""
		srcPod.UID = ""
		srcPod.OwnerReferences = nil

		_, err := s.srcClient.CoreV1().Pods(SubletSystem).Create(ctx, srcPod, metav1.CreateOptions{})
		if err == nil {
			return nil
		}

		if !apierrors.IsAlreadyExists(err) {
			return err
		}
		srcPod, ok = s.srcPodsGetter.GetWithNamespace(name, SubletSystem)
		if !ok {
			srcPod, err = s.srcClient.CoreV1().Pods(SubletSystem).Get(ctx, name, metav1.GetOptions{})
			if err != nil {
				return err
			}
		}
	}

	pod = pod.DeepCopy()
	srcPod = srcPod.DeepCopy()
	srcPod.Spec = pod.Spec
	srcPod.Labels = pod.Labels
	srcPod.Annotations = pod.Annotations
	srcPod.Labels[subletNsKey] = pod.Namespace
	srcPod.Labels[subletNodeKey] = s.nodeName
	srcPod.Spec.NodeName = s.srcNodeName
	srcPod.OwnerReferences = nil
	_, err := s.srcClient.CoreV1().Pods(SubletSystem).Update(ctx, srcPod, metav1.UpdateOptions{})
	if err != nil {
		if apierrors.IsConflict(err) {
			srcPod, err = s.srcClient.CoreV1().Pods(SubletSystem).Get(ctx, name, metav1.GetOptions{})
			if err != nil {
				return err
			}
			_, err = s.srcClient.CoreV1().Pods(SubletSystem).Update(ctx, srcPod, metav1.UpdateOptions{})
			if err != nil {
				return err
			}
			return nil
		}
	}

	return nil
}

func (s *Sublet) SyncPodStatusFromSrc(ctx context.Context, srcPod *corev1.Pod) error {
	name, namespace, err := nameToDst(srcPod.Name)
	if err != nil {
		return err
	}
	if srcPod.DeletionTimestamp != nil {
		err := s.client.CoreV1().Pods(namespace).Delete(ctx, name, metav1.DeleteOptions{})
		if err != nil {
			return err
		}
		return nil
	}
	pod, ok := s.podsGetter.GetWithNamespace(name, namespace)
	if !ok {
		pod, err = s.client.CoreV1().Pods(namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			if apierrors.IsNotFound(err) {
				err := s.srcClient.CoreV1().Pods(srcPod.Namespace).Delete(ctx, srcPod.Name, *deleteOpt)
				if err != nil {
					return err
				}
				return nil
			}
			return err
		}
	}

	pod = pod.DeepCopy()
	pod.Status = srcPod.Status

	_, err = s.client.CoreV1().Pods(pod.Namespace).UpdateStatus(ctx, pod, metav1.UpdateOptions{})
	if err != nil {
		if apierrors.IsConflict(err) {
			pod, err = s.client.CoreV1().Pods(namespace).Get(ctx, name, metav1.GetOptions{})
			if err != nil {
				return err
			}
			_, err = s.client.CoreV1().Pods(pod.Namespace).UpdateStatus(ctx, pod, metav1.UpdateOptions{})
			if err != nil {
				return err
			}
			return nil
		}
		if apierrors.IsNotFound(err) {
			err := s.srcClient.CoreV1().Pods(srcPod.Namespace).Delete(ctx, srcPod.Name, *deleteOpt)
			if err != nil {
				return err
			}
			return nil
		}
		return err
	}

	return nil
}

func (s *Sublet) DeletePodFromSrc(ctx context.Context, srcPod *corev1.Pod) error {
	name, namespace, err := nameToDst(srcPod.Name)
	if err != nil {
		return err
	}

	err = s.client.CoreV1().Pods(namespace).Delete(ctx, name, *deleteOpt)
	if err != nil && !apierrors.IsNotFound(err) {
		return err
	}

	return nil
}

// DeletePodToSrc deletes the specified pod out of memory.
func (s *Sublet) DeletePodToSrc(ctx context.Context, pod *corev1.Pod) (err error) {
	name := nameToSrc(pod.Name, pod.Namespace)

	err = s.srcClient.CoreV1().Pods(SubletSystem).Delete(ctx, name, *deleteOpt)
	if err != nil && !apierrors.IsNotFound(err) {
		return err
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
