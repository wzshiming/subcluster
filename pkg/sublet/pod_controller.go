package sublet

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/wzshiming/subcluster/pkg/informer"
	"github.com/wzshiming/subcluster/pkg/utils/format"
	"github.com/wzshiming/subcluster/pkg/utils/slices"
	authenticationv1 "k8s.io/api/authentication/v1"
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

	subclusterName  string
	sourceNamespace string

	client     kubernetes.Interface
	dnsServers []string

	nodeMapping map[string]string

	nodePort int
	nodeIP   string

	sourceClient kubernetes.Interface
}

type PodControllerConfig struct {
	SubclusterName  string
	NodePort        int
	NodeIP          string
	NodeMapping     map[string]string
	Client          kubernetes.Interface
	SourceClient    kubernetes.Interface
	SourceNamespace string
	DnsServers      []string
}

func NewPodController(conf PodControllerConfig) (*PodController, error) {
	s := PodController{
		clock:           clock.RealClock{},
		subclusterName:  conf.SubclusterName,
		nodeIP:          conf.NodeIP,
		nodePort:        conf.NodePort,
		client:          conf.Client,
		nodeMapping:     conf.NodeMapping,
		sourceClient:    conf.SourceClient,
		sourceNamespace: conf.SourceNamespace,
		dnsServers:      conf.DnsServers,
	}
	return &s, nil
}

func (s *PodController) Start(ctx context.Context) error {
	for nodeName, sourceNodeName := range s.nodeMapping {
		err := s.start(ctx, nodeName, sourceNodeName)
		if err != nil {
			return err
		}
	}
	return nil
}

func (s *PodController) start(ctx context.Context, nodeName string, sourceNodeName string) error {
	cachePodsInformer := informer.NewInformer[*corev1.Pod, *corev1.PodList](s.client.CoreV1().Pods(""))
	podsEvent := make(chan informer.Event[*corev1.Pod])
	podsGetter, err := cachePodsInformer.WatchWithCache(ctx, informer.Option{
		FieldSelector: fields.OneTermEqualSelector("spec.nodeName", nodeName).String(),
	}, podsEvent)
	if err != nil {
		return err
	}

	srcCachePodsInformer := informer.NewInformer[*corev1.Pod, *corev1.PodList](s.sourceClient.CoreV1().Pods(s.sourceNamespace))
	srcPodsEvent := make(chan informer.Event[*corev1.Pod])
	srcPodsGetter, err := srcCachePodsInformer.WatchWithCache(ctx, informer.Option{
		LabelSelector: labels.SelectorFromSet(map[string]string{
			subletNodeKey:    nodeName,
			subletClusterKey: s.subclusterName,
		}).String(),
		FieldSelector: fields.OneTermEqualSelector("spec.nodeName", sourceNodeName).String(),
	}, srcPodsEvent)
	if err != nil {
		return err
	}

	go func() {
		for pod := range podsEvent {
			slog.Info("Pod Event", "type", pod.Type, "name", pod.Object.Name, "namespace", pod.Object.Namespace)
			switch pod.Type {
			case informer.Added, informer.Modified:
				err := s.SyncPodToSource(ctx, pod.Object, srcPodsGetter, sourceNodeName)
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
				err := s.SyncPodStatusFromSource(ctx, srcPod.Object, podsGetter)
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

func (s *PodController) syncPodToSource(pod, srcPod *corev1.Pod, name string, sourceNodeName string) error {
	if srcPod.Labels == nil {
		srcPod.Labels = map[string]string{}
	}
	if srcPod.Annotations == nil {
		srcPod.Annotations = map[string]string{}
	}
	srcPod.Labels[subletNamespaceKey] = pod.Namespace
	srcPod.Labels[subletNodeKey] = pod.Spec.NodeName
	srcPod.Labels[subletClusterKey] = s.subclusterName
	srcPod.Name = name
	srcPod.Namespace = s.sourceNamespace
	srcPod.Spec.NodeName = sourceNodeName

	if len(s.dnsServers) != 0 {
		srcPod.Spec.DNSPolicy = corev1.DNSNone
		srcPod.Spec.DNSConfig = &corev1.PodDNSConfig{}
		srcPod.Spec.DNSConfig.Nameservers = s.dnsServers

		srcPod.Spec.DNSConfig.Searches = []string{
			pod.Namespace + ".svc.cluster.local",
			"svc.cluster.local",
			"cluster.local",
		}
	}

	apiserverAddress := "kubernetes"

	srcPod.Spec.HostAliases = append(srcPod.Spec.HostAliases, corev1.HostAlias{
		IP: s.nodeIP,
		Hostnames: []string{
			apiserverAddress,
		},
	})

	if srcPod.Spec.AutomountServiceAccountToken == nil || *srcPod.Spec.AutomountServiceAccountToken == false {
		cm, err := s.client.CoreV1().ConfigMaps(pod.Namespace).Get(context.Background(), "kube-root-ca.crt", metav1.GetOptions{})
		if err != nil {
			return err
		}
		srcPod.Annotations["sublet-ca-crt"] = cm.Data["ca.crt"]

		tq, err := s.client.CoreV1().ServiceAccounts(pod.Namespace).CreateToken(context.Background(), pod.Spec.ServiceAccountName,
			&authenticationv1.TokenRequest{}, metav1.CreateOptions{})
		if err != nil {
			return err
		}
		srcPod.Annotations["sublet-token"] = tq.Status.Token

		srcPod.Spec.Volumes = slices.Map(srcPod.Spec.Volumes, func(volume corev1.Volume) corev1.Volume {
			if !strings.HasPrefix(volume.Name, "kube-api-") || volume.Projected == nil {
				return volume
			}

			volume.Projected = nil
			volume.DownwardAPI = &corev1.DownwardAPIVolumeSource{
				Items: []corev1.DownwardAPIVolumeFile{
					{
						Path: "ca.crt",
						FieldRef: &corev1.ObjectFieldSelector{
							FieldPath: "metadata.annotations['sublet-ca-crt']",
						},
					},
					{
						Path: "token",
						FieldRef: &corev1.ObjectFieldSelector{
							FieldPath: "metadata.annotations['sublet-token']",
						},
					},
					{
						Path: "namespace",
						FieldRef: &corev1.ObjectFieldSelector{
							FieldPath: "metadata.labels['" + subletNamespaceKey + "']",
						},
					},
				},
			}
			return volume
		})
	} else {
		srcPod.Spec.Volumes = slices.Filter(srcPod.Spec.Volumes, func(volume corev1.Volume) bool {
			return !strings.HasPrefix(volume.Name, "kube-api-")
		})
		srcPod.Spec.Containers = slices.Map(srcPod.Spec.Containers, func(container corev1.Container) corev1.Container {
			container.VolumeMounts = slices.Filter(container.VolumeMounts, func(mount corev1.VolumeMount) bool {
				return !strings.HasPrefix(mount.Name, "kube-api-")
			})
			return container
		})
	}

	srcPod.Spec.Containers = slices.Map(srcPod.Spec.Containers, func(container corev1.Container) corev1.Container {
		container.Env = append(container.Env,
			corev1.EnvVar{
				Name:  "KUBERNETES_SERVICE_HOST",
				Value: apiserverAddress,
			},
			corev1.EnvVar{
				Name:  "KUBERNETES_SERVICE_PORT",
				Value: "443",
			},
		)
		return container
	})

	srcPod.Spec.AutomountServiceAccountToken = format.Ptr(false)
	srcPod.Spec.ServiceAccountName = "default"
	srcPod.Spec.DeprecatedServiceAccount = "default"
	srcPod.ResourceVersion = ""
	srcPod.UID = ""
	srcPod.OwnerReferences = nil
	return nil
}

func (s *PodController) SyncPodToSource(ctx context.Context, pod *corev1.Pod, srcPodsGetter informer.Getter[*corev1.Pod], sourceNodeName string) error {
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
	srcPod, ok := srcPodsGetter.GetWithNamespace(name, s.sourceNamespace)
	if !ok {
		srcPod = pod.DeepCopy()
		err := s.syncPodToSource(pod, srcPod, name, sourceNodeName)
		if err != nil {
			return err
		}

		_, err = s.sourceClient.CoreV1().Pods(s.sourceNamespace).Create(ctx, srcPod, metav1.CreateOptions{})
		if err == nil {
			return nil
		}

		if !apierrors.IsAlreadyExists(err) {
			return err
		}
		srcPod, ok = srcPodsGetter.GetWithNamespace(name, s.sourceNamespace)
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
	err := s.syncPodToSource(pod, srcPod, name, sourceNodeName)
	if err != nil {
		return err
	}
	_, err = s.sourceClient.CoreV1().Pods(s.sourceNamespace).Update(ctx, srcPod, metav1.UpdateOptions{})
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

func (s *PodController) SyncPodStatusFromSource(ctx context.Context, srcPod *corev1.Pod, podsGetter informer.Getter[*corev1.Pod]) error {
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
	pod, ok := podsGetter.GetWithNamespace(name, namespace)
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
