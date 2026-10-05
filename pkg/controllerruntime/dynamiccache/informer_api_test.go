// SPDX-FileCopyrightText: 2026 Deutsche Telekom AG
//
// SPDX-License-Identifier: Apache-2.0

package dynamiccache_test

// cache.Informer / cache.Informers API surface not exercised by the
// behavioral contracts: kind-based lookup, indexers, DoneCheckers, informer
// removal and the error types' messages.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	toolscache "k8s.io/client-go/tools/cache"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"

	dynamiccache "github.com/telekom/t-caas-go-library/pkg/controllerruntime/dynamiccache"
)

var _ = Describe("error types", func() {
	It("render actionable messages and support errors.As", func() {
		gvk := rbacv1.SchemeGroupVersion.WithKind("ClusterRole")
		var err error = &dynamiccache.ErrClusterScopedUnsupported{GVK: gvk}
		var csErr *dynamiccache.ErrClusterScopedUnsupported
		Expect(errors.As(err, &csErr)).To(BeTrue())
		Expect(err.Error()).To(ContainSubstring("ClusterRole"))
		Expect(err.Error()).To(ContainSubstring("DisableFor"))

		err = &dynamiccache.ErrNamespaceNotReady{Namespace: "team-a"}
		var nrErr *dynamiccache.ErrNamespaceNotReady
		Expect(errors.As(err, &nrErr)).To(BeTrue())
		Expect(nrErr.Namespace).To(Equal("team-a"))
		Expect(err.Error()).To(ContainSubstring(`"team-a"`))

		err = &dynamiccache.ErrCacheNotStarted{}
		Expect(err.Error()).To(ContainSubstring("not started"))
	})
})

var _ = Describe("informer API surface", func() {
	waitDone := func(checker toolscache.DoneChecker) {
		GinkgoHelper()
		Expect(checker.Name()).NotTo(BeEmpty())
		Eventually(checker.Done()).Should(BeClosed())
	}

	It("keeps structured, unstructured and metadata events, snapshots and removal independent", func(ctx SpecContext) {
		selVal := uniqueName("representations")
		ns := uniqueName("representations-ns")
		createNamespace(ns, selectedLabels(selVal, nil))
		createConfigMap(ns, "cm", nil)
		dc := newStartedCache(selVal)
		gvk := corev1.SchemeGroupVersion.WithKind("ConfigMap")
		u := &unstructured.Unstructured{}
		u.SetGroupVersionKind(gvk)
		m := &metav1.PartialObjectMetadata{}
		m.SetGroupVersionKind(gvk)
		prototypes := []client.Object{&corev1.ConfigMap{}, u, m}
		informers := make([]cache.Informer, len(prototypes))
		recorders := make([]*eventRecorder, len(prototypes))
		key := ns + "/cm"
		for i, prototype := range prototypes {
			var err error
			informers[i], err = dc.GetInformer(ctx, prototype)
			Expect(err).NotTo(HaveOccurred())
			recorders[i] = newEventRecorder()
			_, err = informers[i].AddEventHandler(recorders[i])
			Expect(err).NotTo(HaveOccurred())
			Eventually(recorders[i].addKeys).Should(ContainElement(key))
			recorders[i].mu.Lock()
			actualType := reflect.TypeOf(recorders[i].adds[0])
			recorders[i].mu.Unlock()
			Expect(actualType).To(Equal(reflect.TypeOf(prototype)))
		}
		Expect(informers[0]).NotTo(BeIdenticalTo(informers[1]))
		Expect(informers[1]).NotTo(BeIdenticalTo(informers[2]))

		unlabelNamespace(ns)
		for i, recorder := range recorders {
			Eventually(recorder.tombstoneKeys).Should(ContainElement(key))
			tombstone, ok := recorder.tombstoneFor(key)
			Expect(ok).To(BeTrue())
			Expect(reflect.TypeOf(tombstone.Obj)).To(Equal(reflect.TypeOf(prototypes[i])))
		}

		labelNamespace(ns, selectedLabelKey, selVal)
		for _, recorder := range recorders {
			Eventually(func() int { return recorder.addCount(key) }).Should(BeNumerically(">=", 2))
		}
		Expect(dc.RemoveInformer(ctx, u)).To(Succeed())
		Expect(informers[1].IsStopped()).To(BeTrue())
		Expect(informers[0].IsStopped()).To(BeFalse())
		Expect(informers[2].IsStopped()).To(BeFalse())
		for _, i := range []int{0, 2} {
			Expect(informers[i].HasSynced()).To(BeTrue())
		}
	})

	It("serves namespaced informers by kind with indexers, checkers and removal", func(ctx SpecContext) {
		selVal := uniqueName("api")
		ns := uniqueName("api-ns")
		createNamespace(ns, selectedLabels(selVal, nil))
		createConfigMap(ns, "cm", map[string]string{"k": "v"})

		dc := newStartedCache(selVal)

		inf, err := dc.GetInformerForKind(ctx, corev1.SchemeGroupVersion.WithKind("ConfigMap"))
		Expect(err).NotTo(HaveOccurred())
		Expect(inf.IsStopped()).To(BeFalse())

		rec := newEventRecorder()
		reg, err := inf.AddEventHandlerWithResyncPeriod(rec, time.Minute)
		Expect(err).NotTo(HaveOccurred())
		Eventually(rec.addKeys).Should(ContainElement(ns + "/cm"))
		waitDone(reg.HasSyncedChecker())
		waitDone(inf.HasSyncedChecker())

		Expect(inf.AddIndexers(toolscache.Indexers{
			"by-data-k": func(obj any) ([]string, error) {
				return []string{obj.(*corev1.ConfigMap).Data["k"]}, nil
			},
		})).To(Succeed())

		Expect(dc.RemoveInformer(ctx, &corev1.ConfigMap{})).To(Succeed())
		Expect(inf.IsStopped()).To(BeTrue())
		Expect(inf.AddIndexers(toolscache.Indexers{
			"late": func(any) ([]string, error) { return nil, nil },
		})).To(MatchError(ContainSubstring("was removed")))
	})

	It("replays the call-time indexer batch even after the caller mutates its map", func(ctx SpecContext) {
		selVal := uniqueName("index-copy")
		ns, nsLate := uniqueName("index-copy-ns"), uniqueName("index-copy-late")
		createNamespace(ns, selectedLabels(selVal, nil))
		createConfigMap(ns, "cm", nil)
		createNamespace(nsLate, nil)
		createConfigMap(nsLate, "cm", nil)
		dc := newStartedCache(selVal)
		inf, err := dc.GetInformer(ctx, &corev1.ConfigMap{})
		Expect(err).NotTo(HaveOccurred())
		Eventually(inf.HasSynced).Should(BeTrue())
		var originalLate, mutatedLate atomic.Bool
		indexers := toolscache.Indexers{
			"by-name": func(obj any) ([]string, error) {
				cm := obj.(*corev1.ConfigMap)
				if cm.Namespace == nsLate {
					originalLate.Store(true)
				}
				return []string{cm.Name}, nil
			},
		}
		Expect(inf.AddIndexers(indexers)).To(Succeed())
		delete(indexers, "by-name")
		indexers["mutated"] = func(obj any) ([]string, error) {
			cm := obj.(*corev1.ConfigMap)
			if cm.Namespace == nsLate {
				mutatedLate.Store(true)
			}
			return []string{cm.Name}, nil
		}
		labelNamespace(nsLate, selectedLabelKey, selVal)
		Eventually(originalLate.Load).Should(BeTrue())
		Eventually(inf.HasSynced).Should(BeTrue())
		Consistently(mutatedLate.Load).Should(BeFalse())
	})

	It("exposes the namespace informer through the full cache.Informer API", func(ctx SpecContext) {
		selVal := uniqueName("api-nsinf")
		ns := uniqueName("api-nsinf-ns")
		createNamespace(ns, selectedLabels(selVal, nil))

		dc := newStartedCache(selVal)

		inf, err := dc.GetInformerForKind(ctx, corev1.SchemeGroupVersion.WithKind("Namespace"))
		Expect(err).NotTo(HaveOccurred())
		Expect(inf.HasSynced()).To(BeTrue())
		Expect(inf.IsStopped()).To(BeFalse())
		waitDone(inf.HasSyncedChecker())

		rec := newEventRecorder()
		reg1, err := inf.AddEventHandlerWithResyncPeriod(rec, time.Minute)
		Expect(err).NotTo(HaveOccurred())
		reg2, err := inf.AddEventHandlerWithOptions(newEventRecorder(), toolscache.HandlerOptions{})
		Expect(err).NotTo(HaveOccurred())
		Eventually(rec.addKeys).Should(ContainElement(ns))
		Expect(inf.RemoveEventHandler(reg1)).To(Succeed())
		Expect(inf.RemoveEventHandler(reg2)).To(Succeed())

		Expect(inf.AddIndexers(toolscache.Indexers{
			"by-name": func(obj any) ([]string, error) {
				return []string{obj.(*corev1.Namespace).Name}, nil
			},
		})).To(Succeed())

		Expect(dc.RemoveInformer(ctx, &corev1.Namespace{})).
			To(MatchError(ContainSubstring("namespace informer cannot be removed")))
	})

	It("routes cluster-scoped kinds, including unregistered ones, to the side cache", func(ctx SpecContext) {
		selVal := uniqueName("api-cluster")
		dc := newStartedCache(selVal)

		crdGVK := schema.GroupVersionKind{Group: "apiextensions.k8s.io", Version: "v1", Kind: "CustomResourceDefinition"}
		inf, err := dc.GetInformerForKind(ctx, crdGVK)
		Expect(err).NotTo(HaveOccurred())
		Expect(inf).NotTo(BeNil())

		Expect(dc.RemoveInformer(ctx, &rbacv1.ClusterRole{})).To(Succeed())
	})

	It("rejects cluster-scoped informer removal when RejectClusterScoped is set", func(ctx SpecContext) {
		dc, err := dynamiccache.New(cfg, cache.Options{}, dynamiccache.Options{
			NamespaceSelector:   newSelector(uniqueName("api-reject")),
			RejectClusterScoped: true,
		})
		Expect(err).NotTo(HaveOccurred())
		startCache(dc)

		var csErr *dynamiccache.ErrClusterScopedUnsupported
		Expect(errors.As(dc.RemoveInformer(ctx, &rbacv1.ClusterRole{}), &csErr)).To(BeTrue())
		_, err = dc.GetInformerForKind(ctx, rbacv1.SchemeGroupVersion.WithKind("ClusterRole"))
		Expect(errors.As(err, &csErr)).To(BeTrue())
	})
})

var _ = Describe("namespace reads", func() {
	It("preserves Namespace representations in reads and add, update and delete events", func(ctx SpecContext) {
		selVal := uniqueName("namespace-types")
		ns := uniqueName("namespace-types-ns")
		createNamespace(ns, selectedLabels(selVal, nil))
		dc := newStartedCache(selVal)
		gvk := corev1.SchemeGroupVersion.WithKind("Namespace")
		u := &unstructured.Unstructured{}
		u.SetGroupVersionKind(gvk)
		m := &metav1.PartialObjectMetadata{}
		m.SetGroupVersionKind(gvk)
		prototypes := []client.Object{&corev1.Namespace{}, u, m}
		recorders := make([]*eventRecorder, len(prototypes))
		for i, prototype := range prototypes {
			Expect(dc.Get(ctx, client.ObjectKey{Name: ns}, prototype)).To(Succeed())
			Expect(prototype.GetLabels()).To(HaveKeyWithValue(selectedLabelKey, selVal))
			inf, err := dc.GetInformer(ctx, prototype)
			Expect(err).NotTo(HaveOccurred())
			var indexed atomic.Bool
			Expect(inf.AddIndexers(toolscache.Indexers{
				"by-name": func(obj any) ([]string, error) {
					if reflect.TypeOf(obj) != reflect.TypeOf(prototype) {
						return nil, fmt.Errorf("indexer expected %T, received %T", prototype, obj)
					}
					indexed.Store(true)
					return []string{obj.(client.Object).GetName()}, nil
				},
			})).To(Succeed())
			Eventually(indexed.Load).Should(BeTrue())
			recorders[i] = newEventRecorder()
			switch i {
			case 0:
				_, err = inf.AddEventHandler(recorders[i])
			case 1:
				_, err = inf.AddEventHandlerWithResyncPeriod(recorders[i], time.Minute)
			case 2:
				_, err = inf.AddEventHandlerWithOptions(recorders[i], toolscache.HandlerOptions{})
			}
			Expect(err).NotTo(HaveOccurred())
			Eventually(func() int {
				recorders[i].mu.Lock()
				defer recorders[i].mu.Unlock()
				return len(recorders[i].adds)
			}).Should(BeNumerically(">", 0))
			recorders[i].mu.Lock()
			actualType := reflect.TypeOf(recorders[i].adds[0])
			recorders[i].mu.Unlock()
			Expect(actualType).To(Equal(reflect.TypeOf(prototype)))
		}

		ml := &metav1.PartialObjectMetadataList{}
		ml.SetGroupVersionKind(corev1.SchemeGroupVersion.WithKind("NamespaceList"))
		Expect(dc.List(ctx, ml, client.MatchingLabels{selectedLabelKey: selVal}, client.Limit(1))).To(Succeed())
		Expect(ml.Items).To(HaveLen(1))
		Expect(ml.Items[0].Name).To(Equal(ns))
		Expect(ml.Items[0].GroupVersionKind()).To(Equal(gvk))
		ml.Items[0].Labels[selectedLabelKey] = "mutated-copy"
		Expect(dc.Get(ctx, client.ObjectKey{Name: ns}, m)).To(Succeed())
		Expect(m.Labels).To(HaveKeyWithValue(selectedLabelKey, selVal))

		labelNamespace(ns, "example.com/updated", "true")
		for i, recorder := range recorders {
			Eventually(func() bool {
				recorder.mu.Lock()
				defer recorder.mu.Unlock()
				for _, obj := range recorder.updates {
					if obj.GetLabels()["example.com/updated"] == "true" {
						return reflect.TypeOf(obj) == reflect.TypeOf(prototypes[i])
					}
				}
				return false
			}).Should(BeTrue())
		}
		unlabelNamespace(ns)
		for i, recorder := range recorders {
			Eventually(func() bool {
				recorder.mu.Lock()
				defer recorder.mu.Unlock()
				for _, obj := range recorder.deletes {
					if reflect.TypeOf(obj) == reflect.TypeOf(prototypes[i]) {
						return true
					}
				}
				return false
			}).Should(BeTrue())
		}
		Expect(dc.List(ctx, ml)).To(Succeed())
		Expect(ml.Items).To(BeEmpty())
	})

	It("rejects continuation tokens on every list path", func(ctx SpecContext) {
		selVal := uniqueName("continue")
		ns := uniqueName("continue-ns")
		createNamespace(ns, selectedLabels(selVal, nil))
		dc := newStartedCache(selVal)
		for _, tc := range []struct {
			list client.ObjectList
			opts []client.ListOption
		}{
			{list: &corev1.NamespaceList{}},
			{list: &rbacv1.ClusterRoleList{}},
			{list: &corev1.ConfigMapList{}},
			{list: &corev1.ConfigMapList{}, opts: []client.ListOption{client.InNamespace(ns)}},
			{list: &corev1.ConfigMapList{}, opts: []client.ListOption{client.InNamespace("unselected")}},
		} {
			opts := append(tc.opts, client.Continue("non-empty-token"))
			Expect(dc.List(ctx, tc.list, opts...)).To(MatchError(ContainSubstring("continuation tokens are not supported")))
		}
	})

	It("marks limited Namespace and merged namespaced lists with the upstream continuation sentinel", func(ctx SpecContext) {
		selVal := uniqueName("limit")
		nsA, nsB := uniqueName("limit-a"), uniqueName("limit-b")
		createNamespace(nsA, selectedLabels(selVal, nil))
		createNamespace(nsB, selectedLabels(selVal, nil))
		createConfigMap(nsA, "cm-a", nil)
		createConfigMap(nsB, "cm-b", nil)
		dc := newStartedCache(selVal)
		cms := &corev1.ConfigMapList{}
		Eventually(func() error { return dc.List(ctx, cms) }).Should(Succeed())
		Expect(cms.Items).To(HaveLen(2))
		Expect(dc.List(ctx, cms, client.Limit(1))).To(Succeed())
		Expect(cms.Items).To(HaveLen(1))
		Expect(cms.GetContinue()).To(Equal("continue-not-supported"))

		u := &unstructured.UnstructuredList{}
		u.SetGroupVersionKind(corev1.SchemeGroupVersion.WithKind("NamespaceList"))
		m := &metav1.PartialObjectMetadataList{}
		m.SetGroupVersionKind(corev1.SchemeGroupVersion.WithKind("NamespaceList"))
		for _, list := range []client.ObjectList{&corev1.NamespaceList{}, u, m} {
			Expect(dc.List(ctx, list, client.Limit(1))).To(Succeed())
			items, err := apimeta.ExtractList(list)
			Expect(err).NotTo(HaveOccurred())
			Expect(items).To(HaveLen(1))
			Expect(list.GetContinue()).To(Equal("continue-not-supported"))
			Expect(dc.List(ctx, list, client.Continue(list.GetContinue()))).
				To(MatchError(ContainSubstring("continuation tokens are not supported")))
		}
	})

	It("serves unstructured Namespace Get/List with label selectors and limits", func(ctx SpecContext) {
		selVal := uniqueName("nsread")
		nsA := uniqueName("nsread-a")
		nsB := uniqueName("nsread-b")
		createNamespace(nsA, selectedLabels(selVal, map[string]string{"tier": "a"}))
		createNamespace(nsB, selectedLabels(selVal, nil))
		dc := newStartedCache(selVal)

		u := &unstructured.Unstructured{}
		u.SetGroupVersionKind(corev1.SchemeGroupVersion.WithKind("Namespace"))
		Eventually(func() error { return dc.Get(ctx, client.ObjectKey{Name: nsA}, u) }).Should(Succeed())
		Expect(u.GetName()).To(Equal(nsA))

		ul := &unstructured.UnstructuredList{}
		ul.SetGroupVersionKind(corev1.SchemeGroupVersion.WithKind("NamespaceList"))
		Expect(dc.List(ctx, ul)).To(Succeed())
		Expect(ul.Items).To(HaveLen(2))

		Expect(dc.List(ctx, ul, client.MatchingLabels{"tier": "a"})).To(Succeed())
		Expect(ul.Items).To(HaveLen(1))

		nsl := &corev1.NamespaceList{}
		Expect(dc.List(ctx, nsl, client.Limit(1))).To(Succeed())
		Expect(nsl.Items).To(HaveLen(1))

		Expect(dc.List(ctx, nsl, client.MatchingFields{"metadata.name": nsA})).
			To(MatchError(ContainSubstring("field selectors are not supported")))
	})
})

// failingStartCache wraps a cache whose Start fails while fail is set.
type failingStartCache struct {
	cache.Cache
	fail *atomic.Bool
}

func (c *failingStartCache) Start(ctx context.Context) error {
	if c.fail.Load() {
		return errors.New("injected start failure")
	}
	return c.Cache.Start(ctx)
}

type pendingRBACTransport struct {
	http.RoundTripper
	pending *atomic.Bool
}

func (t *pendingRBACTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if t.pending.Load() && strings.Contains(req.URL.Path, "/configmaps") {
		return &http.Response{
			StatusCode: http.StatusForbidden,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body: io.NopCloser(strings.NewReader(
				`{"kind":"Status","apiVersion":"v1","status":"Failure","reason":"Forbidden","code":403}`)),
			Request: req,
		}, nil
	}
	return t.RoundTripper.RoundTrip(req)
}

type indexerProbe struct {
	calls   atomic.Int32
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

type blockingIndexerInformer struct {
	cache.Informer
	probe *indexerProbe
}

func (i *blockingIndexerInformer) AddIndexers(indexers toolscache.Indexers) error {
	i.probe.calls.Add(1)
	i.probe.once.Do(func() { close(i.probe.entered) })
	<-i.probe.release
	return i.Informer.AddIndexers(indexers)
}

type blockingIndexerCache struct {
	cache.Cache
	probe *indexerProbe
}

func (c *blockingIndexerCache) GetInformer(
	ctx context.Context, obj client.Object, opts ...cache.InformerGetOption,
) (cache.Informer, error) {
	inf, err := c.Cache.GetInformer(ctx, obj, opts...)
	if err != nil {
		return nil, err
	}
	return &blockingIndexerInformer{Informer: inf, probe: c.probe}, nil
}

var _ = Describe("per-namespace cache failure recovery", func() {
	It("serializes public index additions with namespace resync replay", func(ctx SpecContext) {
		selVal := uniqueName("index-race")
		ns := uniqueName("index-race-ns")
		createNamespace(ns, selectedLabels(selVal, nil))
		createConfigMap(ns, "cm", nil)
		probe := &indexerProbe{entered: make(chan struct{}), release: make(chan struct{})}
		var releaseOnce sync.Once
		release := func() { releaseOnce.Do(func() { close(probe.release) }) }
		DeferCleanup(release)
		newCache := func(config *rest.Config, opts cache.Options) (cache.Cache, error) {
			c, err := cache.New(config, opts)
			if err != nil {
				return nil, err
			}
			if _, perNS := opts.DefaultNamespaces[ns]; perNS {
				return &blockingIndexerCache{Cache: c, probe: probe}, nil
			}
			return c, nil
		}
		dc, err := dynamiccache.NewWithCacheFunc(cfg, cache.Options{Scheme: clientgoscheme.Scheme}, dynamiccache.Options{
			NamespaceSelector: newSelector(selVal), NamespaceResync: testNamespaceResync,
		}, newCache)
		Expect(err).NotTo(HaveOccurred())
		startCache(dc)
		inf, err := dc.GetInformer(ctx, &corev1.ConfigMap{})
		Expect(err).NotTo(HaveOccurred())
		Eventually(inf.HasSynced).Should(BeTrue())
		done := make(chan error, 1)
		go func() {
			done <- inf.AddIndexers(toolscache.Indexers{
				"by-name": func(obj any) ([]string, error) { return []string{obj.(client.Object).GetName()}, nil },
			})
		}()
		Eventually(probe.entered).Should(BeClosed())
		// At least one Namespace resync attempts replay while the public
		// batch application is held inside the underlying informer.
		Consistently(probe.calls.Load).WithTimeout(2 * testNamespaceResync).Should(Equal(int32(1)))
		release()
		Eventually(done).Should(Receive(Succeed()))
		Eventually(inf.HasSynced).Should(BeTrue())
		Expect(probe.calls.Load()).To(Equal(int32(1)))
	})

	It("returns transient read errors without waiting for pending RBAC and recovers when it lands", func(ctx SpecContext) {
		selVal := uniqueName("pending")
		ns := uniqueName("pending-ns")
		createNamespace(ns, selectedLabels(selVal, nil))
		createConfigMap(ns, "cm", nil)
		var pending atomic.Bool
		pending.Store(true)
		newCache := func(config *rest.Config, opts cache.Options) (cache.Cache, error) {
			if _, perNS := opts.DefaultNamespaces[ns]; perNS {
				config = rest.CopyConfig(config)
				config.Wrap(func(base http.RoundTripper) http.RoundTripper {
					return &pendingRBACTransport{RoundTripper: base, pending: &pending}
				})
				opts.HTTPClient = nil
			}
			return cache.New(config, opts)
		}
		dc, err := dynamiccache.NewWithCacheFunc(cfg, cache.Options{Scheme: clientgoscheme.Scheme}, dynamiccache.Options{
			NamespaceSelector: newSelector(selVal), NamespaceResync: testNamespaceResync,
		}, newCache)
		Expect(err).NotTo(HaveOccurred())
		startCache(dc)
		for _, read := range []func(context.Context) error{
			func(ctx context.Context) error {
				return dc.Get(ctx, client.ObjectKey{Namespace: ns, Name: "cm"}, &corev1.ConfigMap{})
			},
			func(ctx context.Context) error {
				return dc.List(ctx, &corev1.ConfigMapList{}, client.InNamespace(ns))
			},
			func(ctx context.Context) error { return dc.List(ctx, &corev1.ConfigMapList{}) },
		} {
			readCtx, cancel := context.WithTimeout(ctx, time.Second)
			start := time.Now()
			err := read(readCtx)
			cancel()
			var notReady *dynamiccache.ErrNamespaceNotReady
			Expect(errors.As(err, &notReady)).To(BeTrue(), "read error: %v", err)
			Expect(time.Since(start)).To(BeNumerically("<", 500*time.Millisecond))
		}
		cancelledCtx, cancel := context.WithCancel(ctx)
		cancel()
		err = dc.Get(cancelledCtx, client.ObjectKey{Namespace: ns, Name: "cm"}, &corev1.ConfigMap{})
		var notReady *dynamiccache.ErrNamespaceNotReady
		Expect(err).To(HaveOccurred())
		Expect(errors.As(err, &notReady)).To(BeFalse(), "caller cancellation is not namespace degradation")
		pending.Store(false)
		Eventually(func() error {
			return dc.Get(ctx, client.ObjectKey{Namespace: ns, Name: "cm"}, &corev1.ConfigMap{})
		}).Should(Succeed())
		list := &corev1.ConfigMapList{}
		Expect(dc.List(ctx, list)).To(Succeed())
		Expect(list.Items).To(HaveLen(1))
	})

	It("preserves ReaderFailOnMissingInformer instead of creating informers during reads", func(ctx SpecContext) {
		selVal := uniqueName("missing")
		ns := uniqueName("missing-ns")
		createNamespace(ns, selectedLabels(selVal, nil))
		dc, err := dynamiccache.New(cfg, cache.Options{
			Scheme: clientgoscheme.Scheme, ReaderFailOnMissingInformer: true,
		}, dynamiccache.Options{NamespaceSelector: newSelector(selVal)})
		Expect(err).NotTo(HaveOccurred())
		startCache(dc)
		var missing *cache.ErrResourceNotCached
		Expect(errors.As(dc.Get(ctx, client.ObjectKey{Namespace: ns, Name: "cm"}, &corev1.ConfigMap{}), &missing)).To(BeTrue())
		Expect(errors.As(dc.List(ctx, &corev1.ConfigMapList{}, client.InNamespace(ns)), &missing)).To(BeTrue())
		Expect(errors.As(dc.List(ctx, &corev1.ConfigMapList{}), &missing)).To(BeTrue())
	})

	It("reports ErrNamespaceNotReady while construction or start fails and recovers on resync", func(ctx SpecContext) {
		selVal := uniqueName("fail")
		nsName := uniqueName("fail-ns")
		createNamespace(nsName, selectedLabels(selVal, nil))
		createConfigMap(nsName, "cm", nil)

		var failConstruct, failStart atomic.Bool
		failConstruct.Store(true)
		failStart.Store(true)
		newCache := func(config *rest.Config, opts cache.Options) (cache.Cache, error) {
			if _, perNS := opts.DefaultNamespaces[nsName]; perNS {
				if failConstruct.Load() {
					return nil, errors.New("injected construction failure")
				}
				c, err := cache.New(config, opts)
				if err != nil {
					return nil, err
				}
				return &failingStartCache{Cache: c, fail: &failStart}, nil
			}
			return cache.New(config, opts)
		}
		dc, err := dynamiccache.NewWithCacheFunc(cfg, cache.Options{Scheme: clientgoscheme.Scheme}, dynamiccache.Options{
			NamespaceSelector: newSelector(selVal),
			NamespaceResync:   testNamespaceResync,
		}, newCache)
		Expect(err).NotTo(HaveOccurred())

		runCtx, cancel := context.WithCancel(context.Background())
		DeferCleanup(cancel)
		go func() {
			defer GinkgoRecover()
			Expect(dc.Start(runCtx)).To(Succeed())
		}()

		key := client.ObjectKey{Namespace: nsName, Name: "cm"}
		var notReady *dynamiccache.ErrNamespaceNotReady
		Eventually(func() bool {
			return errors.As(dc.Get(ctx, key, &corev1.ConfigMap{}), &notReady)
		}).Should(BeTrue())

		// Construction succeeds now, but the cache dies on Start: the entry
		// is torn down and the namespace stays not-ready.
		failConstruct.Store(false)
		Consistently(func() bool {
			return errors.As(dc.Get(ctx, key, &corev1.ConfigMap{}), &notReady)
		}).WithTimeout(3 * testNamespaceResync).Should(BeTrue())

		failStart.Store(false)
		Eventually(func() error { return dc.Get(ctx, key, &corev1.ConfigMap{}) }).Should(Succeed())
	})
})
