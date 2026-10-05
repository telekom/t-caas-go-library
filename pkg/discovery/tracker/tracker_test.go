// SPDX-FileCopyrightText: 2026 Deutsche Telekom AG
//
// SPDX-License-Identifier: Apache-2.0

package tracker_test

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/discovery"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/telekom/t-caas-go-library/pkg/discovery/tracker"
)

type sourceFunc func(context.Context) ([]*metav1.APIResourceList, error)

func (f sourceFunc) ServerGroupsAndResourcesWithContext(ctx context.Context) ([]*metav1.APIGroup, []*metav1.APIResourceList, error) {
	lists, err := f(ctx)
	return nil, lists, err
}

func lists() []*metav1.APIResourceList {
	return []*metav1.APIResourceList{
		{GroupVersion: "v1", APIResources: []metav1.APIResource{
			{Name: "secrets", Kind: "Secret", Namespaced: true, Verbs: []string{"watch", "get"},
				ShortNames: []string{"sec", "s"}, Categories: []string{"two", "one"}},
			{Name: "namespaces", Kind: "Namespace", Verbs: []string{"get"}},
		}},
		{GroupVersion: "example.org/v1", APIResources: []metav1.APIResource{{Name: "people", Kind: "Person"}}},
	}
}

func TestRefreshAndDeepCopies(t *testing.T) {
	input := lists()
	var sourceErr error
	var callbacks, metrics int
	observer := func(_ context.Context, snapshot tracker.Snapshot) {
		callbacks++
		for _, resources := range snapshot {
			for i := range resources {
				resources[i].Name = "mutated callback"
				if len(resources[i].Verbs) > 0 {
					resources[i].Verbs[0] = "mutated callback"
				}
			}
		}
	}
	instance, _ := tracker.New(sourceFunc(func(context.Context) ([]*metav1.APIResourceList, error) {
		return input, sourceErr
	}), nil, tracker.Options{OnChange: observer, Hooks: tracker.Hooks{
		Collected: func(context.Context, time.Duration, error) { metrics++ },
	}})
	if instance.NeedLeaderElection() {
		t.Fatal("local cache requires all replicas")
	}
	if _, err := instance.Snapshot(); !errors.Is(err, tracker.ErrNotReady) {
		t.Fatal(err)
	}
	ctx := context.Background()
	changed, err := instance.Refresh(ctx)
	if err != nil || !changed || callbacks != 1 || metrics != 1 {
		t.Fatalf("%v %v %d %d", changed, err, callbacks, metrics)
	}
	snapshot, _ := instance.Snapshot()
	if snapshot["v1"][0].Name != "namespaces" || snapshot["v1"][1].Verbs[0] != "get" {
		t.Fatal("callback corrupted cache or sort incorrect")
	}
	snapshot["v1"][1].Verbs[0] = "corrupt"
	snapshot["v1"][1].ShortNames[0] = "corrupt"
	snapshot["v1"][1].Categories[0] = "corrupt"
	again, _ := instance.Snapshot()
	if again["v1"][1].Verbs[0] != "get" || again["v1"][1].ShortNames[0] != "s" ||
		again["v1"][1].Categories[0] != "one" {
		t.Fatal("Snapshot is a shallow copy")
	}
	input[0].APIResources[0].Verbs[0] = "get"
	input[0].APIResources[0].Verbs[1] = "watch"
	changed, err = instance.Refresh(ctx)
	if err != nil || changed || callbacks != 1 {
		t.Fatal("order-only change notified")
	}
	input[0].APIResources[0].SingularName = "secret"
	if changed, err := instance.Refresh(ctx); err != nil || !changed {
		t.Fatal("resource field change not detected")
	}
}

func TestPartialDiscovery(t *testing.T) {
	input := lists()
	var sourceErr error
	instance, err := tracker.New(sourceFunc(func(context.Context) ([]*metav1.APIResourceList, error) {
		return input, sourceErr
	}), nil, tracker.Options{})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := instance.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	sourceErr = &discovery.ErrGroupDiscoveryFailed{Groups: map[schema.GroupVersion]error{
		{Group: "example.org", Version: "v1"}: errors.New("aggregated API unavailable"),
	}}
	input = []*metav1.APIResourceList{{GroupVersion: "v1", APIResources: []metav1.APIResource{{Name: "configmaps"}}}, nil}
	var partialErr *discovery.ErrGroupDiscoveryFailed
	if changed, err := instance.Refresh(ctx); !errors.As(err, &partialErr) || !changed {
		t.Fatalf("partial collection not applied or error chain lost: %v", err)
	}
	partial, _ := instance.Snapshot()
	if partial["example.org/v1"][0].Name != "people" || partial["v1"][0].Name != "configmaps" {
		t.Fatal("partial collection discarded failed group or healthy update")
	}
	sourceErr = errors.New("total outage")
	input = nil
	if changed, err := instance.Refresh(ctx); err == nil || changed {
		t.Fatal("total outage replaced cache")
	}
	previous, _ := instance.Snapshot()
	if len(previous) != 2 {
		t.Fatal("cache discarded on failure")
	}
	sourceErr = nil
	if changed, err := instance.Refresh(ctx); err != nil || !changed {
		t.Fatal("removed resources not detected")
	}
	empty, _ := instance.Snapshot()
	if len(empty) != 0 {
		t.Fatal("removed groups retained after healthy discovery")
	}
}

func TestOptionsAndTransform(t *testing.T) {
	source := sourceFunc(func(context.Context) ([]*metav1.APIResourceList, error) { return lists(), nil })
	if _, err := tracker.New(nil, nil, tracker.Options{}); err == nil {
		t.Fatal("nil source accepted")
	}
	for _, options := range []tracker.Options{{Interval: -1}, {Timeout: -1}, {Debounce: -1}, {WatchRetry: -1}} {
		if _, err := tracker.New(source, nil, options); err == nil {
			t.Fatal("negative duration accepted")
		}
	}
	instance, _ := tracker.New(source, nil, tracker.Options{
		Transform: func(_ context.Context, snapshot tracker.Snapshot) tracker.Snapshot {
			snapshot["v1"] = append(snapshot["v1"], metav1.APIResource{Name: "synthetic", Verbs: []string{"custom"}})
			return snapshot
		},
	})
	if _, err := instance.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	snapshot, _ := instance.Snapshot()
	if len(snapshot["v1"]) != 3 {
		t.Fatal("transform was ignored")
	}
}

func TestRefreshCancellation(t *testing.T) {
	entered := make(chan struct{}, 1)
	source := sourceFunc(func(ctx context.Context) ([]*metav1.APIResourceList, error) {
		entered <- struct{}{}
		<-ctx.Done()
		return nil, ctx.Err()
	})
	instance, _ := tracker.New(source, nil, tracker.Options{Timeout: 30 * time.Millisecond})
	done := make(chan error, 1)
	go func() {
		_, err := instance.Refresh(context.Background())
		done <- err
	}()
	<-entered
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := instance.Refresh(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if err := <-done; !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	if _, err := instance.Snapshot(); !errors.Is(err, tracker.ErrNotReady) {
		t.Fatal("cancelled collection published")
	}
}

func TestTransformCancellationPreservesSnapshot(t *testing.T) {
	var cancelTransform bool
	var callbacks int
	instance, err := tracker.New(sourceFunc(func(context.Context) ([]*metav1.APIResourceList, error) {
		return lists(), nil
	}), nil, tracker.Options{
		Timeout: 20 * time.Millisecond,
		Transform: func(ctx context.Context, snapshot tracker.Snapshot) tracker.Snapshot {
			if cancelTransform {
				<-ctx.Done()
				return tracker.Snapshot{}
			}
			return snapshot
		},
		OnChange: func(context.Context, tracker.Snapshot) { callbacks++ },
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := instance.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	cancelTransform = true
	if changed, err := instance.Refresh(context.Background()); changed || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("cancelled transformation: changed=%t err=%v", changed, err)
	}
	snapshot, err := instance.Snapshot()
	if err != nil || len(snapshot) != 2 || callbacks != 1 {
		t.Fatalf("cancelled transformation replaced snapshot or notified: %v %v %d", snapshot, err, callbacks)
	}
}

func TestStartPeriodic(t *testing.T) {
	var calls atomic.Int32
	changed := make(chan struct{}, 10)
	instance, _ := tracker.New(sourceFunc(func(context.Context) ([]*metav1.APIResourceList, error) {
		calls.Add(1)
		return lists(), nil
	}), nil, tracker.Options{Interval: 5 * time.Millisecond,
		OnChange: func(context.Context, tracker.Snapshot) { changed <- struct{}{} },
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- instance.Start(ctx) }()
	select {
	case <-changed:
	case <-time.After(time.Second):
		t.Fatal("no initial collection")
	}
	if err := instance.Start(ctx); err == nil {
		t.Fatal("concurrent Start allowed")
	}
	deadline := time.After(time.Second)
	for calls.Load() < 3 {
		select {
		case <-time.After(time.Millisecond):
		case <-deadline:
			t.Fatal("no periodic collection")
		}
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestWatchReconnectAndDebounce(t *testing.T) {
	scheme := runtime.NewScheme()
	apiextensionsv1.AddToScheme(scheme)
	var attempts, failures atomic.Int32
	streams := make(chan *watch.RaceFreeFakeWatcher, 10)
	watcher := fake.NewClientBuilder().WithScheme(scheme).WithInterceptorFuncs(interceptor.Funcs{
		List: func(_ context.Context, _ client.WithWatch, _ client.ObjectList, _ ...client.ListOption) error {
			if attempts.Add(1) == 1 {
				return errors.New("list denied")
			}
			return nil
		},
		Watch: func(_ context.Context, _ client.WithWatch, _ client.ObjectList, _ ...client.ListOption) (watch.Interface, error) {
			if attempts.Load() == 2 {
				return nil, errors.New("watch unavailable")
			}
			stream := watch.NewRaceFreeFake()
			streams <- stream
			return stream, nil
		},
	}).Build()
	var collections atomic.Int32
	notifications := make(chan struct{}, 100)
	instance, _ := tracker.New(sourceFunc(func(context.Context) ([]*metav1.APIResourceList, error) {
		n := collections.Add(1)
		return []*metav1.APIResourceList{{GroupVersion: "v1",
			APIResources: []metav1.APIResource{{Name: fmt.Sprintf("resource-%d", n)}}}}, nil
	}), watcher, tracker.Options{
		Interval: time.Hour, WatchRetry: time.Millisecond, Debounce: 5 * time.Millisecond,
		OnChange: func(context.Context, tracker.Snapshot) { notifications <- struct{}{} },
		Hooks:    tracker.Hooks{WatchError: func(context.Context, error) { failures.Add(1) }},
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- instance.Start(ctx) }()
	getStream := func() *watch.RaceFreeFakeWatcher {
		t.Helper()
		select {
		case stream := <-streams:
			return stream
		case <-ctx.Done():
			t.Fatal("watch not established")
			return nil
		}
	}
	stream := getStream()
	// Establishment signals a collection after the list/watch startup gap.
	for range 2 {
		select {
		case <-notifications:
		case <-ctx.Done():
			t.Fatal("startup refresh missing")
		}
	}
	before := collections.Load()
	stream.Action(watch.Bookmark, &metav1.PartialObjectMetadata{})
	for range 10 {
		stream.Add(&apiextensionsv1.CustomResourceDefinition{})
		stream.Modify(&apiextensionsv1.CustomResourceDefinition{})
		stream.Delete(&apiextensionsv1.CustomResourceDefinition{})
	}
	select {
	case <-notifications:
	case <-ctx.Done():
		t.Fatal("burst's trailing refresh was lost")
	}
	if collections.Load() != before+1 {
		t.Fatalf("burst was not coalesced: %d -> %d", before, collections.Load())
	}
	stream.Error(&metav1.Status{Message: "expired"})
	stream = getStream()
	stream.Stop() // a closed stream must reconnect, too.
	stream = getStream()
	if failures.Load() < 4 {
		t.Fatal("watch/list failures not observed")
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if !stream.IsStopped() {
		t.Fatal("active stream was not stopped")
	}
}

func ExampleTracker_Snapshot() {
	source := sourceFunc(func(context.Context) ([]*metav1.APIResourceList, error) {
		return []*metav1.APIResourceList{{GroupVersion: "v1", APIResources: []metav1.APIResource{{Name: "pods"}}}}, nil
	})
	instance, _ := tracker.New(source, nil, tracker.Options{})
	instance.Refresh(context.Background())
	snapshot, _ := instance.Snapshot()
	fmt.Println(snapshot["v1"][0].Name)
	// Output: pods
}
