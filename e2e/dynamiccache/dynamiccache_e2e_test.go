// SPDX-FileCopyrightText: 2026 Deutsche Telekom AG
//
// SPDX-License-Identifier: Apache-2.0

package e2e

import (
	"fmt"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var _ = Describe("dynamiccache with auth-operator managed RBAC", Ordered, func() {
	var (
		tenantNS *corev1.Namespace
		cmNames  = []string{"dc-e2e-alpha", "dc-e2e-beta", "dc-e2e-gamma"}
	)

	expectedKeys := func() []any {
		keys := make([]any, 0, len(cmNames))
		for _, name := range cmNames {
			keys = append(keys, tenantNS.Name+"/"+name)
		}
		return keys
	}

	// setSelectionLabel adds or removes the selection label on the tenant
	// namespace via a merge patch, using the admin client.
	setSelectionLabel := func(ctx SpecContext, selected bool) {
		value := fmt.Sprintf("%q", selectionValue)
		if !selected {
			value = "null"
		}
		patch := []byte(fmt.Sprintf(`{"metadata":{"labels":{%q:%s}}}`, selectionLabel, value))
		Expect(adminClient.Patch(ctx, tenantNS, client.RawPatch(types.MergePatchType, patch))).To(Succeed())
	}

	expectAllConfigMapsServed := func(ctx SpecContext) {
		Eventually(func(g Gomega) {
			for _, name := range cmNames {
				cm := &corev1.ConfigMap{}
				g.Expect(dynCache.Get(ctx, types.NamespacedName{Namespace: tenantNS.Name, Name: name}, cm)).
					To(Succeed(), "ConfigMap %s/%s should be served from the cache", tenantNS.Name, name)
				g.Expect(cm.Data).To(HaveKeyWithValue("owner", "dynamiccache-e2e"))
			}
		}).WithContext(ctx).WithTimeout(convergenceTimeout).WithPolling(pollInterval).Should(Succeed())
	}

	BeforeAll(func(ctx SpecContext) {
		// Create the tenant namespace WITHOUT the selection label and populate
		// it while it is still unselected, so both the RBAC grant and the
		// cache contents must catch up after labeling.
		tenantNS = &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{GenerateName: "dynamiccache-e2e-tenant-"},
		}
		Expect(adminClient.Create(ctx, tenantNS)).To(Succeed())
		DeferCleanup(func(ctx SpecContext) {
			err := adminClient.Delete(ctx, tenantNS)
			if err != nil && !apierrors.IsNotFound(err) {
				GinkgoWriter.Printf("cleanup of namespace %q failed: %v\n", tenantNS.Name, err)
			}
		})

		for _, name := range cmNames {
			cm := &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: tenantNS.Name},
				Data:       map[string]string{"owner": "dynamiccache-e2e"},
			}
			Expect(adminClient.Create(ctx, cm)).To(Succeed())
		}
	})

	It("eventually serves pre-existing ConfigMaps after labeling, racing the async RBAC grant", func(ctx SpecContext) {
		// Before selection, reads must return NotFound.
		cm := &corev1.ConfigMap{}
		err := dynCache.Get(ctx, types.NamespacedName{Namespace: tenantNS.Name, Name: cmNames[0]}, cm)
		Expect(apierrors.IsNotFound(err)).To(BeTrue(), "expected NotFound for unselected namespace, got: %v", err)

		// Label the namespace. auth-operator now creates the RoleBinding
		// asynchronously; the per-namespace informer's initial LIST is
		// expected to fail with 403 until then and must retry until it
		// converges.
		setSelectionLabel(ctx, true)

		expectAllConfigMapsServed(ctx)

		// The registered event handler received (replayed) ADDs for the
		// pre-existing objects.
		Eventually(func() []string {
			return cmEvents.AddsIn(tenantNS.Name)
		}).WithTimeout(convergenceTimeout).WithPolling(pollInterval).Should(ContainElements(expectedKeys()...))

		// The namespace itself is readable from the cache.
		Eventually(func() error {
			return dynCache.Get(ctx, types.NamespacedName{Name: tenantNS.Name}, &corev1.Namespace{})
		}).WithTimeout(convergenceTimeout).WithPolling(pollInterval).Should(Succeed())

		// A cluster-wide List only returns objects from selected namespaces —
		// and therefore includes the tenant namespace's objects now.
		cmList := &corev1.ConfigMapList{}
		Expect(dynCache.List(ctx, cmList)).To(Succeed())
		Expect(namesInNamespace(cmList, tenantNS.Name)).To(ContainElements("dc-e2e-alpha", "dc-e2e-beta", "dc-e2e-gamma"))
	})

	It("delivers synthetic deletes and returns NotFound after unlabeling", func(ctx SpecContext) {
		cmEvents.Reset()
		setSelectionLabel(ctx, false)

		// Reads converge to NotFound; absence is data, not an error.
		Eventually(func(g Gomega) {
			for _, name := range cmNames {
				err := dynCache.Get(ctx, types.NamespacedName{Namespace: tenantNS.Name, Name: name}, &corev1.ConfigMap{})
				g.Expect(apierrors.IsNotFound(err)).To(BeTrue(), "expected NotFound, got: %v", err)
			}
		}).WithContext(ctx).WithTimeout(convergenceTimeout).WithPolling(pollInterval).Should(Succeed())

		// The handler received synthetic DELETEs for every cached object of
		// the deselected namespace.
		Eventually(func() []string {
			return cmEvents.DeletesIn(tenantNS.Name)
		}).WithTimeout(convergenceTimeout).WithPolling(pollInterval).Should(ContainElements(expectedKeys()...))

		// List no longer returns the namespace's objects.
		cmList := &corev1.ConfigMapList{}
		Expect(dynCache.List(ctx, cmList)).To(Succeed())
		Expect(namesInNamespace(cmList, tenantNS.Name)).To(BeEmpty())

		// The namespace disappears from the cached namespace view as well.
		Eventually(func() bool {
			err := dynCache.Get(ctx, types.NamespacedName{Name: tenantNS.Name}, &corev1.Namespace{})
			return apierrors.IsNotFound(err)
		}).WithTimeout(convergenceTimeout).WithPolling(pollInterval).Should(BeTrue())

		// Nothing crashed: the cache still serves reads (empty result set)
		// and the suite process is alive, i.e. no lingering watch error
		// escalated to a failure.
		Expect(dynCache.List(ctx, &corev1.ConfigMapList{})).To(Succeed())
	})

	It("serves the objects again after relabeling", func(ctx SpecContext) {
		cmEvents.Reset()
		setSelectionLabel(ctx, true)

		expectAllConfigMapsServed(ctx)

		// Replayed ADDs are delivered again after re-selection.
		Eventually(func() []string {
			return cmEvents.AddsIn(tenantNS.Name)
		}).WithTimeout(convergenceTimeout).WithPolling(pollInterval).Should(ContainElements(expectedKeys()...))
	})
})

// namesInNamespace returns the names of the ConfigMaps in the list that live
// in the given namespace.
func namesInNamespace(list *corev1.ConfigMapList, namespace string) []string {
	var names []string
	for i := range list.Items {
		if list.Items[i].Namespace == namespace {
			names = append(names, list.Items[i].Name)
		}
	}
	return names
}
