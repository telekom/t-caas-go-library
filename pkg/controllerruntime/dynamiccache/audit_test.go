// SPDX-FileCopyrightText: 2026 Deutsche Telekom AG
//
// SPDX-License-Identifier: Apache-2.0

package dynamiccache

import (
	"context"
	"errors"
	"net/http"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/rest"
	toolscache "k8s.io/client-go/tools/cache"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func TestObjectPrototypes(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	dc := &dynamicCache{scheme: scheme}
	custom := schema.GroupVersionKind{Group: "example.com", Version: "v1", Kind: "Widget"}
	obj, err := dc.objectFor(custom)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := obj.(*unstructured.Unstructured); !ok || obj.GetObjectKind().GroupVersionKind() != custom {
		t.Fatalf("unexpected prototype: %#v", obj)
	}
	if _, err := dc.objectFor(corev1.SchemeGroupVersion.WithKind("Status")); err == nil {
		t.Fatal("non-client runtime object must be rejected")
	}
}

func TestNamespaceEventConversion(t *testing.T) {
	dc := &dynamicCache{runCtx: context.Background(), doneCh: make(chan struct{})}
	a := newNamespaceInformerAdapter(dc, &corev1.Namespace{})
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "demo"}}
	event, ok := a.eventObject(toolscache.DeletedFinalStateUnknown{Key: "demo", Obj: ns})
	if !ok {
		t.Fatal("namespace tombstone was rejected")
	}
	tombstone, ok := event.(toolscache.DeletedFinalStateUnknown)
	if !ok || tombstone.Key != "demo" || tombstone.Obj.(*corev1.Namespace).Name != "demo" {
		t.Fatalf("unexpected tombstone: %#v", event)
	}
	if _, ok := a.eventObject(&corev1.ConfigMap{}); ok {
		t.Fatal("non-namespace event was accepted")
	}
	invalid := newNamespaceInformerAdapter(dc, &corev1.ConfigMap{})
	if _, ok := invalid.eventObject(ns); ok {
		t.Fatal("incompatible prototype was accepted")
	}
	if a.IsStopped() {
		t.Fatal("new adapter is stopped")
	}
	close(dc.doneCh)
	if !a.IsStopped() {
		t.Fatal("adapter did not observe shutdown")
	}
}

type indexFailingCache struct {
	cache.Cache
	err error
}

func (c indexFailingCache) IndexField(context.Context, client.Object, string, client.IndexerFunc) error {
	return c.err
}

func TestIndexErrors(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	mapper := apimeta.NewDefaultRESTMapper([]schema.GroupVersion{corev1.SchemeGroupVersion})
	mapper.Add(corev1.SchemeGroupVersion.WithKind("ConfigMap"), apimeta.RESTScopeNamespace)
	mapper.Add(corev1.SchemeGroupVersion.WithKind("Node"), apimeta.RESTScopeRoot)
	boom := errors.New("index unavailable")
	dc := &dynamicCache{
		scheme: scheme,
		mapper: mapper,
		nsEntries: map[string]*nsEntry{
			"demo": {name: "demo", cache: indexFailingCache{err: boom}},
		},
	}
	extract := func(client.Object) []string { return nil }
	ctx := t.Context()
	for _, obj := range []client.Object{&corev1.Namespace{}, &corev1.Node{}, &corev1.Pod{}} {
		if err := dc.IndexField(ctx, obj, "example", extract); err == nil {
			t.Fatalf("expected unsupported index for %T", obj)
		}
	}
	if err := dc.IndexField(ctx, &corev1.ConfigMap{}, "example", extract); !errors.Is(err, boom) {
		t.Fatalf("namespace index error lost cause: %v", err)
	}
	unregistered := &unstructured.Unstructured{}
	if _, err := dc.GetInformer(ctx, unregistered); err == nil {
		t.Fatal("missing GVK was accepted")
	}
	if _, err := dc.GetInformerForKind(ctx, schema.GroupVersionKind{}); err == nil {
		t.Fatal("missing kind was accepted")
	}
}

func TestConstructorTransportErrors(t *testing.T) {
	opts := Options{NamespaceSelector: labels.SelectorFromSet(labels.Set{"example.com/team": "demo"})}
	_, err := New(&rest.Config{TLSClientConfig: rest.TLSClientConfig{CertData: []byte("invalid"), KeyData: []byte("invalid")}},
		cache.Options{}, opts)
	if err == nil {
		t.Fatal("invalid TLS client credentials were accepted")
	}
	_, err = New(&rest.Config{Host: "://invalid"}, cache.Options{HTTPClient: http.DefaultClient}, opts)
	if err == nil {
		t.Fatal("invalid API server URL was accepted")
	}
}

type handlerRejectingInformer struct {
	toolscache.SharedIndexInformer
	err error
}

func (i handlerRejectingInformer) AddEventHandler(
	toolscache.ResourceEventHandler,
) (toolscache.ResourceEventHandlerRegistration, error) {
	return nil, i.err
}

func TestStartRegistrationFailure(t *testing.T) {
	boom := errors.New("handler registration unavailable")
	dc := &dynamicCache{
		nsInformer: handlerRejectingInformer{err: boom},
		doneCh:     make(chan struct{}),
	}
	if err := dc.Start(t.Context()); !errors.Is(err, boom) {
		t.Fatalf("start lost registration error: %v", err)
	}
	select {
	case <-dc.doneCh:
	default:
		t.Fatal("failed start left completion checkers running")
	}
	if err := dc.Start(t.Context()); err == nil {
		t.Fatal("failed cache start was allowed to restart")
	}
}
