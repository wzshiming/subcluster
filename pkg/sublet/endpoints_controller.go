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

type EndpointsController struct {
	clock clock.Clock

	subclusterName  string
	sourceNamespace string

	client kubernetes.Interface

	sourceClient kubernetes.Interface
}

type EndpointsControllerConfig struct {
	SubclusterName  string
	SourceNamespace string
	Client          kubernetes.Interface
	SourceClient    kubernetes.Interface
}

func NewEndpointsController(conf EndpointsControllerConfig) (*EndpointsController, error) {
	s := EndpointsController{
		clock:           clock.RealClock{},
		client:          conf.Client,
		sourceClient:    conf.SourceClient,
		sourceNamespace: conf.SourceNamespace,
		subclusterName:  conf.SubclusterName,
	}
	return &s, nil
}

func (s *EndpointsController) Start(ctx context.Context) error {

	srcCacheNodeInformer := informer.NewInformer[*corev1.Endpoints, *corev1.EndpointsList](s.sourceClient.CoreV1().Endpoints(""))
	srcNodeEvent := make(chan informer.Event[*corev1.Endpoints])
	_, err := srcCacheNodeInformer.WatchWithCache(ctx, informer.Option{
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
				err := s.SyncEndpointsFromSource(ctx, srcNode.Object)
				if err != nil {
					slog.Error("sync endpoints", "err", err)
				}
			case informer.Deleted:
				err := s.DeleteEndpointsFromSource(ctx, srcNode.Object)
				if err != nil {
					slog.Error("delete endpoints", "err", err)
				}
			default:
				slog.Error("sync endpoints", "err", fmt.Errorf("invalid event type: %s", srcNode.Type))
			}
		}
	}()

	return nil
}

func (s *EndpointsController) DeleteEndpointsFromSource(ctx context.Context, srcSvc *corev1.Endpoints) (err error) {
	name, err := svcNameFromSource(srcSvc.Name)
	if err != nil {
		return err
	}
	namespace := srcSvc.Labels[subletNamespaceKey]
	if namespace == "" {
		return nil
	}
	err = s.client.CoreV1().Endpoints(namespace).Delete(ctx, name, metav1.DeleteOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		return err
	}

	return nil
}

func (s *EndpointsController) SyncEndpointsFromSource(ctx context.Context, srcSvc *corev1.Endpoints) error {
	srcSvc = srcSvc.DeepCopy()

	name, err := svcNameFromSource(srcSvc.Name)
	if err != nil {
		return err
	}

	namespace := srcSvc.Labels[subletNamespaceKey]
	if namespace == "" {
		return nil
	}
	svcClient := s.client.CoreV1().Endpoints(namespace)
	svc, err := svcClient.Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		if !apierrors.IsNotFound(err) {
			return err
		}
		svc = &corev1.Endpoints{
			ObjectMeta: metav1.ObjectMeta{
				Name:        name,
				Namespace:   namespace,
				Labels:      srcSvc.Labels,
				Annotations: srcSvc.Annotations,
			},
			Subsets: srcSvc.Subsets,
		}
		_, err = svcClient.Create(ctx, svc, metav1.CreateOptions{})
		if err != nil {
			return fmt.Errorf("failed to create endpoint: %w", err)
		}
		return nil
	}

	if !reflect.DeepEqual(svc.Subsets, srcSvc.Subsets) {
		svc.Subsets = srcSvc.Subsets
		svc, err = svcClient.Update(ctx, svc, metav1.UpdateOptions{})
		if err != nil {
			return fmt.Errorf("failed to update endpoint: %w", err)
		}
	}

	return nil
}
