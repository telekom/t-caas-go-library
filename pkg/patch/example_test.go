// SPDX-FileCopyrightText: 2026 Deutsche Telekom AG
//
// SPDX-License-Identifier: Apache-2.0

package patch_test

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/telekom/t-caas-go-library/pkg/patch"
)

func ExampleStatus() {
	ctx := context.Background()
	c := fake.NewClientBuilder().
		WithObjects(&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "worker-1"}}).
		WithStatusSubresource(&corev1.Node{}).
		Build()

	node, err := patch.Status(ctx, c, nil, retry.DefaultRetry, client.ObjectKey{Name: "worker-1"},
		func() *corev1.Node { return &corev1.Node{} },
		func(n *corev1.Node) (bool, error) {
			for _, cond := range n.Status.Conditions {
				if cond.Type == "Maintenance" {
					return false, nil // already set: no request is sent
				}
			}
			n.Status.Conditions = append(n.Status.Conditions,
				corev1.NodeCondition{Type: "Maintenance", Status: corev1.ConditionTrue})
			return true, nil
		})
	if err != nil {
		fmt.Println("error:", err)
		return
	}
	fmt.Println(node.Status.Conditions[0].Type, node.Status.Conditions[0].Status)
	// Output: Maintenance True
}

func ExampleObject() {
	ctx := context.Background()
	c := fake.NewClientBuilder().
		WithObjects(&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "settings", Namespace: "default"}}).
		Build()

	cm, err := patch.Object(ctx, c, nil, retry.DefaultRetry, client.ObjectKey{Name: "settings", Namespace: "default"},
		func() *corev1.ConfigMap { return &corev1.ConfigMap{} },
		func(cm *corev1.ConfigMap) (bool, error) {
			if cm.Data["mode"] == "strict" {
				return false, nil
			}
			if cm.Data == nil {
				cm.Data = map[string]string{}
			}
			cm.Data["mode"] = "strict"
			return true, nil
		})
	if err != nil {
		fmt.Println("error:", err)
		return
	}
	fmt.Println(cm.Data["mode"])
	// Output: strict
}

func ExampleEnsureFinalizer() {
	ctx := context.Background()
	cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "owned", Namespace: "default"}}
	c := fake.NewClientBuilder().WithObjects(cm).Build()

	added, err := patch.EnsureFinalizer(ctx, c, cm, "example.com/cleanup")
	fmt.Println(added, err, cm.Finalizers)

	removed, err := patch.RemoveFinalizer(ctx, c, cm, "example.com/cleanup")
	fmt.Println(removed, err, cm.Finalizers)
	// Output:
	// true <nil> [example.com/cleanup]
	// true <nil> []
}
