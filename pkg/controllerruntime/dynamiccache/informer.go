// SPDX-FileCopyrightText: 2026 Deutsche Telekom AG
//
// SPDX-License-Identifier: Apache-2.0

package dynamiccache

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	toolscache "k8s.io/client-go/tools/cache"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// fanoutInformer presents the per-namespace informers of one GVK as a single
// cache.Informer. Event handlers and indexers registered on it are recorded
// and replayed onto informers of namespaces that start matching the selector
// later; client-go then replays the informer store to the handler as ADD
// events, so late namespaces are observationally identical to initial ones.
//
// addNamespace is an idempotent repair invoked on every namespace event AND
// on every resync of the namespace informer: informer materialization,
// indexer application and handler registration that failed transiently are
// retried until they succeed, and until they do the affected namespace keeps
// HasSynced (and every registration's HasSynced) false so the degradation is
// observable.
type fanoutInformer struct {
	dc  *dynamicCache
	gvk schema.GroupVersionKind
	// prototype is a never-mutated instance of the GVK's Go type used for
	// per-namespace GetInformer calls.
	prototype client.Object

	mu       sync.Mutex
	perNS    map[string]*nsInformerState
	handlers []*fanoutRegistration
	indexers []toolscache.Indexers
	removed  bool
}

// nsInformerState tracks how far a namespace's informer has been wired.
type nsInformerState struct {
	inf cache.Informer
	// indexMu serializes replay and public AddIndexers without holding fi.mu
	// while client-go invokes consumer index functions.
	indexMu sync.Mutex
	// ctx is the owning namespace entry's context; a cancelled ctx marks the
	// state as stale (deselected) so late writers do not resurrect it.
	ctx context.Context
	// appliedIndexers counts how many batches of fi.indexers have been
	// applied to inf (prefix of fi.indexers).
	appliedIndexers int
}

var _ cache.Informer = &fanoutInformer{}

func newFanoutInformer(dc *dynamicCache, gvk schema.GroupVersionKind, obj client.Object) *fanoutInformer {
	return &fanoutInformer{
		dc:        dc,
		gvk:       gvk,
		prototype: obj.DeepCopyObject().(client.Object),
		perNS:     map[string]*nsInformerState{},
	}
}

// listPrototype returns a fresh list object for the fan-out's GVK.
func (fi *fanoutInformer) listPrototype() client.ObjectList {
	listGVK := fi.gvk
	listGVK.Kind += "List"
	switch fi.prototype.(type) {
	case *metav1.PartialObjectMetadata:
		list := &metav1.PartialObjectMetadataList{}
		list.SetGroupVersionKind(listGVK)
		return list
	case *unstructured.Unstructured:
		// Preserve unstructured event objects even for scheme-registered GVKs.
	default:
		if fi.dc.scheme.Recognizes(listGVK) {
			if ro, err := fi.dc.scheme.New(listGVK); err == nil {
				if list, ok := ro.(client.ObjectList); ok {
					return list
				}
			}
		}
	}
	ul := &unstructured.UnstructuredList{}
	ul.SetGroupVersionKind(listGVK)
	return ul
}

// addNamespace materializes the informer for this GVK in the given namespace
// cache and replays recorded indexers and handlers onto it. It is an
// idempotent repair: every step that already succeeded is skipped, every step
// that failed before is retried. ctx is the namespace entry's context; once
// it is cancelled (namespace deselected) no state is committed, which
// guarantees a concurrent dropNamespace cannot be overwritten by a stale
// writer (drop cancels first, then drops).
func (fi *fanoutInformer) addNamespace(ctx context.Context, namespace string, c cache.Cache) {
	if ctx.Err() != nil {
		return
	}
	fi.mu.Lock()
	if fi.removed {
		fi.mu.Unlock()
		return
	}
	st := fi.perNS[namespace]
	fi.mu.Unlock()

	if st == nil {
		// Never block until sync here: this runs on the namespace event
		// handler goroutine and a namespace whose RBAC grant is still pending
		// would wedge the whole cache. Sync is tracked via
		// HasSynced/WaitForCacheSync instead.
		inf, err := c.GetInformer(ctx, fi.prototype.DeepCopyObject().(client.Object), cache.BlockUntilSynced(false))
		if err != nil {
			utilruntime.HandleErrorWithContext(ctx, err,
				"dynamiccache: getting informer failed (namespace stays unsynced, retried on resync)",
				"gvk", fi.gvk.String(), "namespace", namespace)
			return
		}
		fi.mu.Lock()
		if fi.removed || ctx.Err() != nil {
			// Removed, or the namespace was deselected while we were getting
			// the informer — do not resurrect state a drop already cleaned.
			fi.mu.Unlock()
			return
		}
		if existing := fi.perNS[namespace]; existing != nil {
			st = existing
		} else {
			st = &nsInformerState{inf: inf, ctx: ctx}
			fi.perNS[namespace] = st
		}
		fi.mu.Unlock()
	}

	if err := fi.applyIndexers(namespace, st); err != nil {
		utilruntime.HandleErrorWithContext(ctx, err,
			"dynamiccache: adding indexers failed (namespace stays unsynced, retried on resync)",
			"gvk", fi.gvk.String(), "namespace", namespace)
		return
	}

	fi.mu.Lock()
	handlers := slices.Clone(fi.handlers)
	fi.mu.Unlock()
	for _, h := range handlers {
		if err := h.registerOn(ctx, namespace, st.inf); err != nil {
			utilruntime.HandleErrorWithContext(ctx, err,
				"dynamiccache: registering event handler failed (handler stays unsynced, retried on resync)",
				"gvk", fi.gvk.String(), "namespace", namespace)
		}
	}
}

func (fi *fanoutInformer) applyIndexers(namespace string, st *nsInformerState) error {
	st.indexMu.Lock()
	defer st.indexMu.Unlock()
	for {
		fi.mu.Lock()
		if fi.removed || fi.perNS[namespace] != st || st.appliedIndexers >= len(fi.indexers) {
			fi.mu.Unlock()
			return nil
		}
		batch := fi.indexers[st.appliedIndexers]
		fi.mu.Unlock()
		if err := st.inf.AddIndexers(batch); err != nil {
			return err
		}
		fi.mu.Lock()
		if fi.perNS[namespace] == st {
			st.appliedIndexers++
		}
		fi.mu.Unlock()
	}
}

// dropNamespace forgets the informer of a deselected namespace. The informer
// itself is stopped by cancelling the namespace cache's context (which the
// caller does BEFORE calling this; addNamespace relies on that ordering).
func (fi *fanoutInformer) dropNamespace(namespace string) {
	fi.mu.Lock()
	delete(fi.perNS, namespace)
	handlers := slices.Clone(fi.handlers)
	fi.mu.Unlock()
	for _, h := range handlers {
		h.dropNamespace(namespace)
	}
}

// namespaceSnapshotReady reports whether the namespace's informer for this
// GVK exists and has synced — i.e. whether a synthetic-delete snapshot can be
// taken without blocking and can actually contain objects.
func (fi *fanoutInformer) namespaceSnapshotReady(namespace string) bool {
	fi.mu.Lock()
	st := fi.perNS[namespace]
	fi.mu.Unlock()
	return st != nil && st.inf.HasSynced()
}

// deliverSyntheticDeletes invokes OnDelete on every registered handler for
// every object the deselected namespace held, wrapped in
// DeletedFinalStateUnknown to signal that the final object state is a cache
// snapshot, not an observed deletion.
//
// Note: this delivery happens on the namespace event goroutine and may
// interleave with the stopping informer's processor draining its last
// buffered events to the same handler. controller-runtime's workqueue-based
// handlers are safe (a late ADD enqueues a reconcile that observes NotFound);
// stateful custom handlers must tolerate this relaxed ordering.
func (fi *fanoutInformer) deliverSyntheticDeletes(namespace string, objs []client.Object) {
	fi.mu.Lock()
	handlers := slices.Clone(fi.handlers)
	fi.mu.Unlock()

	for _, h := range handlers {
		for _, obj := range objs {
			h.handler.OnDelete(toolscache.DeletedFinalStateUnknown{
				Key: namespace + "/" + obj.GetName(),
				Obj: obj,
			})
		}
	}
}

func (fi *fanoutInformer) markRemoved() {
	fi.mu.Lock()
	fi.removed = true
	fi.perNS = map[string]*nsInformerState{}
	fi.handlers = nil
	fi.mu.Unlock()
}

// AddEventHandler implements cache.Informer.
func (fi *fanoutInformer) AddEventHandler(handler toolscache.ResourceEventHandler) (toolscache.ResourceEventHandlerRegistration, error) {
	return fi.addHandler(handler, toolscache.HandlerOptions{})
}

// AddEventHandlerWithResyncPeriod implements cache.Informer.
func (fi *fanoutInformer) AddEventHandlerWithResyncPeriod(
	handler toolscache.ResourceEventHandler, resyncPeriod time.Duration,
) (toolscache.ResourceEventHandlerRegistration, error) {
	return fi.addHandler(handler, toolscache.HandlerOptions{ResyncPeriod: &resyncPeriod})
}

// AddEventHandlerWithOptions implements cache.Informer.
func (fi *fanoutInformer) AddEventHandlerWithOptions(
	handler toolscache.ResourceEventHandler, options toolscache.HandlerOptions,
) (toolscache.ResourceEventHandlerRegistration, error) {
	return fi.addHandler(handler, options)
}

func (fi *fanoutInformer) addHandler(
	handler toolscache.ResourceEventHandler, options toolscache.HandlerOptions,
) (toolscache.ResourceEventHandlerRegistration, error) {
	reg := &fanoutRegistration{
		fi:      fi,
		handler: handler,
		options: options,
		regs:    map[string]toolscache.ResourceEventHandlerRegistration{},
	}

	fi.mu.Lock()
	if fi.removed {
		fi.mu.Unlock()
		return nil, fmt.Errorf("dynamiccache: informer for %s was removed", fi.gvk)
	}
	fi.handlers = append(fi.handlers, reg)
	perNS := maps.Clone(fi.perNS)
	fi.mu.Unlock()

	// The caller is told the watch is established, so registration failures
	// on this synchronous path must be reported, not swallowed: roll back and
	// return the error.
	var errs []error
	for ns, st := range perNS {
		if err := reg.registerOn(st.ctx, ns, st.inf); err != nil {
			errs = append(errs, fmt.Errorf("namespace %q: %w", ns, err))
		}
	}
	if len(errs) > 0 {
		if removeErr := fi.RemoveEventHandler(reg); removeErr != nil {
			errs = append(errs, fmt.Errorf("rolling back partial registration: %w", removeErr))
		}
		return nil, fmt.Errorf("dynamiccache: adding event handler on %s: %w", fi.gvk, errors.Join(errs...))
	}
	return reg, nil
}

// RemoveEventHandler implements cache.Informer.
func (fi *fanoutInformer) RemoveEventHandler(handle toolscache.ResourceEventHandlerRegistration) error {
	reg, ok := handle.(*fanoutRegistration)
	if !ok {
		return fmt.Errorf("dynamiccache: unexpected registration type %T", handle)
	}

	fi.mu.Lock()
	for i, h := range fi.handlers {
		if h == reg {
			fi.handlers = append(fi.handlers[:i], fi.handlers[i+1:]...)
			break
		}
	}
	perNS := make(map[string]cache.Informer, len(fi.perNS))
	for ns, st := range fi.perNS {
		perNS[ns] = st.inf
	}
	fi.mu.Unlock()

	return reg.remove(perNS)
}

// AddIndexers implements cache.Informer. The indexer batch is recorded for
// future namespaces and applied to every already-wired namespace; failures on
// this synchronous path are returned (lagging namespaces are caught up — and
// failed applications retried — by the resync repair).
func (fi *fanoutInformer) AddIndexers(indexers toolscache.Indexers) error {
	fi.mu.Lock()
	if fi.removed {
		fi.mu.Unlock()
		return fmt.Errorf("dynamiccache: informer for %s was removed", fi.gvk)
	}
	fi.indexers = append(fi.indexers, maps.Clone(indexers))
	type target struct {
		ns string
		st *nsInformerState
	}
	var current []target
	for ns, st := range fi.perNS {
		current = append(current, target{ns: ns, st: st})
	}
	fi.mu.Unlock()

	var errs []error
	for _, t := range current {
		if err := fi.applyIndexers(t.ns, t.st); err != nil {
			errs = append(errs, fmt.Errorf("namespace %q: %w", t.ns, err))
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("dynamiccache: adding indexers on %s: %w", fi.gvk, errors.Join(errs...))
	}
	return nil
}

// HasSynced implements cache.Informer: synced once the namespace informer has
// synced and every currently selected namespace has a fully wired (informer
// materialized, all indexers applied) and synced informer of this GVK.
func (fi *fanoutInformer) HasSynced() bool {
	if !fi.dc.nsInformer.HasSynced() {
		return false
	}
	// Iterate the namespace store, not perNS: the store being synced does not
	// imply the ADD handlers materialized every per-namespace informer yet,
	// and a vacuous pass over a still-empty perNS map must not count as
	// synced. Extra perNS entries for just-deselected namespaces are ignored.
	selected := fi.dc.nsInformer.GetStore().ListKeys()
	fi.mu.Lock()
	total := len(fi.indexers)
	infs := make([]cache.Informer, 0, len(selected))
	for _, ns := range selected {
		st, ok := fi.perNS[ns]
		if !ok || st.appliedIndexers != total {
			fi.mu.Unlock()
			return false
		}
		infs = append(infs, st.inf)
	}
	fi.mu.Unlock()
	for _, inf := range infs {
		if !inf.HasSynced() {
			return false
		}
	}
	return true
}

// HasSyncedChecker implements cache.Informer.
func (fi *fanoutInformer) HasSyncedChecker() toolscache.DoneChecker {
	return newPollChecker(fmt.Sprintf("dynamiccache %s", fi.gvk), fi.HasSynced, fi.dc.doneCh)
}

// IsStopped implements cache.Informer.
func (fi *fanoutInformer) IsStopped() bool {
	fi.mu.Lock()
	removed := fi.removed
	fi.mu.Unlock()
	if removed {
		return true
	}
	fi.dc.mu.RLock()
	defer fi.dc.mu.RUnlock()
	return fi.dc.runCtx != nil && fi.dc.runCtx.Err() != nil
}

// fanoutRegistration is the registration handle for a handler registered on a
// fanoutInformer. It tracks the per-namespace registrations of the handler.
type fanoutRegistration struct {
	fi      *fanoutInformer
	handler toolscache.ResourceEventHandler
	options toolscache.HandlerOptions

	mu      sync.Mutex
	regs    map[string]toolscache.ResourceEventHandlerRegistration
	removed bool
}

var _ toolscache.ResourceEventHandlerRegistration = &fanoutRegistration{}

// registerOn registers the handler on one namespace's informer. Idempotent
// per namespace; a cancelled ctx (namespace deselected mid-flight) discards
// the registration instead of storing a stale handle on a dying informer.
func (r *fanoutRegistration) registerOn(ctx context.Context, namespace string, inf cache.Informer) error {
	r.mu.Lock()
	if r.removed || r.regs[namespace] != nil {
		r.mu.Unlock()
		return nil
	}
	r.mu.Unlock()

	reg, err := inf.AddEventHandlerWithOptions(r.handler, r.options)
	if err != nil {
		return err
	}

	r.mu.Lock()
	stale := r.removed || ctx.Err() != nil || r.regs[namespace] != nil
	if !stale {
		r.regs[namespace] = reg
	}
	r.mu.Unlock()
	if stale {
		// Removed, deselected, or raced with another registration while we
		// were registering; best effort unregister of the extra handle.
		go func() {
			if err := inf.RemoveEventHandler(reg); err != nil {
				utilruntime.HandleErrorWithContext(ctx, err, "dynamiccache: removing stale event handler", "namespace", namespace)
			}
		}()
	}
	return nil
}

func (r *fanoutRegistration) dropNamespace(namespace string) {
	r.mu.Lock()
	delete(r.regs, namespace)
	r.mu.Unlock()
}

func (r *fanoutRegistration) remove(perNS map[string]cache.Informer) error {
	r.mu.Lock()
	r.removed = true
	regs := maps.Clone(r.regs)
	r.regs = map[string]toolscache.ResourceEventHandlerRegistration{}
	r.mu.Unlock()

	var errs []error
	for ns, reg := range regs {
		inf, ok := perNS[ns]
		if !ok {
			continue // namespace already deselected, informer stopped
		}
		if err := inf.RemoveEventHandler(reg); err != nil {
			errs = append(errs, fmt.Errorf("namespace %q: %w", ns, err))
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("dynamiccache: removing event handler: %w", errors.Join(errs...))
	}
	return nil
}

// HasSynced implements toolscache.ResourceEventHandlerRegistration: true once
// the namespace informer has synced and the handler's registration in every
// currently selected namespace has synced.
func (r *fanoutRegistration) HasSynced() bool {
	if !r.fi.dc.nsInformer.HasSynced() {
		return false
	}

	// Every currently selected namespace (from the store, not perNS, to avoid
	// a vacuous pass before the ADD handlers ran) must have a synced
	// registration for this handler.
	namespaces := r.fi.dc.nsInformer.GetStore().ListKeys()

	r.mu.Lock()
	defer r.mu.Unlock()
	for _, ns := range namespaces {
		reg, ok := r.regs[ns]
		if !ok || !reg.HasSynced() {
			return false
		}
	}
	return true
}

// HasSyncedChecker implements toolscache.ResourceEventHandlerRegistration.
func (r *fanoutRegistration) HasSyncedChecker() toolscache.DoneChecker {
	return newPollChecker(fmt.Sprintf("dynamiccache %s handler", r.fi.gvk), r.HasSynced, r.fi.dc.doneCh)
}

// pollChecker adapts a HasSynced-style predicate to toolscache.DoneChecker.
type pollChecker struct {
	name string
	done chan struct{}
}

var _ toolscache.DoneChecker = &pollChecker{}

// checkerPollInterval is the polling cadence of DoneChecker adapters.
const checkerPollInterval = 50 * time.Millisecond

// newPollChecker polls isDone until it returns true (closing the checker's
// channel) or stop is closed (abandoning the poll).
func newPollChecker(name string, isDone func() bool, stop <-chan struct{}) *pollChecker {
	c := &pollChecker{name: name, done: make(chan struct{})}
	go func() {
		ticker := time.NewTicker(checkerPollInterval)
		defer ticker.Stop()
		for {
			if isDone() {
				close(c.done)
				return
			}
			select {
			case <-ticker.C:
			case <-stop:
				return
			}
		}
	}()
	return c
}

// Name implements toolscache.DoneChecker.
func (c *pollChecker) Name() string { return c.name }

// Done implements toolscache.DoneChecker.
func (c *pollChecker) Done() <-chan struct{} { return c.done }

// namespaceInformerAdapter exposes the internal namespace informer as a
// cache.Informer so controllers can watch Namespace objects (e.g. to react to
// label transitions). Only namespaces matching the selector are observed.
type namespaceInformerAdapter struct {
	dc        *dynamicCache
	prototype client.Object
}

var _ cache.Informer = &namespaceInformerAdapter{}

func newNamespaceInformerAdapter(dc *dynamicCache, obj client.Object) *namespaceInformerAdapter {
	return &namespaceInformerAdapter{dc: dc, prototype: obj.DeepCopyObject().(client.Object)}
}

func (a *namespaceInformerAdapter) eventObject(obj any) (any, bool) {
	if tombstone, ok := obj.(toolscache.DeletedFinalStateUnknown); ok {
		converted, ok := a.eventObject(tombstone.Obj)
		tombstone.Obj = converted
		return tombstone, ok
	}
	ns, ok := obj.(*corev1.Namespace)
	if !ok {
		return nil, false
	}
	out := a.prototype.DeepCopyObject().(client.Object)
	if err := assignNamespace(ns, out); err != nil {
		a.dc.mu.RLock()
		ctx := a.dc.runCtx
		a.dc.mu.RUnlock()
		utilruntime.HandleErrorWithContext(ctx, err, "dynamiccache: converting namespace event")
		return nil, false
	}
	return out, true
}

func (a *namespaceInformerAdapter) eventHandler(handler toolscache.ResourceEventHandler) toolscache.ResourceEventHandler {
	return toolscache.ResourceEventHandlerDetailedFuncs{
		AddFunc: func(obj any, initial bool) {
			if converted, ok := a.eventObject(obj); ok {
				handler.OnAdd(converted, initial)
			}
		},
		UpdateFunc: func(oldObj, newObj any) {
			oldConverted, oldOK := a.eventObject(oldObj)
			newConverted, newOK := a.eventObject(newObj)
			if oldOK && newOK {
				handler.OnUpdate(oldConverted, newConverted)
			}
		},
		DeleteFunc: func(obj any) {
			if converted, ok := a.eventObject(obj); ok {
				handler.OnDelete(converted)
			}
		},
	}
}

// AddEventHandler implements cache.Informer.
func (a *namespaceInformerAdapter) AddEventHandler(
	handler toolscache.ResourceEventHandler,
) (toolscache.ResourceEventHandlerRegistration, error) {
	return a.dc.nsInformer.AddEventHandler(a.eventHandler(handler))
}

// AddEventHandlerWithResyncPeriod implements cache.Informer.
func (a *namespaceInformerAdapter) AddEventHandlerWithResyncPeriod(
	handler toolscache.ResourceEventHandler, resyncPeriod time.Duration,
) (toolscache.ResourceEventHandlerRegistration, error) {
	return a.dc.nsInformer.AddEventHandlerWithResyncPeriod(a.eventHandler(handler), resyncPeriod)
}

// AddEventHandlerWithOptions implements cache.Informer.
func (a *namespaceInformerAdapter) AddEventHandlerWithOptions(
	handler toolscache.ResourceEventHandler, options toolscache.HandlerOptions,
) (toolscache.ResourceEventHandlerRegistration, error) {
	return a.dc.nsInformer.AddEventHandlerWithOptions(a.eventHandler(handler), options)
}

// RemoveEventHandler implements cache.Informer.
func (a *namespaceInformerAdapter) RemoveEventHandler(handle toolscache.ResourceEventHandlerRegistration) error {
	return a.dc.nsInformer.RemoveEventHandler(handle)
}

// AddIndexers implements cache.Informer.
func (a *namespaceInformerAdapter) AddIndexers(indexers toolscache.Indexers) error {
	convertedIndexers := make(toolscache.Indexers, len(indexers))
	for name, index := range indexers {
		internalName := fmt.Sprintf("dynamiccache.namespace/%T/%s", a.prototype, name)
		convertedIndexers[internalName] = func(obj any) ([]string, error) {
			converted, ok := a.eventObject(obj)
			if !ok {
				return nil, fmt.Errorf("dynamiccache: converting namespace index object %T", obj)
			}
			return index(converted)
		}
	}
	return a.dc.nsInformer.AddIndexers(convertedIndexers)
}

// HasSynced implements cache.Informer.
func (a *namespaceInformerAdapter) HasSynced() bool {
	return a.dc.nsInformer.HasSynced()
}

// HasSyncedChecker implements cache.Informer.
func (a *namespaceInformerAdapter) HasSyncedChecker() toolscache.DoneChecker {
	return a.dc.nsInformer.HasSyncedChecker()
}

// IsStopped implements cache.Informer.
func (a *namespaceInformerAdapter) IsStopped() bool {
	select {
	case <-a.dc.doneCh:
		return true
	default:
		return false
	}
}

// newUnstructuredFor builds an unstructured prototype for an unregistered GVK.
func newUnstructuredFor(gvk schema.GroupVersionKind) client.Object {
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(gvk)
	return u
}
