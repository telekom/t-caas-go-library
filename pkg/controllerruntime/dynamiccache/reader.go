// SPDX-FileCopyrightText: 2026 Deutsche Telekom AG
// SPDX-FileCopyrightText: 2019 The Kubernetes Authors
//
// SPDX-License-Identifier: Apache-2.0

package dynamiccache

// The cross-namespace List merge semantics follow controller-runtime's
// internal multiNamespaceCache (Apache-2.0, The Kubernetes Authors).
// See NOTICE for the upstream source and attribution.

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/apiutil"
)

const continueNotSupported = "continue-not-supported"

// Get implements client.Reader. Objects in namespaces that do not match the
// selector are reported as NotFound: absence from the selection is data, not
// an error, so level-based reconcilers converge after a namespace is
// deselected (mirroring how label-selected caches treat filtered-out objects).
func (dc *dynamicCache) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	if !dc.readReady() {
		return &ErrCacheNotStarted{}
	}
	gvk, err := apiutil.GVKForObject(obj, dc.scheme)
	if err != nil {
		return err
	}
	if gvk == namespaceGVK {
		return dc.getNamespace(key.Name, obj)
	}
	namespaced, err := dc.isNamespacedGVK(gvk)
	if err != nil {
		return err
	}
	if !namespaced {
		if dc.clusterCache == nil {
			return &ErrClusterScopedUnsupported{GVK: gvk}
		}
		return dc.clusterCache.Get(ctx, key, obj, opts...)
	}
	if key.Namespace == "" {
		return fmt.Errorf("dynamiccache: getting namespaced object %s without a namespace", gvk)
	}

	dc.mu.RLock()
	entry, ok := dc.nsEntries[key.Namespace]
	dc.mu.RUnlock()
	if !ok {
		// A selected namespace without a running cache is degraded, not
		// absent: report a transient error, never NotFound.
		if dc.namespaceSelected(key.Namespace) {
			return &ErrNamespaceNotReady{Namespace: key.Namespace}
		}
		return apierrors.NewNotFound(dc.groupResource(gvk), key.Name)
	}
	return namespaceRead(ctx, entry, func(readCtx context.Context) error {
		return entry.cache.Get(readCtx, key, obj, opts...)
	})
}

// namespaceRead bounds upstream's lazy informer sync wait while preserving
// ReaderFailOnMissingInformer and the caller's own cancellation errors.
func namespaceRead(ctx context.Context, entry *nsEntry, read func(context.Context) error) error {
	readCtx, cancel := context.WithTimeout(ctx, subCacheSyncProbeTimeout)
	defer cancel()
	err := read(readCtx)
	var notStarted *cache.ErrCacheNotStarted
	if errors.As(err, &notStarted) || (err != nil && readCtx.Err() == context.DeadlineExceeded && ctx.Err() == nil) {
		return &ErrNamespaceNotReady{Namespace: entry.name}
	}
	return err
}

// namespaceSelected reports whether the namespace is currently in the
// selector-filtered namespace informer store.
func (dc *dynamicCache) namespaceSelected(namespace string) bool {
	_, exists, err := dc.nsInformer.GetStore().GetByKey(namespace)
	return err == nil && exists
}

// List implements client.Reader. A cross-namespace List fans out over all
// currently selected namespaces; a List scoped to an unselected namespace
// returns an empty result.
//
//nolint:gocyclo // ported verbatim from the upstream multi-namespace List merge; splitting obscures the merge semantics.
func (dc *dynamicCache) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	if !dc.readReady() {
		return &ErrCacheNotStarted{}
	}
	gvk, err := apiutil.GVKForObject(list, dc.scheme)
	if err != nil {
		return err
	}
	itemGVK := gvk
	itemGVK.Kind = strings.TrimSuffix(gvk.Kind, "List")

	listOpts := client.ListOptions{}
	listOpts.ApplyOptions(opts)
	if listOpts.Continue != "" {
		return fmt.Errorf("dynamiccache: continuation tokens are not supported")
	}

	if itemGVK == namespaceGVK {
		return dc.listNamespaces(list, listOpts)
	}
	namespaced, err := dc.isNamespacedGVK(itemGVK)
	if err != nil {
		return err
	}
	if !namespaced {
		if dc.clusterCache == nil {
			return &ErrClusterScopedUnsupported{GVK: itemGVK}
		}
		return dc.clusterCache.List(ctx, list, opts...)
	}

	if listOpts.Namespace != "" {
		dc.mu.RLock()
		entry, ok := dc.nsEntries[listOpts.Namespace]
		dc.mu.RUnlock()
		if !ok {
			if dc.namespaceSelected(listOpts.Namespace) {
				return &ErrNamespaceNotReady{Namespace: listOpts.Namespace}
			}
			return apimeta.SetList(list, nil)
		}
		return namespaceRead(ctx, entry, func(readCtx context.Context) error {
			return entry.cache.List(readCtx, list, opts...)
		})
	}

	dc.mu.RLock()
	entries := slices.Collect(maps.Values(dc.nsEntries))
	// A cross-namespace List must not silently omit a selected namespace whose
	// cache is not running; surface the degradation as a transient error.
	var notReady string
	for _, ns := range dc.nsInformer.GetStore().ListKeys() {
		if _, ok := dc.nsEntries[ns]; !ok {
			notReady = ns
			break
		}
	}
	dc.mu.RUnlock()
	if notReady != "" {
		return &ErrNamespaceNotReady{Namespace: notReady}
	}
	// Deterministic order across calls.
	slices.SortFunc(entries, func(a, b *nsEntry) int { return cmp.Compare(a.name, b.name) })

	limitSet := listOpts.Limit > 0
	remaining := listOpts.Limit
	var items []runtime.Object
	resourceVersion := ""
	for _, entry := range entries {
		sub := list.DeepCopyObject().(client.ObjectList)
		if err := apimeta.SetList(sub, nil); err != nil {
			return err
		}
		subOpts := opts
		if limitSet {
			subOpts = append(append([]client.ListOption{}, opts...), client.Limit(remaining))
		}
		if err := namespaceRead(ctx, entry, func(readCtx context.Context) error {
			return entry.cache.List(readCtx, sub, subOpts...)
		}); err != nil {
			return fmt.Errorf("dynamiccache: listing %s in namespace %q: %w", itemGVK, entry.name, err)
		}
		subItems, err := apimeta.ExtractList(sub)
		if err != nil {
			return err
		}
		items = append(items, subItems...)
		if accessor, err := apimeta.ListAccessor(sub); err == nil {
			resourceVersion = accessor.GetResourceVersion()
		}
		if limitSet {
			remaining -= int64(len(subItems))
			if remaining <= 0 {
				break
			}
		}
	}
	if err := apimeta.SetList(list, items); err != nil {
		return err
	}
	if accessor, err := apimeta.ListAccessor(list); err == nil {
		accessor.SetResourceVersion(resourceVersion)
	}
	list.SetContinue(continueNotSupported)
	return nil
}

// getNamespace serves Namespace objects from the internal namespace informer.
// Only namespaces matching the selector are visible.
func (dc *dynamicCache) getNamespace(name string, obj client.Object) error {
	item, exists, err := dc.nsInformer.GetStore().GetByKey(name)
	if err != nil {
		return err
	}
	if !exists {
		return apierrors.NewNotFound(corev1.Resource("namespaces"), name)
	}
	ns, ok := item.(*corev1.Namespace)
	if !ok {
		return fmt.Errorf("dynamiccache: unexpected object type %T in namespace store", item)
	}
	return assignNamespace(ns, obj)
}

func assignNamespace(ns *corev1.Namespace, obj client.Object) error {
	switch o := obj.(type) {
	case *corev1.Namespace:
		ns.DeepCopyInto(o)
		return nil
	case *unstructured.Unstructured:
		content, err := runtime.DefaultUnstructuredConverter.ToUnstructured(ns.DeepCopy())
		if err != nil {
			return err
		}
		o.SetUnstructuredContent(content)
		o.SetGroupVersionKind(namespaceGVK)
		return nil
	case *metav1.PartialObjectMetadata:
		o.TypeMeta = metav1.TypeMeta{APIVersion: namespaceGVK.GroupVersion().String(), Kind: namespaceGVK.Kind}
		ns.ObjectMeta.DeepCopyInto(&o.ObjectMeta)
		return nil
	default:
		return fmt.Errorf("dynamiccache: unsupported namespace object type %T", obj)
	}
}

// listNamespaces serves Namespace lists from the internal namespace informer.
func (dc *dynamicCache) listNamespaces(list client.ObjectList, listOpts client.ListOptions) error {
	if listOpts.FieldSelector != nil && !listOpts.FieldSelector.Empty() {
		return fmt.Errorf("dynamiccache: field selectors are not supported for namespace lists")
	}
	selector := listOpts.LabelSelector
	if selector == nil {
		selector = labels.Everything()
	}

	_, isUnstructured := list.(*unstructured.UnstructuredList)
	_, isMetadata := list.(*metav1.PartialObjectMetadataList)
	var items []runtime.Object
	for _, item := range dc.nsInformer.GetStore().List() {
		ns, ok := item.(*corev1.Namespace)
		if !ok {
			continue
		}
		if !selector.Matches(labels.Set(ns.Labels)) {
			continue
		}
		switch {
		case isUnstructured:
			content, err := runtime.DefaultUnstructuredConverter.ToUnstructured(ns.DeepCopy())
			if err != nil {
				return err
			}
			u := &unstructured.Unstructured{}
			u.SetUnstructuredContent(content)
			u.SetGroupVersionKind(namespaceGVK)
			items = append(items, u)
		case isMetadata:
			m := &metav1.PartialObjectMetadata{}
			if err := assignNamespace(ns, m); err != nil {
				return err
			}
			items = append(items, m)
		default:
			items = append(items, ns.DeepCopy())
		}
	}
	slices.SortFunc(items, func(a, b runtime.Object) int {
		return cmp.Compare(a.(client.Object).GetName(), b.(client.Object).GetName())
	})
	if listOpts.Limit > 0 && int64(len(items)) > listOpts.Limit {
		items = items[:listOpts.Limit]
	}
	if err := apimeta.SetList(list, items); err != nil {
		return err
	}
	list.SetContinue(continueNotSupported)
	return nil
}
