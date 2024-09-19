package informer

import (
	"context"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/watch"
)

// EventType defines the possible types of events.
type EventType string

// Event types.
const (
	Added    EventType = "ADDED"
	Modified EventType = "MODIFIED"
	Deleted  EventType = "DELETED"
	Sync     EventType = "SYNC"
)

// Event represents a single event to a watched resource.
type Event[T runtime.Object] struct {
	// Type is Added, Modified, Deleted, or Sync.
	Type EventType

	// Object is:
	//  * If Type is Added, Modified or Sync: the new state of the object.
	//  * If Type is Deleted: the state of the object immediately before deletion.
	Object T
}

// Watcher is an interface for objects that know how to watch resources.
type Watcher[T runtime.Object, L runtime.Object] interface {
	// List returns an object containing a list of the resources matching the provided options.
	List(ctx context.Context, opts metav1.ListOptions) (L, error)
	// Watch returns an object that watches the resources matching the provided options.
	Watch(ctx context.Context, opts metav1.ListOptions) (watch.Interface, error)
}

// Option is used to filter events.
type Option struct {
	LabelSelector      string
	FieldSelector      string
	AnnotationSelector string
	annotationSelector labels.Selector
}

func (o *Option) setup(opts *metav1.ListOptions) {
	if o.LabelSelector != "" {
		opts.LabelSelector = o.LabelSelector
	}
	if o.FieldSelector != "" {
		opts.FieldSelector = o.FieldSelector
	}
}

func (o *Option) toListOptions() metav1.ListOptions {
	opts := metav1.ListOptions{}
	o.setup(&opts)
	return opts
}

func (o *Option) filter(obj any) (bool, error) {
	if o.AnnotationSelector == "" {
		return true, nil
	}

	if o.annotationSelector == nil {
		var err error
		o.annotationSelector, err = labels.Parse(o.AnnotationSelector)
		if err != nil {
			return false, err
		}
	}

	accessor, err := meta.Accessor(obj)
	if err != nil {
		return false, err
	}

	annotations := accessor.GetAnnotations()
	if len(annotations) == 0 {
		return false, nil
	}

	return o.annotationSelector.Matches(labels.Set(annotations)), nil
}
