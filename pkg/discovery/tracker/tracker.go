// SPDX-FileCopyrightText: 2026 Deutsche Telekom AG
//
// SPDX-License-Identifier: Apache-2.0

package tracker

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/discovery"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

// ErrNotReady means no successful (possibly partial) snapshot has been collected.
var ErrNotReady = errors.New("discovery tracker is not ready")

// Snapshot maps group/version strings to deep-copyable API resources.
// Verbs, short names and categories are sorted before comparison.
type Snapshot map[string][]metav1.APIResource

// DeepCopy isolates all nested resource slices from the original snapshot.
func (s Snapshot) DeepCopy() Snapshot {
	result := make(Snapshot, len(s))
	for gv, resources := range s {
		copied := make([]metav1.APIResource, len(resources))
		for i := range resources {
			copied[i] = *resources[i].DeepCopy()
		}
		result[gv] = copied
	}
	return result
}

// Hooks allows injection of metrics without registering global collectors.
// Hooks run synchronously; they must be quick and must not call Refresh.
type Hooks struct {
	// Collected receives collection duration and discovery errors (including partial errors).
	Collected func(ctx context.Context, duration time.Duration, err error)
	// WatchError receives list/watch establishment and stream errors.
	WatchError func(ctx context.Context, err error)
}

// Options controls lifecycle and optional application-specific augmentation.
// Zero intervals select 5m polling, 30s collection timeout, 100ms event debounce,
// and 1s watch reconnection delay. Negative durations are rejected.
type Options struct {
	Interval   time.Duration
	Timeout    time.Duration
	Debounce   time.Duration
	WatchRetry time.Duration
	Hooks      Hooks
	// OnChange runs on the initial collection and subsequent changes with an
	// isolated snapshot. It must not call Refresh or block indefinitely.
	OnChange func(ctx context.Context, snapshot Snapshot)
	// Transform optionally augments a deep copy of discovery results. It must
	// be idempotent (failed groups retain previously transformed data) and must
	// not call Refresh. There are no synthetic verbs/resources by default.
	Transform func(ctx context.Context, snapshot Snapshot) Snapshot
}

// Tracker implements manager.Runnable and manager.LeaderElectionRunnable.
// Start blocks until cancellation and joins its CRD watch worker before return.
type Tracker struct {
	source  Source
	watcher client.WithWatch
	options Options
	running atomic.Bool
	collect chan struct{}
	mu      sync.RWMutex
	ready   bool
	cache   Snapshot
}

// New creates a tracker using an injected discovery source and optional CRD
// watch client. A nil watcher selects polling-only operation. A watcher must
// have the apiextensions/v1 scheme registered and list/watch CRD permission.
func New(source Source, watcher client.WithWatch, options Options) (*Tracker, error) {
	if source == nil {
		return nil, fmt.Errorf("discovery source is required")
	}
	if options.Interval < 0 || options.Timeout < 0 || options.Debounce < 0 || options.WatchRetry < 0 {
		return nil, fmt.Errorf("tracker durations cannot be negative")
	}
	if options.Interval == 0 {
		options.Interval = 5 * time.Minute
	}
	if options.Timeout == 0 {
		options.Timeout = 30 * time.Second
	}
	if options.Debounce == 0 {
		options.Debounce = 100 * time.Millisecond
	}
	if options.WatchRetry == 0 {
		options.WatchRetry = time.Second
	}
	return &Tracker{source: source, watcher: watcher, options: options, collect: make(chan struct{}, 1)}, nil
}

// NeedLeaderElection returns false: every replica maintains its own snapshot.
func (*Tracker) NeedLeaderElection() bool { return false }

// Snapshot returns a deep copy of the most recently collected snapshot. Failed
// group/versions retain their previous data until a successful discovery.
func (t *Tracker) Snapshot() (Snapshot, error) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	if !t.ready {
		return nil, ErrNotReady
	}
	return t.cache.DeepCopy(), nil
}

// Refresh serializes discovery, updates the cache and notifies on change.
// Partial-discovery errors are returned even though healthy groups are updated.
// Non-partial errors and cancelled collections never replace the cache.
func (t *Tracker) Refresh(ctx context.Context) (bool, error) {
	select {
	case t.collect <- struct{}{}:
		defer func() { <-t.collect }()
	case <-ctx.Done():
		return false, ctx.Err()
	}
	ctx, cancel := context.WithTimeout(ctx, t.options.Timeout)
	defer cancel()
	start := time.Now()
	_, lists, err := t.source.ServerGroupsAndResourcesWithContext(ctx)
	if t.options.Hooks.Collected != nil {
		t.options.Hooks.Collected(ctx, time.Since(start), err)
	}
	if ctx.Err() != nil {
		return false, ctx.Err()
	}
	var partial *discovery.ErrGroupDiscoveryFailed
	if err != nil && !errors.As(err, &partial) {
		return false, fmt.Errorf("collect discovery: %w", err)
	}
	next := make(Snapshot, len(lists))
	for _, list := range lists {
		if list != nil {
			next[list.GroupVersion] = append(next[list.GroupVersion], list.APIResources...)
		}
	}
	t.mu.RLock()
	if partial != nil {
		for gv := range partial.Groups {
			if old, ok := t.cache[gv.String()]; ok {
				next[gv.String()] = old
			}
		}
	}
	next = next.DeepCopy()
	t.mu.RUnlock()
	if t.options.Transform != nil {
		next = t.options.Transform(ctx, next).DeepCopy()
	}
	canonicalize(next)
	t.mu.Lock()
	if err := ctx.Err(); err != nil {
		t.mu.Unlock()
		return false, err
	}
	changed := !t.ready || !reflect.DeepEqual(t.cache, next)
	t.cache, t.ready = next, true
	t.mu.Unlock()
	if changed && t.options.OnChange != nil {
		t.options.OnChange(ctx, next.DeepCopy())
	}
	if err != nil {
		return changed, fmt.Errorf("collect discovery: %w", err)
	}
	return changed, nil
}

func canonicalize(snapshot Snapshot) {
	for _, resources := range snapshot {
		for i := range resources {
			slices.Sort(resources[i].Verbs)
			slices.Sort(resources[i].ShortNames)
			slices.Sort(resources[i].Categories)
		}
		slices.SortFunc(resources, func(a, b metav1.APIResource) int {
			return cmp.Compare(a.Name, b.Name)
		})
	}
}

// Start runs periodic collection and debounced CRD-triggered refreshes. Discovery
// and watch failures are logged, not fatal: periodic refreshes self-heal.
// Debouncing keeps a trailing-edge refresh instead of dropping burst events.
func (t *Tracker) Start(ctx context.Context) error {
	if !t.running.CompareAndSwap(false, true) {
		return fmt.Errorf("discovery tracker is already running")
	}
	defer t.running.Store(false)
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	events := make(chan struct{}, 1)
	var workers sync.WaitGroup
	if t.watcher != nil {
		workers.Go(func() { t.watchLoop(ctx, events) })
	}
	defer func() { cancel(); workers.Wait() }()
	t.refreshAndLog(ctx)
	ticker := time.NewTicker(t.options.Interval)
	defer ticker.Stop()
	var pending <-chan time.Time
	timer := time.NewTimer(time.Hour)
	timer.Stop()
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			t.refreshAndLog(ctx)
		case <-events:
			timer.Reset(t.options.Debounce)
			pending = timer.C
		case <-pending:
			pending = nil
			t.refreshAndLog(ctx)
		}
	}
}

func (t *Tracker) refreshAndLog(ctx context.Context) {
	if _, err := t.Refresh(ctx); err != nil && ctx.Err() == nil {
		log.FromContext(ctx).Error(err, "discovery collection failed")
	}
}

func (t *Tracker) watchLoop(ctx context.Context, events chan<- struct{}) {
	for ctx.Err() == nil {
		err := t.watchOnce(ctx, events)
		if err != nil && ctx.Err() == nil {
			log.FromContext(ctx).Error(err, "CRD watch failed")
			if t.options.Hooks.WatchError != nil {
				t.options.Hooks.WatchError(ctx, err)
			}
		}
		timer := time.NewTimer(t.options.WatchRetry)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

func (t *Tracker) watchOnce(ctx context.Context, events chan<- struct{}) error {
	listCtx, cancel := context.WithTimeout(ctx, t.options.Timeout)
	var crds apiextensionsv1.CustomResourceDefinitionList
	err := t.watcher.List(listCtx, &crds)
	cancel()
	if err != nil {
		return fmt.Errorf("list CRDs: %w", err)
	}
	stream, err := t.watcher.Watch(ctx, &apiextensionsv1.CustomResourceDefinitionList{},
		&client.ListOptions{Raw: &metav1.ListOptions{ResourceVersion: crds.ResourceVersion}})
	if err != nil {
		return fmt.Errorf("watch CRDs: %w", err)
	}
	defer stream.Stop()
	// Collect after watch establishment to close the list/discovery startup gap.
	signal(events)
	for {
		select {
		case <-ctx.Done():
			return nil
		case event, ok := <-stream.ResultChan():
			if !ok {
				return fmt.Errorf("CRD watch closed")
			}
			switch event.Type {
			case watch.Error:
				return fmt.Errorf("CRD watch error: %w", apierrors.FromObject(event.Object))
			case watch.Added, watch.Modified, watch.Deleted:
				signal(events)
			}
		}
	}
}

func signal(events chan<- struct{}) {
	select {
	case events <- struct{}{}:
	default:
	}
}
