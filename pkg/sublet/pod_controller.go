package sublet

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/wzshiming/subcluster/pkg/informer"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/kubernetes"
	"k8s.io/utils/clock"
)

type PodController struct {
	clock clock.Clock

	subclusterName    string
	nodeName          string
	client            kubernetes.Interface
	cachePodsInformer *informer.Informer[*corev1.Pod, *corev1.PodList]
	podsGetter        informer.Getter[*corev1.Pod]
	dnsServers        []string
	dnsSearches       []string

	sourceNodeName       string
	sourceClient         kubernetes.Interface
	srcCachePodsInformer *informer.Informer[*corev1.Pod, *corev1.PodList]
	srcPodsGetter        informer.Getter[*corev1.Pod]
	sourceNamespace      string
}

type PodControllerConfig struct {
	SubclusterName  string
	NodeName        string
	Client          kubernetes.Interface
	SourceNodeName  string
	SourceClient    kubernetes.Interface
	SourceNamespace string
	DnsServers      []string
	DnsSearches     []string
}

func NewPodController(conf PodControllerConfig) (*PodController, error) {
	s := PodController{
		clock:           clock.RealClock{},
		subclusterName:  conf.SubclusterName,
		nodeName:        conf.NodeName,
		client:          conf.Client,
		sourceNodeName:  conf.SourceNodeName,
		sourceClient:    conf.SourceClient,
		sourceNamespace: conf.SourceNamespace,
		dnsServers:      conf.DnsServers,
		dnsSearches:     conf.DnsSearches,
	}
	return &s, nil
}

func (s *PodController) Start(ctx context.Context) error {
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

	srcCachePodsInformer := informer.NewInformer[*corev1.Pod, *corev1.PodList](s.sourceClient.CoreV1().Pods(s.sourceNamespace))
	srcPodsEvent := make(chan informer.Event[*corev1.Pod])
	srcPodsGetter, err := srcCachePodsInformer.WatchWithCache(ctx, informer.Option{
		LabelSelector: labels.SelectorFromSet(map[string]string{
			subletNodeKey:    s.nodeName,
			subletClusterKey: s.subclusterName,
		}).String(),
		FieldSelector: fields.OneTermEqualSelector("spec.nodeName", s.sourceNodeName).String(),
	}, srcPodsEvent)
	if err != nil {
		return err
	}
	s.srcCachePodsInformer = srcCachePodsInformer
	s.srcPodsGetter = srcPodsGetter

	go func() {
		for pod := range podsEvent {
			slog.Info("Pod Event", "type", pod.Type, "name", pod.Object.Name, "namespace", pod.Object.Namespace)
			switch pod.Type {
			case informer.Added, informer.Modified:
				err := s.SyncPodToSource(ctx, pod.Object)
				if err != nil {
					slog.Error("sync pod", "err", err)
				}
			case informer.Deleted:
				err := s.DeletePodToSource(ctx, pod.Object)
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
				err := s.SyncPodStatusFromSource(ctx, srcPod.Object)
				if err != nil {
					slog.Error("sync pod status from src", "err", err)
				}
			case informer.Deleted:
				err := s.DeletePodFromSource(ctx, srcPod.Object)
				if err != nil {
					slog.Error("delete pod from src", "err", err)
				}
			default:
				slog.Error("sync pod status", "err", fmt.Errorf("invalid event type: %s", srcPod.Type))
			}
		}
	}()

	return nil
}

func (s *PodController) SyncPodToSource(ctx context.Context, pod *corev1.Pod) error {
	name := nameToSource(s.subclusterName, pod.Namespace, pod.Name)
	if pod.DeletionTimestamp != nil {
		err := s.sourceClient.CoreV1().Pods(s.sourceNamespace).Delete(ctx, name, metav1.DeleteOptions{})
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
	srcPod, ok := s.srcPodsGetter.GetWithNamespace(name, s.sourceNamespace)
	if !ok {
		srcPod = pod.DeepCopy()
		srcPod.Labels[subletNamespaceKey] = pod.Namespace
		srcPod.Labels[subletNodeKey] = s.nodeName
		srcPod.Labels[subletClusterKey] = s.subclusterName
		srcPod.Name = name
		srcPod.Namespace = s.sourceNamespace
		srcPod.Spec.NodeName = s.sourceNodeName
		srcPod.Spec.DNSPolicy = corev1.DNSNone
		srcPod.Spec.DNSConfig = &corev1.PodDNSConfig{
			Nameservers: s.dnsServers,
			Searches:    s.dnsSearches,
		}
		srcPod.ResourceVersion = ""
		srcPod.UID = ""
		srcPod.OwnerReferences = nil

		_, err := s.sourceClient.CoreV1().Pods(s.sourceNamespace).Create(ctx, srcPod, metav1.CreateOptions{})
		if err == nil {
			return nil
		}

		if !apierrors.IsAlreadyExists(err) {
			return err
		}
		srcPod, ok = s.srcPodsGetter.GetWithNamespace(name, s.sourceNamespace)
		if !ok {
			srcPod, err = s.sourceClient.CoreV1().Pods(s.sourceNamespace).Get(ctx, name, metav1.GetOptions{})
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
	srcPod.Labels[subletNamespaceKey] = pod.Namespace
	srcPod.Labels[subletNodeKey] = s.nodeName
	srcPod.Labels[subletClusterKey] = s.subclusterName
	srcPod.Spec.NodeName = s.sourceNodeName
	srcPod.Spec.DNSPolicy = corev1.DNSNone
	srcPod.Spec.DNSConfig = &corev1.PodDNSConfig{
		Nameservers: s.dnsServers,
		Searches:    s.dnsSearches,
	}
	srcPod.OwnerReferences = nil
	_, err := s.sourceClient.CoreV1().Pods(s.sourceNamespace).Update(ctx, srcPod, metav1.UpdateOptions{})
	if err != nil {
		if apierrors.IsConflict(err) {
			srcPod, err = s.sourceClient.CoreV1().Pods(s.sourceNamespace).Get(ctx, name, metav1.GetOptions{})
			if err != nil {
				return err
			}
			_, err = s.sourceClient.CoreV1().Pods(s.sourceNamespace).Update(ctx, srcPod, metav1.UpdateOptions{})
			if err != nil {
				return err
			}
			return nil
		}
	}

	return nil
}

func (s *PodController) SyncPodStatusFromSource(ctx context.Context, srcPod *corev1.Pod) error {
	subclusterName, namespace, name, err := nameFromSource(srcPod.Name)
	if err != nil {
		return err
	}
	if subclusterName != s.subclusterName {
		return nil
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
				err := s.sourceClient.CoreV1().Pods(srcPod.Namespace).Delete(ctx, srcPod.Name, *deleteOpt)
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
			err := s.sourceClient.CoreV1().Pods(srcPod.Namespace).Delete(ctx, srcPod.Name, *deleteOpt)
			if err != nil {
				return err
			}
			return nil
		}
		return err
	}

	return nil
}

func (s *PodController) DeletePodFromSource(ctx context.Context, srcPod *corev1.Pod) error {
	subclusterName, namespace, name, err := nameFromSource(srcPod.Name)
	if err != nil {
		return err
	}
	if subclusterName != s.subclusterName {
		return nil
	}
	err = s.client.CoreV1().Pods(namespace).Delete(ctx, name, *deleteOpt)
	if err != nil && !apierrors.IsNotFound(err) {
		return err
	}

	return nil
}

// DeletePodToSource deletes the specified pod out of memory.
func (s *PodController) DeletePodToSource(ctx context.Context, pod *corev1.Pod) (err error) {
	name := nameToSource(s.subclusterName, pod.Namespace, pod.Name)

	err = s.sourceClient.CoreV1().Pods(s.sourceNamespace).Delete(ctx, name, *deleteOpt)
	if err != nil && !apierrors.IsNotFound(err) {
		return err
	}

	return nil
}
