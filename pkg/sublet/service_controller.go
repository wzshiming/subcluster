package sublet

import (
	"context"
	"fmt"
	"log/slog"
	"reflect"

	"github.com/wzshiming/subcluster/pkg/informer"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/kubernetes"
	"k8s.io/utils/clock"
)

type ServiceController struct {
	clock clock.Clock

	subclusterName  string
	sourceNamespace string

	client kubernetes.Interface

	sourceClient kubernetes.Interface
}

type ServiceControllerConfig struct {
	SubclusterName  string
	SourceNamespace string
	Client          kubernetes.Interface
	SourceClient    kubernetes.Interface
}

func NewServiceController(conf ServiceControllerConfig) (*ServiceController, error) {
	s := ServiceController{
		clock:           clock.RealClock{},
		client:          conf.Client,
		sourceClient:    conf.SourceClient,
		sourceNamespace: conf.SourceNamespace,
		subclusterName:  conf.SubclusterName,
	}
	return &s, nil
}

func (s *ServiceController) Start(ctx context.Context) error {

	srcCacheNodeInformer := informer.NewInformer[*corev1.Service, *corev1.ServiceList](s.sourceClient.CoreV1().Services(""))
	srcNodeEvent := make(chan informer.Event[*corev1.Service])
	srcSvcsGetter, err := srcCacheNodeInformer.WatchWithCache(ctx, informer.Option{
		LabelSelector: labels.SelectorFromSet(map[string]string{
			subletClusterKey: s.subclusterName,
		}).String(),
	}, srcNodeEvent)
	if err != nil {
		return err
	}

	go func() {
		for srcNode := range srcNodeEvent {
			switch srcNode.Type {
			case informer.Added, informer.Modified, informer.Sync:
				err := s.SyncServiceFromSource(ctx, srcNode.Object)
				if err != nil {
					slog.Error("sync service", "err", err)
				}
			default:
				slog.Error("sync service", "err", fmt.Errorf("invalid event type: %s", srcNode.Type))
			}
		}
	}()

	cacheSvcsInformer := informer.NewInformer[*corev1.Service, *corev1.ServiceList](s.client.CoreV1().Services(""))
	svcsEvent := make(chan informer.Event[*corev1.Service])
	_, err = cacheSvcsInformer.WatchWithCache(ctx, informer.Option{}, svcsEvent)
	if err != nil {
		return err
	}
	go func() {
		for svc := range svcsEvent {
			slog.Info("Service Event", "type", svc.Type, "name", svc.Object.Name, "namespace", svc.Object.Namespace)
			switch svc.Type {
			case informer.Added, informer.Modified:
				err := s.SyncServiceToSource(ctx, svc.Object, srcSvcsGetter)
				if err != nil {
					slog.Error("sync service", "err", err)
				}
			//case informer.Deleted:
			//	err := s.DeleteServiceToSource(ctx, svc.Object)
			//	if err != nil {
			//		slog.Error("delete service", "err", err)
			//	}
			default:
				slog.Error("sync service", "err", fmt.Errorf("invalid event type: %s", svc.Type))
			}
		}
	}()
	return nil
}

func (s *ServiceController) syncServiceToSource(svc, srcSvc *corev1.Service, name string) error {
	if srcSvc.Labels == nil {
		srcSvc.Labels = map[string]string{}
	}
	if srcSvc.Annotations == nil {
		srcSvc.Annotations = map[string]string{}
	}
	srcSvc.Labels[subletNamespaceKey] = svc.Namespace
	srcSvc.Labels[subletClusterKey] = s.subclusterName
	srcSvc.Name = name
	srcSvc.Namespace = s.sourceNamespace

	srcSvc.Spec.ClusterIP = ""
	srcSvc.Spec.ClusterIPs = nil
	srcSvc.ResourceVersion = ""
	srcSvc.UID = ""
	srcSvc.OwnerReferences = nil
	return nil
}

func (s *ServiceController) SyncServiceToSource(ctx context.Context, svc *corev1.Service, srcSvcsGetter informer.Getter[*corev1.Service]) error {
	if svc.Name == "kubernetes" && svc.Namespace == "default" {
		return nil
	}

	name := svcNameToSource(svc.Name)
	if svc.DeletionTimestamp != nil {
		err := s.sourceClient.CoreV1().Services(s.sourceNamespace).Delete(ctx, name, metav1.DeleteOptions{})
		if err != nil {
			if apierrors.IsNotFound(err) {
				err = s.client.CoreV1().Services(svc.Namespace).Delete(ctx, svc.Name, *deleteOpt)
				if err != nil {
					return err
				}
				return nil
			}
			return err
		}
		return nil
	}
	srcSvc, ok := srcSvcsGetter.GetWithNamespace(name, s.sourceNamespace)
	if !ok {
		srcSvc = svc.DeepCopy()
		err := s.syncServiceToSource(svc, srcSvc, name)
		if err != nil {
			return err
		}

		_, err = s.sourceClient.CoreV1().Services(s.sourceNamespace).Create(ctx, srcSvc, metav1.CreateOptions{})
		if err == nil {
			return nil
		}

		if !apierrors.IsAlreadyExists(err) {
			return err
		}
		srcSvc, ok = srcSvcsGetter.GetWithNamespace(name, s.sourceNamespace)
		if !ok {
			srcSvc, err = s.sourceClient.CoreV1().Services(s.sourceNamespace).Get(ctx, name, metav1.GetOptions{})
			if err != nil {
				return err
			}
		}
	}

	svc = svc.DeepCopy()
	srcSvc = srcSvc.DeepCopy()
	srcSvc.Spec = svc.Spec
	err := s.syncServiceToSource(svc, srcSvc, name)
	if err != nil {
		return err
	}
	_, err = s.sourceClient.CoreV1().Services(s.sourceNamespace).Update(ctx, srcSvc, metav1.UpdateOptions{})
	if err != nil {
		if apierrors.IsConflict(err) {
			srcSvc, err = s.sourceClient.CoreV1().Services(s.sourceNamespace).Get(ctx, name, metav1.GetOptions{})
			if err != nil {
				return err
			}
			_, err = s.sourceClient.CoreV1().Services(s.sourceNamespace).Update(ctx, srcSvc, metav1.UpdateOptions{})
			if err != nil {
				return err
			}
			return nil
		}
	}

	return nil
}

func (s *ServiceController) DeleteServiceToSource(ctx context.Context, svc *corev1.Service) (err error) {
	name := svcNameToSource(svc.Name)

	err = s.sourceClient.CoreV1().Services(s.sourceNamespace).Delete(ctx, name, metav1.DeleteOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		return err
	}

	return nil
}

func (s *ServiceController) SyncServiceFromSource(ctx context.Context, srcSvc *corev1.Service) error {
	srcSvc = srcSvc.DeepCopy()

	name, err := svcNameFromSource(srcSvc.Name)
	if err != nil {
		return err
	}

	namespace := srcSvc.Labels[subletNamespaceKey]
	if namespace == "" {
		return nil
	}
	svcClient := s.client.CoreV1().Services(namespace)
	svc, err := svcClient.Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		if !apierrors.IsNotFound(err) {
			return err
		}
		svc = &corev1.Service{
			ObjectMeta: metav1.ObjectMeta{
				Name:        name,
				Namespace:   namespace,
				Labels:      srcSvc.Labels,
				Annotations: srcSvc.Annotations,
			},
			Spec:   srcSvc.Spec,
			Status: srcSvc.Status,
		}
		_, err = svcClient.Create(ctx, svc, metav1.CreateOptions{})
		if err != nil {
			return fmt.Errorf("failed to create service: %w", err)
		}
		return nil
	}

	if svc.Spec.ClusterIP != srcSvc.Spec.ClusterIP ||
		svc.Spec.Type != srcSvc.Spec.Type {
		// TODO: use webhook instead
		err = svcClient.Delete(ctx, name, metav1.DeleteOptions{})
		if err != nil {
			return fmt.Errorf("failed to delete service: %w", err)
		}

		svc = &corev1.Service{
			ObjectMeta: metav1.ObjectMeta{
				Name:        name,
				Namespace:   namespace,
				Labels:      srcSvc.Labels,
				Annotations: srcSvc.Annotations,
			},
			Spec:   srcSvc.Spec,
			Status: srcSvc.Status,
		}
		_, err = svcClient.Create(ctx, svc, metav1.CreateOptions{})
		if err != nil {
			return fmt.Errorf("failed to create service: %w", err)
		}
		return nil
	}

	if !reflect.DeepEqual(svc.Spec, srcSvc.Spec) ||
		!reflect.DeepEqual(svc.Labels, srcSvc.Labels) ||
		!reflect.DeepEqual(svc.Annotations, srcSvc.Annotations) {
		svc.Labels = srcSvc.Labels
		svc.Annotations = srcSvc.Annotations
		svc.Spec = srcSvc.Spec
		svc, err = svcClient.Update(ctx, svc, metav1.UpdateOptions{})
		if err != nil {
			return fmt.Errorf("failed to update service: %w", err)
		}
	}
	if !reflect.DeepEqual(svc.Status, srcSvc.Status) {
		svc.Status = srcSvc.Status
		svc, err = svcClient.UpdateStatus(ctx, svc, metav1.UpdateOptions{})
		if err != nil {
			return fmt.Errorf("failed to update service: %w", err)
		}
	}

	return nil
}
