// SPDX-FileCopyrightText: 2026 Deutsche Telekom AG
//
// SPDX-License-Identifier: Apache-2.0

package dynamiccache

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/rest"
	toolscache "k8s.io/client-go/tools/cache"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/apiutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
)

// dynamicCache implements cache.Cache over a dynamic set of single-namespace
// caches, driven by a label-selected namespace informer.
type dynamicCache struct {
	config    *rest.Config
	cacheOpts cache.Options // template for per-namespace caches; DefaultNamespaces is set per namespace
	scheme    *runtime.Scheme
	mapper    apimeta.RESTMapper
	newCache  cache.NewCacheFunc

	nsInformer toolscache.SharedIndexInformer
	// clusterCache serves cluster-scoped types (except Namespace); nil when
	// Options.RejectClusterScoped is set.
	clusterCache cache.Cache
	// doneCh is closed when Start's context is cancelled; it bounds the
	// lifetime of internal polling goroutines.
	doneCh chan struct{}

	mu         sync.RWMutex
	started    bool
	runCtx     context.Context
	nsEntries  map[string]*nsEntry
	informers  map[informerKey]*fanoutInformer
	fieldIdxes []fieldIndex
}

type informerKey struct {
	gvk        schema.GroupVersionKind
	objectType reflect.Type
}

var _ cache.Cache = &dynamicCache{}

const (
	// syncPollInterval is the outer polling interval of WaitForCacheSync.
	syncPollInterval = 50 * time.Millisecond
	// subCacheSyncProbeTimeout bounds each per-namespace WaitForCacheSync
	// probe; generous enough for the sub-cache's internal 100ms sync poll to
	// observe an already-synced state at least once.
	subCacheSyncProbeTimeout = 250 * time.Millisecond
	// syntheticDeleteSnapshotTimeout bounds the object snapshot taken for
	// synthetic deletes when a namespace is deselected.
	syntheticDeleteSnapshotTimeout = 5 * time.Second
)

// hasStarted reports whether Start has been called.
func (dc *dynamicCache) hasStarted() bool {
	dc.mu.RLock()
	defer dc.mu.RUnlock()
	return dc.started
}

// readReady reports whether reads can produce meaningful results: Start was
// called and the namespace informer has listed at least once. Before that,
// every namespace would look unselected and reads would produce spurious
// NotFound/empty results (relevant e.g. for webhooks, which the manager
// starts before caches are synced).
func (dc *dynamicCache) readReady() bool {
	return dc.hasStarted() && dc.nsInformer.HasSynced()
}

// nsEntry is one running single-namespace cache.
type nsEntry struct {
	name   string
	cache  cache.Cache
	ctx    context.Context
	cancel context.CancelFunc
}

// fieldIndex records an IndexField call for replay onto later namespaces.
type fieldIndex struct {
	obj     client.Object
	field   string
	extract client.IndexerFunc
}

// Start runs the namespace informer and blocks until ctx is cancelled.
// Per-namespace caches are started and stopped as namespace events arrive.
//
//nolint:contextcheck // namespace lifecycles are bound to Start's context by design, not to event-handler callers
func (dc *dynamicCache) Start(ctx context.Context) error {
	dc.mu.Lock()
	if dc.started {
		dc.mu.Unlock()
		return errors.New("dynamiccache: cache was already started")
	}
	dc.started = true
	dc.runCtx = ctx
	dc.mu.Unlock()

	if _, err := dc.nsInformer.AddEventHandler(toolscache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			if ns, ok := obj.(*corev1.Namespace); ok {
				dc.ensureNamespace(ns.Name)
			}
		},
		UpdateFunc: func(_, obj interface{}) {
			// The list/watch is server-side filtered by the selector, so any
			// update still matches; this also retries namespaces whose cache
			// construction previously failed (via resync).
			if ns, ok := obj.(*corev1.Namespace); ok {
				dc.ensureNamespace(ns.Name)
			}
		},
		DeleteFunc: func(obj interface{}) {
			// Label removal surfaces as a watch DELETE because the list/watch
			// is server-side filtered; actual namespace deletion looks the same.
			if tombstone, ok := obj.(toolscache.DeletedFinalStateUnknown); ok {
				obj = tombstone.Obj
			}
			if ns, ok := obj.(*corev1.Namespace); ok {
				dc.removeNamespace(ns.Name)
			}
		},
	}); err != nil {
		// Leave nothing half-alive: doneCh bounds the pollChecker goroutines
		// and IsStopped reporting, and must be closed on this path too.
		close(dc.doneCh)
		return fmt.Errorf("dynamiccache: adding namespace event handler: %w", err)
	}

	if dc.clusterCache != nil {
		go func() {
			if err := dc.clusterCache.Start(ctx); err != nil {
				utilruntime.HandleErrorWithContext(ctx, err, "dynamiccache: cluster-scoped cache stopped with error")
			}
		}()
	}

	dc.nsInformer.RunWithContext(ctx) // blocks until ctx is done
	close(dc.doneCh)
	return nil
}

// ensureNamespace creates and starts the cache for a namespace if it does not
// exist yet, replays recorded field indexes and wires all fan-out informers.
func (dc *dynamicCache) ensureNamespace(name string) {
	dc.mu.Lock()
	if entry, ok := dc.nsEntries[name]; ok {
		fanouts := slices.Collect(maps.Values(dc.informers))
		dc.mu.Unlock()
		// Re-wire fan-outs on every event/resync: addNamespace is an
		// idempotent repair, so informer, indexer or handler registrations
		// that failed transiently on a previous attempt are retried here.
		for _, fi := range fanouts {
			fi.addNamespace(entry.ctx, name, entry.cache)
		}
		return
	}
	runCtx := dc.runCtx

	copts := dc.cacheOpts
	copts.DefaultNamespaces = map[string]cache.Config{name: {}}
	c, err := dc.newCache(dc.config, copts)
	if err != nil {
		dc.mu.Unlock()
		logf.FromContext(runCtx).Error(err, "failed to construct namespace cache, will retry on resync", "namespace", name)
		return
	}
	for _, fi := range dc.fieldIdxes {
		if err := c.IndexField(runCtx, fi.obj, fi.field, fi.extract); err != nil {
			dc.mu.Unlock()
			logf.FromContext(runCtx).Error(err, "failed to apply field index to namespace cache, will retry on resync",
				"namespace", name, "field", fi.field)
			return
		}
	}

	entryCtx, cancel := context.WithCancel(runCtx)
	entry := &nsEntry{name: name, cache: c, ctx: entryCtx, cancel: cancel}
	dc.nsEntries[name] = entry
	fanouts := slices.Collect(maps.Values(dc.informers))
	dc.mu.Unlock()

	go func() {
		if err := c.Start(entryCtx); err != nil {
			utilruntime.HandleErrorWithContext(entryCtx, err, "dynamiccache: namespace cache stopped with error", "namespace", name)
			// Tear the dead entry down so reads report ErrNamespaceNotReady
			// instead of serving a frozen cache, and so the next resync
			// rebuilds the namespace from scratch.
			dc.dropFailedEntry(name, entry)
		}
	}()

	for _, fi := range fanouts {
		fi.addNamespace(entryCtx, name, c)
	}
}

// dropFailedEntry removes a namespace entry whose cache terminated with an
// error. No synthetic deletes are delivered: the namespace is still selected
// and its state is rebuilt on the next resync.
func (dc *dynamicCache) dropFailedEntry(name string, failed *nsEntry) {
	dc.mu.Lock()
	current, ok := dc.nsEntries[name]
	if !ok || current != failed {
		dc.mu.Unlock()
		return
	}
	delete(dc.nsEntries, name)
	fanouts := slices.Collect(maps.Values(dc.informers))
	dc.mu.Unlock()

	failed.cancel()
	for _, fi := range fanouts {
		fi.dropNamespace(name)
	}
}

// removeNamespace stops a namespace's cache, drops it from all fan-out
// informers and delivers synthetic DELETE events for its cached objects.
func (dc *dynamicCache) removeNamespace(name string) {
	dc.mu.Lock()
	entry, ok := dc.nsEntries[name]
	if !ok {
		dc.mu.Unlock()
		return
	}
	delete(dc.nsEntries, name)
	fanouts := slices.Collect(maps.Values(dc.informers))
	dc.mu.Unlock()

	// Snapshot the namespace's objects per watched type before stopping the
	// watches so the synthetic deletes carry the last known object state.
	type pendingDeletes struct {
		fi   *fanoutInformer
		objs []client.Object
	}
	pending := make([]pendingDeletes, 0, len(fanouts))
	for _, fi := range fanouts {
		// An informer that never materialized or never synced (e.g. RBAC was
		// never granted) provably cached nothing — skip instead of letting
		// cache.List block this goroutine waiting for a sync that cannot
		// happen.
		if !fi.namespaceSnapshotReady(name) {
			logf.FromContext(entry.ctx).Info("skipping synthetic deletes for never-synced informer",
				"namespace", name, "gvk", fi.gvk.String())
			continue
		}
		objs, err := listCachedObjectsWithTimeout(entry, fi, name)
		if err != nil {
			// One immediate retry; the informer was synced, so a failure here
			// is unexpected and losing the snapshot means lost cleanup.
			objs, err = listCachedObjectsWithTimeout(entry, fi, name)
		}
		if err != nil {
			utilruntime.HandleErrorWithContext(entry.ctx, err,
				"dynamiccache: snapshot failed — synthetic deletes are LOST, reconcilers will not observe object removal",
				"gvk", fi.gvk.String(), "namespace", name)
			continue
		}
		pending = append(pending, pendingDeletes{fi: fi, objs: objs})
	}

	entry.cancel()
	for _, fi := range fanouts {
		fi.dropNamespace(name)
	}
	// Delivered after the entry is gone from nsEntries: reconcilers triggered
	// by these events observe NotFound from the reader and treat the object as
	// deleted.
	for _, p := range pending {
		p.fi.deliverSyntheticDeletes(name, p.objs)
	}
}

// listCachedObjectsWithTimeout snapshots a fan-out's objects in one namespace
// with a bounded deadline.
func listCachedObjectsWithTimeout(entry *nsEntry, fi *fanoutInformer, namespace string) ([]client.Object, error) {
	listCtx, cancel := context.WithTimeout(entry.ctx, syntheticDeleteSnapshotTimeout)
	defer cancel()
	return listCachedObjects(listCtx, entry.cache, fi.listPrototype(), namespace)
}

// listCachedObjects lists all objects of the fan-out's type in the given
// namespace from the (still running) namespace cache.
func listCachedObjects(ctx context.Context, c cache.Cache, list client.ObjectList, namespace string) ([]client.Object, error) {
	if err := c.List(ctx, list, client.InNamespace(namespace)); err != nil {
		return nil, err
	}
	items, err := apimeta.ExtractList(list)
	if err != nil {
		return nil, err
	}
	objs := make([]client.Object, 0, len(items))
	for _, item := range items {
		obj, ok := item.(client.Object)
		if !ok {
			return nil, fmt.Errorf("list item %T is not a client.Object", item)
		}
		objs = append(objs, obj)
	}
	return objs, nil
}

// GetInformer implements cache.Informers.
//
// InformerGetOptions are ignored: informers are always materialized
// non-blocking (a namespace with pending RBAC must not block callers) and
// sync is reported via WaitForCacheSync, HasSynced and the registration
// handles, which is what controller-runtime's watch sources rely on.
func (dc *dynamicCache) GetInformer(ctx context.Context, obj client.Object, opts ...cache.InformerGetOption) (cache.Informer, error) {
	gvk, err := apiutil.GVKForObject(obj, dc.scheme)
	if err != nil {
		return nil, err
	}
	return dc.informerForGVK(ctx, gvk, obj, opts...)
}

// GetInformerForKind implements cache.Informers.
func (dc *dynamicCache) GetInformerForKind(
	ctx context.Context, gvk schema.GroupVersionKind, opts ...cache.InformerGetOption,
) (cache.Informer, error) {
	obj, err := dc.objectFor(gvk)
	if err != nil {
		return nil, err
	}
	return dc.informerForGVK(ctx, gvk, obj, opts...)
}

func (dc *dynamicCache) informerForGVK(
	ctx context.Context, gvk schema.GroupVersionKind, obj client.Object, opts ...cache.InformerGetOption,
) (cache.Informer, error) {
	if gvk == namespaceGVK {
		if err := assignNamespace(&corev1.Namespace{}, obj.DeepCopyObject().(client.Object)); err != nil {
			return nil, err
		}
		return newNamespaceInformerAdapter(dc, obj), nil
	}
	namespaced, err := dc.isNamespacedGVK(gvk)
	if err != nil {
		return nil, err
	}
	if !namespaced {
		if dc.clusterCache == nil {
			return nil, &ErrClusterScopedUnsupported{GVK: gvk}
		}
		// Cluster-scoped types are served by the side cache directly; its
		// informers honor the caller's InformerGetOptions.
		return dc.clusterCache.GetInformer(ctx, obj, opts...)
	}

	key := informerKey{gvk: gvk, objectType: reflect.TypeOf(obj)}
	dc.mu.Lock()
	fi, ok := dc.informers[key]
	if !ok {
		fi = newFanoutInformer(dc, gvk, obj)
		dc.informers[key] = fi
	}
	entries := slices.Collect(maps.Values(dc.nsEntries))
	dc.mu.Unlock()

	for _, e := range entries {
		fi.addNamespace(e.ctx, e.name, e.cache) //nolint:contextcheck // informer lifetime is the namespace's, not the caller's
	}
	return fi, nil
}

// RemoveInformer implements cache.Informers.
func (dc *dynamicCache) RemoveInformer(ctx context.Context, obj client.Object) error {
	gvk, err := apiutil.GVKForObject(obj, dc.scheme)
	if err != nil {
		return err
	}
	if gvk == namespaceGVK {
		return errors.New("dynamiccache: the namespace informer cannot be removed")
	}
	if namespaced, err := dc.isNamespacedGVK(gvk); err != nil {
		return err
	} else if !namespaced {
		if dc.clusterCache == nil {
			return &ErrClusterScopedUnsupported{GVK: gvk}
		}
		return dc.clusterCache.RemoveInformer(ctx, obj)
	}

	key := informerKey{gvk: gvk, objectType: reflect.TypeOf(obj)}
	dc.mu.Lock()
	fi, ok := dc.informers[key]
	if ok {
		delete(dc.informers, key)
	}
	entries := slices.Collect(maps.Values(dc.nsEntries))
	dc.mu.Unlock()

	if fi != nil {
		fi.markRemoved()
	}
	var errs []error
	for _, e := range entries {
		if err := e.cache.RemoveInformer(ctx, obj); err != nil {
			errs = append(errs, fmt.Errorf("namespace %q: %w", e.name, err))
		}
	}
	return errors.Join(errs...)
}

// IndexField implements client.FieldIndexer. Indexes are recorded and applied
// to every current and future namespace cache.
func (dc *dynamicCache) IndexField(ctx context.Context, obj client.Object, field string, extractValue client.IndexerFunc) error {
	gvk, err := apiutil.GVKForObject(obj, dc.scheme)
	if err != nil {
		return err
	}
	if gvk == namespaceGVK {
		return errors.New("dynamiccache: field indexes on Namespace objects are not supported")
	}
	if namespaced, err := dc.isNamespacedGVK(gvk); err != nil {
		return err
	} else if !namespaced {
		if dc.clusterCache == nil {
			return &ErrClusterScopedUnsupported{GVK: gvk}
		}
		return dc.clusterCache.IndexField(ctx, obj, field, extractValue)
	}

	dc.mu.Lock()
	dc.fieldIdxes = append(dc.fieldIdxes, fieldIndex{obj: obj, field: field, extract: extractValue})
	entries := slices.Collect(maps.Values(dc.nsEntries))
	dc.mu.Unlock()

	var errs []error
	for _, e := range entries {
		if err := e.cache.IndexField(ctx, obj, field, extractValue); err != nil {
			errs = append(errs, fmt.Errorf("namespace %q: %w", e.name, err))
		}
	}
	return errors.Join(errs...)
}

// WaitForCacheSync implements cache.Informers. It returns true once the
// namespace informer and all currently selected namespace caches have synced.
// Namespaces appearing while waiting extend the wait.
func (dc *dynamicCache) WaitForCacheSync(ctx context.Context) bool {
	synced := false
	_ = wait.PollUntilContextCancel(ctx, syncPollInterval, true, func(ctx context.Context) (bool, error) {
		if !dc.nsInformer.HasSynced() {
			return false, nil
		}
		if dc.clusterCache != nil {
			waitCtx, cancel := context.WithTimeout(ctx, subCacheSyncProbeTimeout)
			ok := dc.clusterCache.WaitForCacheSync(waitCtx)
			cancel()
			if !ok {
				return false, nil
			}
		}
		// The store being synced does not imply the ADD handlers have run yet;
		// require an entry for every namespace currently in the store so the
		// per-namespace checks below cannot pass vacuously.
		dc.mu.RLock()
		for _, ns := range dc.nsInformer.GetStore().ListKeys() {
			if _, ok := dc.nsEntries[ns]; !ok {
				dc.mu.RUnlock()
				return false, nil
			}
		}
		entries := slices.Collect(maps.Values(dc.nsEntries))
		dc.mu.RUnlock()
		for _, e := range entries {
			waitCtx, cancel := context.WithTimeout(ctx, subCacheSyncProbeTimeout)
			ok := e.cache.WaitForCacheSync(waitCtx)
			cancel()
			if !ok {
				return false, nil
			}
		}
		synced = true
		return true, nil
	})
	return synced
}

// objectFor builds a prototype object for gvk, falling back to unstructured
// for types not registered in the scheme.
func (dc *dynamicCache) objectFor(gvk schema.GroupVersionKind) (client.Object, error) {
	if dc.scheme.Recognizes(gvk) {
		ro, err := dc.scheme.New(gvk)
		if err != nil {
			return nil, err
		}
		obj, ok := ro.(client.Object)
		if !ok {
			return nil, fmt.Errorf("dynamiccache: %s does not implement client.Object", gvk)
		}
		return obj, nil
	}
	u := newUnstructuredFor(gvk)
	return u, nil
}
