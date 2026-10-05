// SPDX-FileCopyrightText: 2026 Deutsche Telekom AG
//
// SPDX-License-Identifier: Apache-2.0

package ssa_test

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	corev1ac "k8s.io/client-go/applyconfigurations/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/telekom/t-caas-go-library/pkg/ssa"
)

// ExampleApplier defines a descriptor for ConfigMaps and applies the same
// desired state twice: the second call is skipped without an apply request.
func ExampleApplier() {
	configMaps := ssa.Applier[*corev1.ConfigMap, *corev1ac.ConfigMapApplyConfiguration]{
		Kind:       "ConfigMap",
		Namespaced: true,
		New:        func() *corev1.ConfigMap { return &corev1.ConfigMap{} },
		Matches:    ssa.MatchesJSON[*corev1.ConfigMap, *corev1ac.ConfigMapApplyConfiguration],
		Extract:    corev1ac.ExtractConfigMap,
	}

	ctx := context.Background()
	c := fake.NewClientBuilder().WithReturnManagedFields().Build()
	for _, mode := range []string{"fast", "fast", "safe"} {
		// Build a fresh apply configuration for every call.
		desired := corev1ac.ConfigMap("settings", "default").WithData(map[string]string{"mode": mode})
		result, err := configMaps.PatchApply(ctx, c, desired, false,
			client.FieldOwner("my-operator"), client.ForceOwnership)
		if err != nil {
			fmt.Println("error:", err)
			return
		}
		fmt.Println(mode, result)
	}
	// Output:
	// fast created
	// fast skipped
	// safe patched
}

// ExampleMatchesJSON shows the subset semantics of the generic comparator:
// values the desired configuration does not declare are ignored.
func ExampleMatchesJSON() {
	live := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name: "settings", Namespace: "default",
			Labels: map[string]string{"app": "demo", "added-by": "someone-else"},
		},
		Data: map[string]string{"mode": "fast"},
	}

	same := corev1ac.ConfigMap("settings", "default").
		WithLabels(map[string]string{"app": "demo"}).
		WithData(map[string]string{"mode": "fast"})
	changed := corev1ac.ConfigMap("settings", "default").
		WithData(map[string]string{"mode": "safe"})

	fmt.Println(ssa.MatchesJSON(live, same))
	fmt.Println(ssa.MatchesJSON(live, changed))
	// Output:
	// true
	// false
}
