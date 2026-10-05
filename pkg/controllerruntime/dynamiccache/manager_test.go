// SPDX-FileCopyrightText: 2026 Deutsche Telekom AG
//
// SPDX-License-Identifier: Apache-2.0

package dynamiccache_test

// Contract 9: full manager integration — the adoption path operators use:
// only manager initialization changes (NewCache + client cache bypass for
// cluster-scoped types), everything else is a stock controller.

import (
	"context"
	"strings"
	"sync"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	dynamiccache "github.com/telekom/t-caas-go-library/pkg/controllerruntime/dynamiccache"
)

// countingReconciler records reconciled keys and whether the manager-cached
// client observed the object as NotFound, thread-safely.
type countingReconciler struct {
	client client.Client

	mu       sync.Mutex
	seen     map[string]int
	notFound map[string]int
}

func newCountingReconciler(c client.Client) *countingReconciler {
	return &countingReconciler{
		client:   c,
		seen:     map[string]int{},
		notFound: map[string]int{},
	}
}

func (r *countingReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	err := r.client.Get(ctx, req.NamespacedName, &corev1.ConfigMap{})

	r.mu.Lock()
	defer r.mu.Unlock()
	key := req.String()
	r.seen[key]++
	switch {
	case err == nil:
	case apierrors.IsNotFound(err):
		r.notFound[key]++
	default:
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

func (r *countingReconciler) seenCount(key string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.seen[key]
}

func (r *countingReconciler) notFoundCount(key string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.notFound[key]
}

func (r *countingReconciler) keysInNamespace(namespace string) []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var keys []string
	for key := range r.seen {
		if strings.HasPrefix(key, namespace+"/") {
			keys = append(keys, key)
		}
	}
	return keys
}

var _ = Describe("manager integration", func() {
	It("reconciles only selected namespaces and converges after deselection", func() {
		selVal := uniqueName("sel")
		nsSelected := uniqueName("mgr-sel")
		nsOther := uniqueName("mgr-other")
		createNamespace(nsSelected, selectedLabels(selVal, nil))
		createNamespace(nsOther, nil)
		createConfigMap(nsSelected, "target", map[string]string{"k": "v"})
		createConfigMap(nsOther, "ignored", map[string]string{"k": "v"})

		By("constructing a manager whose only cache customization is NewCache")
		mgr, err := ctrl.NewManager(cfg, ctrl.Options{
			Scheme: clientgoscheme.Scheme,
			NewCache: dynamiccache.NewCacheFunc(dynamiccache.Options{
				NamespaceSelector: newSelector(selVal),
				NamespaceResync:   testNamespaceResync,
			}),
			Metrics: metricsserver.Options{BindAddress: "0"},
			Client: client.Options{
				Cache: &client.CacheOptions{
					// Cluster-scoped reads must bypass the dynamic cache.
					DisableFor: []client.Object{&rbacv1.ClusterRole{}},
				},
			},
		})
		Expect(err).NotTo(HaveOccurred())

		rec := newCountingReconciler(mgr.GetClient())
		Expect(builder.ControllerManagedBy(mgr).
			Named(uniqueName("dynamiccache-cm")). // unique: controller names are global per process
			For(&corev1.ConfigMap{}).
			Complete(rec)).To(Succeed())

		mgrCtx, cancel := context.WithCancel(context.Background())
		DeferCleanup(cancel)
		go func() {
			defer GinkgoRecover()
			Expect(mgr.Start(mgrCtx)).To(Succeed())
		}()
		syncCtx, syncCancel := context.WithTimeout(mgrCtx, 10*time.Second)
		defer syncCancel()
		Expect(mgr.GetCache().WaitForCacheSync(syncCtx)).To(BeTrue())

		targetKey := nsSelected + "/target"

		By("reconciling ConfigMaps in the selected namespace")
		Eventually(func() int { return rec.seenCount(targetKey) }).Should(BeNumerically(">=", 1))

		By("never reconciling ConfigMaps in unselected namespaces")
		Consistently(func() []string { return rec.keysInNamespace(nsOther) }).Should(BeEmpty())

		By("reconciling pre-existing ConfigMaps of a namespace that becomes selected")
		nsLate := uniqueName("mgr-late")
		createNamespace(nsLate, nil)
		createConfigMap(nsLate, "late-cm", map[string]string{"k": "v"})
		labelNamespace(nsLate, selectedLabelKey, selVal)

		lateKey := nsLate + "/late-cm"
		Eventually(func() int { return rec.seenCount(lateKey) }).Should(BeNumerically(">=", 1))

		By("converging after deselection: a reconcile fires and Get reports NotFound")
		unlabelNamespace(nsLate)
		Eventually(func() int { return rec.notFoundCount(lateKey) }).Should(BeNumerically(">=", 1),
			"synthetic delete should trigger a reconcile that observes NotFound via the cached client")

		By("the cached client still serves selected namespaces")
		Expect(rec.seenCount(targetKey)).To(BeNumerically(">=", 1))
		Expect(rec.notFoundCount(targetKey)).To(BeZero())
	})
})
