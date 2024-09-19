package informer

import (
	"context"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/tools/cache"
)

// Informer is a wrapper around a Get/List/Watch function.
type Informer[T runtime.Object, L runtime.Object] struct {
	ListFunc  func(ctx context.Context, opts metav1.ListOptions) (L, error)
	WatchFunc func(ctx context.Context, opts metav1.ListOptions) (watch.Interface, error)
}

// NewInformer returns a new Informer.
func NewInformer[T runtime.Object, L runtime.Object](lw Watcher[T, L]) *Informer[T, L] {
	return &Informer[T, L]{
		ListFunc:  lw.List,
		WatchFunc: lw.Watch,
	}
}

func (i *Informer[T, L]) listWatch(ctx context.Context) *cache.ListWatch {
	return &cache.ListWatch{
		ListFunc: func(opts metav1.ListOptions) (runtime.Object, error) {
			return i.ListFunc(ctx, opts)
		},
		WatchFunc: func(opts metav1.ListOptions) (watch.Interface, error) {
			return i.WatchFunc(ctx, opts)
		},
	}
}

// WatchWithCache starts a goroutine that watches the resource and sends events to the events channel.
func (i *Informer[T, L]) WatchWithCache(ctx context.Context, opt Option, events chan<- Event[T]) (Getter[T], error) {
	store, controller := newCacheInformer[T](i.listWatch(ctx), opt, events)
	go controller.Run(ctx.Done())

	g := &getter[T]{store: store}
	return g, nil
}

func newCacheInformer[T runtime.Object](listWatch cache.ListerWatcher, opt Option, events chan<- Event[T]) (cache.Store, cache.Controller) {
	var t T
	eventHandler := cache.ResourceEventHandlerFuncs{}
	if events != nil {
		eventHandler = cache.ResourceEventHandlerFuncs{
			AddFunc: func(obj any) {
				if ok, err := opt.filter(obj); err != nil {
					return
				} else if !ok {
					return
				}
				events <- Event[T]{Type: Added, Object: obj.(T)}
			},
			UpdateFunc: func(oldObj, newObj any) {
				if ok, err := opt.filter(newObj); err != nil {
					return
				} else if !ok {
					return
				}
				events <- Event[T]{Type: Modified, Object: newObj.(T)}
			},
			DeleteFunc: func(obj any) {
				if ok, err := opt.filter(obj); err != nil {
					return
				} else if !ok {
					return
				}
				events <- Event[T]{Type: Deleted, Object: obj.(T)}
			},
		}
	}
	store, controller := cache.NewInformerWithOptions(cache.InformerOptions{
		ListerWatcher: &cache.ListWatch{
			ListFunc: func(opts metav1.ListOptions) (runtime.Object, error) {
				opt.setup(&opts)
				return listWatch.List(opts)
			},
			WatchFunc: func(opts metav1.ListOptions) (watch.Interface, error) {
				opt.setup(&opts)
				return listWatch.Watch(opts)
			},
		},
		ObjectType: objType(t),
		Handler:    eventHandler,
	})

	return store, controller
}

// Watch starts a goroutine that watches the resource and sends events to the events channel.
func (i *Informer[T, L]) Watch(ctx context.Context, opt Option, events chan<- Event[T]) error {
	if events == nil {
		return fmt.Errorf("events channel is nil")
	}

	informer := newDummyInformer(i.listWatch(ctx), opt, events)
	go informer.Run(ctx.Done())

	return nil
}

func newDummyInformer[T runtime.Object](listWatch cache.ListerWatcher, opt Option, events chan<- Event[T]) *cache.Reflector {
	var t T
	informer := cache.NewReflectorWithOptions(
		&cache.ListWatch{
			ListFunc: func(opts metav1.ListOptions) (runtime.Object, error) {
				opt.setup(&opts)
				return listWatch.List(opts)
			},
			WatchFunc: func(opts metav1.ListOptions) (watch.Interface, error) {
				opt.setup(&opts)
				return listWatch.Watch(opts)
			},
		},
		objType(t),
		dummyCache(events, opt),
		cache.ReflectorOptions{},
	)
	return informer
}

func dummyCache[T runtime.Object](ch chan<- Event[T], opt Option) cache.Store {
	return &cache.FakeCustomStore{
		AddFunc: func(obj any) error {
			if ok, err := opt.filter(obj); err != nil {
				return err
			} else if !ok {
				return nil
			}
			ch <- Event[T]{Type: Added, Object: obj.(T)}
			return nil
		},
		UpdateFunc: func(obj any) error {
			if ok, err := opt.filter(obj); err != nil {
				return err
			} else if !ok {
				return nil
			}
			ch <- Event[T]{Type: Modified, Object: obj.(T)}
			return nil
		},
		DeleteFunc: func(obj any) error {
			if ok, err := opt.filter(obj); err != nil {
				return err
			} else if !ok {
				return nil
			}
			ch <- Event[T]{Type: Deleted, Object: obj.(T)}
			return nil
		},
		ReplaceFunc: func(list []any, resourceVersion string) error {
			for _, obj := range list {
				if ok, err := opt.filter(obj); err != nil {
					return err
				} else if !ok {
					continue
				}
				ch <- Event[T]{Type: Sync, Object: obj.(T)}
			}
			return nil
		},
		ListFunc: func() []any {
			panic("unreachable")
		},
		ListKeysFunc: func() []string {
			panic("unreachable")
		},
		GetFunc: func(obj any) (item any, exists bool, err error) {
			panic("unreachable")
		},
		GetByKeyFunc: func(key string) (item any, exists bool, err error) {
			panic("unreachable")
		},
		ResyncFunc: func() error {
			return nil
		},
	}
}

// Getter is a wrapper around a cache.Store that provides Get and List methods.
type Getter[T runtime.Object] interface {
	Get(name string) (T, bool)
	GetWithNamespace(name, namespace string) (T, bool)
	List() []T
}

type getter[T runtime.Object] struct {
	store cache.Store
}

func (g *getter[T]) Get(name string) (t T, exists bool) {
	obj, exists, err := g.store.GetByKey(name)
	if err != nil {
		return t, false
	}
	if !exists {
		return t, false
	}
	return obj.(T), true
}

func (g *getter[T]) GetWithNamespace(name, namespace string) (t T, exists bool) {
	return g.Get(namespace + "/" + name)
}

func (g *getter[T]) List() (list []T) {
	for _, obj := range g.store.List() {
		list = append(list, obj.(T))
	}
	return list
}

func objType(expectedType runtime.Object) runtime.Object {
	switch expectedType.(type) {
	default:
		return expectedType
	case *unstructured.Unstructured:
		var obj unstructured.Unstructured
		return &obj
	}
}
