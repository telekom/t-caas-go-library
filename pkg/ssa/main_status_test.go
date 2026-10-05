// SPDX-FileCopyrightText: 2026 Deutsche Telekom AG
//
// SPDX-License-Identifier: Apache-2.0

package ssa_test

import (
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	corev1ac "k8s.io/client-go/applyconfigurations/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/telekom/t-caas-go-library/pkg/ssa"
)

// widgetApplyConfiguration models a generated configuration for this test CRD.
type widgetApplyConfiguration struct {
	*corev1ac.ConfigMapApplyConfiguration
	Status map[string]string `json:"status,omitempty"`
}

var _ = Describe("main-endpoint status", func() {
	It("does not skip status declared for a CRD without a status subresource", func() {
		gvk := schema.GroupVersionKind{Group: "ssa-test.example.com", Version: "v1", Kind: "Widget"}
		metav1.AddToGroupVersion(k8sClient.Scheme(), gvk.GroupVersion())
		crd := &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "apiextensions.k8s.io/v1",
			"kind":       "CustomResourceDefinition",
			"metadata":   map[string]any{"name": "widgets." + gvk.Group},
			"spec": map[string]any{
				"group": gvk.Group,
				"scope": "Cluster",
				"names": map[string]any{"plural": "widgets", "singular": "widget", "kind": gvk.Kind},
				"versions": []any{map[string]any{
					"name": gvk.Version, "served": true, "storage": true,
					"schema": map[string]any{"openAPIV3Schema": map[string]any{
						"type": "object",
						"properties": map[string]any{"status": map[string]any{
							"type": "object", "properties": map[string]any{"phase": map[string]any{"type": "string"}},
						}},
					}},
				}},
			},
		}}
		Expect(k8sClient.Create(testCtx, crd)).To(Succeed())
		DeferCleanup(func() { Expect(client.IgnoreNotFound(k8sClient.Delete(testCtx, crd))).To(Succeed()) })

		widget := func() *unstructured.Unstructured {
			obj := &unstructured.Unstructured{Object: map[string]any{
				"apiVersion": gvk.GroupVersion().String(), "kind": gvk.Kind,
				"metadata": map[string]any{"name": "main-status"},
			}}
			return obj
		}
		configuration := func() *widgetApplyConfiguration {
			return &widgetApplyConfiguration{
				ConfigMapApplyConfiguration: (&corev1ac.ConfigMapApplyConfiguration{}).
					WithName("main-status").WithKind(gvk.Kind).WithAPIVersion(gvk.GroupVersion().String()),
			}
		}
		Eventually(func() error {
			return k8sClient.Apply(testCtx, configuration(), client.FieldOwner(foreignOwner))
		}, 30*time.Second, 100*time.Millisecond).Should(Succeed())

		applier := ssa.Applier[*unstructured.Unstructured, *widgetApplyConfiguration]{
			Kind: gvk.Kind, New: widget,
			Matches: ssa.MatchesJSON[*unstructured.Unstructured, *widgetApplyConfiguration],
			Extract: func(existing *unstructured.Unstructured, manager string) (*widgetApplyConfiguration, error) {
				// The manager has never applied this object, so it owns no status.
				Expect(managedBy(existing, manager)).To(BeFalse())
				return configuration(), nil
			},
		}
		desired := configuration()
		desired.Status = map[string]string{"phase": "Ready"}
		c := counting()
		result, err := applier.PatchApply(testCtx, c, desired, false, client.FieldOwner(owner))
		Expect(err).NotTo(HaveOccurred())
		Expect(result).To(Equal(ssa.PatchApplyResultPatched))
		Expect(c.ApplyCalls).To(Equal(1))
		live := widget()
		Expect(k8sClient.Get(testCtx, client.ObjectKeyFromObject(live), live)).To(Succeed())
		phase, found, err := unstructured.NestedString(live.Object, "status", "phase")
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(phase).To(Equal("Ready"))
	})
})
