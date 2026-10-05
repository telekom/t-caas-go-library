// SPDX-FileCopyrightText: 2026 Deutsche Telekom AG
//
// SPDX-License-Identifier: Apache-2.0

package dynamiccache_test

// Behavior specs against a real envtest API server, one contract item per It.
// Every spec uses its own selector label VALUE and uniquely named namespaces so
// specs stay independent under `ginkgo run --randomize-all --race -p`.

import (
	"context"
	"errors"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"

	dynamiccache "github.com/telekom/t-caas-go-library/pkg/controllerruntime/dynamiccache"
)

var _ = Describe("dynamic cache", func() {
	var ctx context.Context

	BeforeEach(func() {
		ctx = context.Background()
	})

	// Contract 2: initial selection.
	It("serves only objects from namespaces matching the selector", func() {
		selVal := uniqueName("sel")
		nsA := uniqueName("init-a")
		nsB := uniqueName("init-b")
		nsOther := uniqueName("init-other")
		createNamespace(nsA, selectedLabels(selVal, nil))
		createNamespace(nsB, selectedLabels(selVal, nil))
		createNamespace(nsOther, nil)
		createConfigMap(nsA, "cm-a1", map[string]string{"k": "a1"})
		createConfigMap(nsA, "cm-a2", map[string]string{"k": "a2"})
		createConfigMap(nsB, "cm-b1", map[string]string{"k": "b1"})
		createConfigMap(nsOther, "cm-o1", map[string]string{"k": "o1"})

		dc := newStartedCache(selVal)

		By("Get succeeds in selected namespaces")
		cm := &corev1.ConfigMap{}
		Eventually(func() error {
			return dc.Get(ctx, client.ObjectKey{Namespace: nsA, Name: "cm-a1"}, cm)
		}).Should(Succeed())
		Expect(cm.Data).To(HaveKeyWithValue("k", "a1"))
		Eventually(func() error {
			return dc.Get(ctx, client.ObjectKey{Namespace: nsB, Name: "cm-b1"}, &corev1.ConfigMap{})
		}).Should(Succeed())

		By("Get in an unselected namespace is NotFound")
		err := dc.Get(ctx, client.ObjectKey{Namespace: nsOther, Name: "cm-o1"}, &corev1.ConfigMap{})
		Expect(apierrors.IsNotFound(err)).To(BeTrue(), "expected NotFound, got: %v", err)

		By("Get of a namespaced object without a namespace errors")
		Expect(dc.Get(ctx, client.ObjectKey{Name: "cm-a1"}, &corev1.ConfigMap{})).
			To(MatchError(ContainSubstring("without a namespace")))

		By("cross-namespace List merges selected namespaces in namespace order")
		var items []corev1.ConfigMap
		Eventually(func(g Gomega) {
			list := &corev1.ConfigMapList{}
			g.Expect(dc.List(ctx, list)).To(Succeed())
			g.Expect(list.Items).To(HaveLen(3))
			items = list.Items
		}).Should(Succeed())
		namespaces := []string{items[0].Namespace, items[1].Namespace, items[2].Namespace}
		Expect(namespaces).To(Equal([]string{nsA, nsA, nsB}), "items must be grouped by namespace in sorted namespace order")

		By("List scoped to an unselected namespace is empty without error")
		scoped := &corev1.ConfigMapList{}
		Expect(dc.List(ctx, scoped, client.InNamespace(nsOther))).To(Succeed())
		Expect(scoped.Items).To(BeEmpty())
	})

	// Contract 3: late namespace selection replays existing objects as ADDs.
	It("starts serving a namespace that becomes selected after handlers were registered", func() {
		selVal := uniqueName("sel")
		nsLate := uniqueName("late")
		createNamespace(nsLate, nil)
		createConfigMap(nsLate, "pre-existing", map[string]string{"k": "v"})

		dc := newStartedCache(selVal)

		inf, err := dc.GetInformer(ctx, &corev1.ConfigMap{})
		Expect(err).NotTo(HaveOccurred())
		rec := newEventRecorder()
		_, err = inf.AddEventHandler(rec)
		Expect(err).NotTo(HaveOccurred())

		By("labeling the namespace after handler registration")
		labelNamespace(nsLate, selectedLabelKey, selVal)

		key := nsLate + "/pre-existing"
		By("the handler receives ADD events for pre-existing objects")
		Eventually(rec.addKeys).Should(ContainElement(key))

		By("Get starts succeeding")
		got := &corev1.ConfigMap{}
		Eventually(func() error {
			return dc.Get(ctx, client.ObjectKey{Namespace: nsLate, Name: "pre-existing"}, got)
		}).Should(Succeed())
		Expect(got.Data).To(HaveKeyWithValue("k", "v"))
	})

	// Contract 4: deselection delivers synthetic deletes and hides the objects.
	It("delivers synthetic DeletedFinalStateUnknown events when a namespace is deselected", func() {
		selVal := uniqueName("sel")
		ns := uniqueName("desel")
		createNamespace(ns, selectedLabels(selVal, nil))
		createConfigMap(ns, "victim", map[string]string{"k": "v"})

		dc := newStartedCache(selVal)

		inf, err := dc.GetInformer(ctx, &corev1.ConfigMap{})
		Expect(err).NotTo(HaveOccurred())
		rec := newEventRecorder()
		_, err = inf.AddEventHandler(rec)
		Expect(err).NotTo(HaveOccurred())

		key := ns + "/victim"
		Eventually(rec.addKeys).Should(ContainElement(key))

		By("removing the selecting label")
		unlabelNamespace(ns)

		By("the handler receives a tombstone carrying the last object state")
		Eventually(rec.tombstoneKeys).Should(ContainElement(key))
		tomb, ok := rec.tombstoneFor(key)
		Expect(ok).To(BeTrue())
		lastState, ok := tomb.Obj.(*corev1.ConfigMap)
		Expect(ok).To(BeTrue(), "tombstone.Obj should be a *corev1.ConfigMap, got %T", tomb.Obj)
		Expect(lastState.Name).To(Equal("victim"))
		Expect(lastState.Namespace).To(Equal(ns))
		Expect(lastState.Data).To(HaveKeyWithValue("k", "v"))

		By("Get now reports NotFound")
		Eventually(func() bool {
			err := dc.Get(ctx, client.ObjectKey{Namespace: ns, Name: "victim"}, &corev1.ConfigMap{})
			return apierrors.IsNotFound(err)
		}).Should(BeTrue())

		By("cross-namespace List no longer includes the namespace's objects")
		Eventually(func(g Gomega) {
			list := &corev1.ConfigMapList{}
			g.Expect(dc.List(ctx, list)).To(Succeed())
			g.Expect(list.Items).To(BeEmpty())
		}).Should(Succeed())
	})

	// Contract 5: re-selection resumes ADD delivery and reads.
	It("resumes delivery and reads when a namespace is re-selected", func() {
		selVal := uniqueName("sel")
		ns := uniqueName("resel")
		createNamespace(ns, selectedLabels(selVal, nil))
		createConfigMap(ns, "phoenix", map[string]string{"k": "v"})

		dc := newStartedCache(selVal)

		inf, err := dc.GetInformer(ctx, &corev1.ConfigMap{})
		Expect(err).NotTo(HaveOccurred())
		rec := newEventRecorder()
		_, err = inf.AddEventHandler(rec)
		Expect(err).NotTo(HaveOccurred())

		key := ns + "/phoenix"
		Eventually(func() int { return rec.addCount(key) }).Should(BeNumerically(">=", 1))

		By("deselecting the namespace")
		unlabelNamespace(ns)
		Eventually(rec.tombstoneKeys).Should(ContainElement(key))
		Eventually(func() bool {
			err := dc.Get(ctx, client.ObjectKey{Namespace: ns, Name: "phoenix"}, &corev1.ConfigMap{})
			return apierrors.IsNotFound(err)
		}).Should(BeTrue())

		By("re-selecting the namespace")
		labelNamespace(ns, selectedLabelKey, selVal)

		By("ADDs are delivered again")
		Eventually(func() int { return rec.addCount(key) }).Should(BeNumerically(">=", 2))

		By("reads work again")
		got := &corev1.ConfigMap{}
		Eventually(func() error {
			return dc.Get(ctx, client.ObjectKey{Namespace: ns, Name: "phoenix"}, got)
		}).Should(Succeed())
		Expect(got.Data).To(HaveKeyWithValue("k", "v"))
	})

	// Contract 6: Namespace reads and watches served from the internal informer.
	It("serves Namespace Get/List/Informer from the internal namespace informer", func() {
		selVal := uniqueName("sel")
		ns1 := uniqueName("nsr-a")
		ns2 := uniqueName("nsr-b")
		nsOther := uniqueName("nsr-o")
		createNamespace(ns1, selectedLabels(selVal, map[string]string{"tier": "gold"}))
		createNamespace(ns2, selectedLabels(selVal, nil))
		createNamespace(nsOther, nil)

		dc := newStartedCache(selVal)

		By("Get of a selected namespace succeeds")
		got := &corev1.Namespace{}
		Eventually(func() error {
			return dc.Get(ctx, client.ObjectKey{Name: ns1}, got)
		}).Should(Succeed())
		Expect(got.Labels).To(HaveKeyWithValue("tier", "gold"))

		By("Get of an unselected namespace is NotFound")
		err := dc.Get(ctx, client.ObjectKey{Name: nsOther}, &corev1.Namespace{})
		Expect(apierrors.IsNotFound(err)).To(BeTrue(), "expected NotFound, got: %v", err)
		err = dc.Get(ctx, client.ObjectKey{Name: "default"}, &corev1.Namespace{})
		Expect(apierrors.IsNotFound(err)).To(BeTrue(), "expected NotFound for envtest default namespace, got: %v", err)

		By("List returns only matching namespaces, sorted by name")
		Eventually(func(g Gomega) {
			nsList := &corev1.NamespaceList{}
			g.Expect(dc.List(ctx, nsList)).To(Succeed())
			names := make([]string, 0, len(nsList.Items))
			for _, item := range nsList.Items {
				names = append(names, item.Name)
			}
			g.Expect(names).To(Equal([]string{ns1, ns2}))
		}).Should(Succeed())

		By("List with an additional label selector filters further")
		filtered := &corev1.NamespaceList{}
		Expect(dc.List(ctx, filtered, client.MatchingLabels{"tier": "gold"})).To(Succeed())
		Expect(filtered.Items).To(HaveLen(1))
		Expect(filtered.Items[0].Name).To(Equal(ns1))

		By("GetInformer for Namespace works and delivers events")
		inf, err := dc.GetInformer(ctx, &corev1.Namespace{})
		Expect(err).NotTo(HaveOccurred())
		rec := newEventRecorder()
		_, err = inf.AddEventHandler(rec)
		Expect(err).NotTo(HaveOccurred())
		Eventually(rec.addKeys).Should(ContainElements(ns1, ns2))

		labelNamespace(ns2, "tier", "silver")
		Eventually(func() bool {
			for _, o := range rec.updateObjects() {
				if o.GetName() == ns2 && o.GetLabels()["tier"] == "silver" {
					return true
				}
			}
			return false
		}).Should(BeTrue(), "expected an Update event carrying the new namespace labels")
	})

	// Reads before Start return *ErrCacheNotStarted.
	It("rejects reads before Start with ErrCacheNotStarted", func() {
		dc := newUnstartedCache(uniqueName("sel"))

		var notStarted *dynamiccache.ErrCacheNotStarted
		err := dc.Get(ctx, client.ObjectKey{Namespace: "default", Name: "cm"}, &corev1.ConfigMap{})
		Expect(errors.As(err, &notStarted)).To(BeTrue(), "expected ErrCacheNotStarted from Get, got: %v", err)

		notStarted = nil
		err = dc.List(ctx, &corev1.ConfigMapList{})
		Expect(errors.As(err, &notStarted)).To(BeTrue(), "expected ErrCacheNotStarted from List, got: %v", err)
	})

	// Contract 7a: mixed scopes — cluster-scoped types are served by the
	// cluster-scoped side cache alongside the namespaced fan-out.
	It("serves cluster-scoped types alongside namespaced ones (mixed scopes)", func() {
		selVal := uniqueName("sel")
		nsName := uniqueName("mixed")
		createNamespace(nsName, selectedLabels(selVal, nil))
		createConfigMap(nsName, "mixed-cm", map[string]string{"k": "v"})

		crName := uniqueName("dynamiccache-mixed")
		cr := &rbacv1.ClusterRole{ObjectMeta: metav1.ObjectMeta{Name: crName}}
		Expect(k8sClient.Create(ctx, cr)).To(Succeed())
		DeferCleanup(func() {
			Expect(client.IgnoreNotFound(k8sClient.Delete(context.Background(), cr))).To(Succeed())
		})

		dc := newStartedCache(selVal)

		By("watching cluster-scoped objects")
		inf, err := dc.GetInformer(ctx, &rbacv1.ClusterRole{})
		Expect(err).NotTo(HaveOccurred())
		rec := newEventRecorder()
		_, err = inf.AddEventHandler(rec)
		Expect(err).NotTo(HaveOccurred())
		Eventually(rec.addKeys).Should(ContainElement(crName))

		By("reading cluster-scoped objects")
		Eventually(func() error {
			return dc.Get(ctx, client.ObjectKey{Name: crName}, &rbacv1.ClusterRole{})
		}).Should(Succeed())
		list := &rbacv1.ClusterRoleList{}
		Expect(dc.List(ctx, list)).To(Succeed())
		names := make([]string, 0, len(list.Items))
		for i := range list.Items {
			names = append(names, list.Items[i].Name)
		}
		Expect(names).To(ContainElement(crName))

		By("still serving namespaced objects from the selected namespace")
		Eventually(func() error {
			return dc.Get(ctx, client.ObjectKey{Namespace: nsName, Name: "mixed-cm"}, &corev1.ConfigMap{})
		}).Should(Succeed())
	})

	// Contract 7b: RejectClusterScoped fails fast for cluster-scoped types.
	It("rejects cluster-scoped types with ErrClusterScopedUnsupported when RejectClusterScoped is set", func() {
		dc, err := dynamiccache.New(cfg, cache.Options{Scheme: clientgoscheme.Scheme}, dynamiccache.Options{
			NamespaceSelector:   newSelector(uniqueName("sel")),
			NamespaceResync:     testNamespaceResync,
			RejectClusterScoped: true,
		})
		Expect(err).NotTo(HaveOccurred())
		startCache(dc)

		By("Get")
		var unsupported *dynamiccache.ErrClusterScopedUnsupported
		err = dc.Get(ctx, client.ObjectKey{Name: "cluster-admin"}, &rbacv1.ClusterRole{})
		Expect(errors.As(err, &unsupported)).To(BeTrue(), "expected ErrClusterScopedUnsupported, got: %v", err)
		Expect(unsupported.GVK.Kind).To(Equal("ClusterRole"))

		By("List")
		unsupported = nil
		err = dc.List(ctx, &rbacv1.ClusterRoleList{})
		Expect(errors.As(err, &unsupported)).To(BeTrue(), "expected ErrClusterScopedUnsupported, got: %v", err)
		Expect(unsupported.GVK.Kind).To(Equal("ClusterRole"))

		By("GetInformer")
		unsupported = nil
		_, err = dc.GetInformer(ctx, &rbacv1.ClusterRole{})
		Expect(errors.As(err, &unsupported)).To(BeTrue(), "expected ErrClusterScopedUnsupported, got: %v", err)
	})

	// Contract 8: field indexes apply to current and future namespaces.
	It("applies field indexes registered before Start to namespaces selected before and after", func() {
		selVal := uniqueName("sel")
		ns1 := uniqueName("idx-a")
		createNamespace(ns1, selectedLabels(selVal, nil))
		createConfigMap(ns1, "alice-1", map[string]string{"owner": "alice"})
		createConfigMap(ns1, "bob-1", map[string]string{"owner": "bob"})

		dc := newUnstartedCache(selVal)
		Expect(dc.IndexField(ctx, &corev1.ConfigMap{}, "data.owner", func(o client.Object) []string {
			cm, ok := o.(*corev1.ConfigMap)
			if !ok {
				return nil
			}
			return []string{cm.Data["owner"]}
		})).To(Succeed())
		startCache(dc)

		ownerAlice := client.MatchingFieldsSelector{Selector: fields.OneTermEqualSelector("data.owner", "alice")}

		By("indexed List over an initially selected namespace")
		Eventually(func(g Gomega) {
			list := &corev1.ConfigMapList{}
			g.Expect(dc.List(ctx, list, ownerAlice)).To(Succeed())
			g.Expect(list.Items).To(HaveLen(1))
			g.Expect(list.Items[0].Name).To(Equal("alice-1"))
		}).Should(Succeed())

		By("indexed List includes a namespace selected after the index was registered")
		ns2 := uniqueName("idx-b")
		createNamespace(ns2, nil)
		createConfigMap(ns2, "alice-2", map[string]string{"owner": "alice"})
		createConfigMap(ns2, "carol-2", map[string]string{"owner": "carol"})
		labelNamespace(ns2, selectedLabelKey, selVal)

		Eventually(func(g Gomega) {
			list := &corev1.ConfigMapList{}
			g.Expect(dc.List(ctx, list, ownerAlice)).To(Succeed())
			names := make([]string, 0, len(list.Items))
			for _, item := range list.Items {
				names = append(names, item.Name)
			}
			g.Expect(names).To(ConsistOf("alice-1", "alice-2"))
		}).Should(Succeed())
	})

	// Contract 10: sync reporting.
	It("reports sync via WaitForCacheSync, informer HasSynced and registration HasSynced", func() {
		selVal := uniqueName("sel")
		ns := uniqueName("sync")
		createNamespace(ns, selectedLabels(selVal, nil))
		createConfigMap(ns, "cm", map[string]string{"k": "v"})

		dc := newStartedCache(selVal) // asserts WaitForCacheSync(ctx) == true internally

		By("WaitForCacheSync stays true after start")
		syncCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		Expect(dc.WaitForCacheSync(syncCtx)).To(BeTrue())

		By("the fan-out informer reports HasSynced")
		inf, err := dc.GetInformer(ctx, &corev1.ConfigMap{})
		Expect(err).NotTo(HaveOccurred())
		Eventually(inf.HasSynced).Should(BeTrue())

		By("an event handler registration reports HasSynced")
		rec := newEventRecorder()
		reg, err := inf.AddEventHandler(rec)
		Expect(err).NotTo(HaveOccurred())
		Eventually(reg.HasSynced).Should(BeTrue())
		Eventually(rec.addKeys).Should(ContainElement(ns + "/cm"))
	})

	// Contract 11: RemoveEventHandler stops delivery for that handler only.
	It("stops delivering events to removed handlers while other handlers keep receiving", func() {
		selVal := uniqueName("sel")
		dc := newStartedCache(selVal)

		inf, err := dc.GetInformer(ctx, &corev1.ConfigMap{})
		Expect(err).NotTo(HaveOccurred())
		recRemoved := newEventRecorder()
		recKept := newEventRecorder()
		regRemoved, err := inf.AddEventHandler(recRemoved)
		Expect(err).NotTo(HaveOccurred())
		_, err = inf.AddEventHandler(recKept)
		Expect(err).NotTo(HaveOccurred())

		By("removing the first handler")
		Expect(inf.RemoveEventHandler(regRemoved)).To(Succeed())

		By("selecting a new namespace with an existing ConfigMap")
		ns := uniqueName("rmh")
		createNamespace(ns, nil)
		createConfigMap(ns, "cm", map[string]string{"k": "v"})
		labelNamespace(ns, selectedLabelKey, selVal)

		key := ns + "/cm"
		By("the remaining handler receives the ADD")
		Eventually(recKept.addKeys).Should(ContainElement(key))

		By("the removed handler receives nothing for the new namespace")
		Consistently(recRemoved.addKeys).ShouldNot(ContainElement(key))
	})
})
