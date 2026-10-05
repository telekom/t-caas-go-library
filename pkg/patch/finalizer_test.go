// SPDX-FileCopyrightText: 2026 Deutsche Telekom AG
//
// SPDX-License-Identifier: Apache-2.0

package patch_test

import (
	"context"
	"slices"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/telekom/t-caas-go-library/pkg/patch"
)

const finalizer = "example.com/cleanup"

func TestEnsureFinalizer(t *testing.T) {
	cnt := &counters{}
	c := newFakeClient(t, cnt, interceptor.Funcs{})
	cm := &corev1.ConfigMap{}
	if err := c.Get(t.Context(), cmKey, cm); err != nil {
		t.Fatal(err)
	}
	rv := cm.ResourceVersion

	added, err := patch.EnsureFinalizer(t.Context(), c, cm, finalizer)
	if err != nil || !added {
		t.Fatalf("added=%t err=%v", added, err)
	}
	if cm.ResourceVersion == rv {
		t.Errorf("obj must carry the post-patch resourceVersion")
	}
	added, err = patch.EnsureFinalizer(t.Context(), c, cm, finalizer)
	if err != nil || added {
		t.Fatalf("second call: added=%t err=%v", added, err)
	}
	if cnt.patches != 1 {
		t.Errorf("patches=%d, want 1", cnt.patches)
	}
	stored := &corev1.ConfigMap{}
	if err := c.Get(t.Context(), cmKey, stored); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(stored.Finalizers, []string{finalizer}) {
		t.Errorf("stored finalizers = %v", stored.Finalizers)
	}
}

func TestRemoveFinalizer(t *testing.T) {
	cnt := &counters{}
	c := newFakeClient(t, cnt, interceptor.Funcs{}, &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: cmName, Namespace: namespace, Finalizers: []string{"other", finalizer}},
	})
	cm := &corev1.ConfigMap{}
	if err := c.Get(t.Context(), cmKey, cm); err != nil {
		t.Fatal(err)
	}

	removed, err := patch.RemoveFinalizer(t.Context(), c, cm, finalizer)
	if err != nil || !removed {
		t.Fatalf("removed=%t err=%v", removed, err)
	}
	removed, err = patch.RemoveFinalizer(t.Context(), c, cm, finalizer)
	if err != nil || removed {
		t.Fatalf("second call: removed=%t err=%v", removed, err)
	}
	if cnt.patches != 1 {
		t.Errorf("patches=%d, want 1", cnt.patches)
	}
	stored := &corev1.ConfigMap{}
	if err := c.Get(t.Context(), cmKey, stored); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(stored.Finalizers, []string{"other"}) {
		t.Errorf("stored finalizers = %v", stored.Finalizers)
	}
}

func TestFinalizerConflictRestoresObject(t *testing.T) {
	cnt := &counters{}
	c := newFakeClient(t, cnt, interceptor.Funcs{
		Patch: func(context.Context, client.WithWatch, client.Object, client.Patch, ...client.PatchOption) error {
			cnt.patches++
			return conflictErr(cmName)
		},
	}, &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: cmName, Namespace: namespace, Finalizers: []string{"other"}}})
	cm := &corev1.ConfigMap{}
	if err := c.Get(t.Context(), cmKey, cm); err != nil {
		t.Fatal(err)
	}

	added, err := patch.EnsureFinalizer(t.Context(), c, cm, finalizer)
	if !apierrors.IsConflict(err) || added {
		t.Fatalf("added=%t err=%v, want conflict", added, err)
	}
	if !slices.Equal(cm.Finalizers, []string{"other"}) {
		t.Errorf("finalizers not restored after failed add: %v", cm.Finalizers)
	}
	removed, err := patch.RemoveFinalizer(t.Context(), c, cm, "other")
	if !apierrors.IsConflict(err) || removed {
		t.Fatalf("removed=%t err=%v, want conflict", removed, err)
	}
	if !slices.Equal(cm.Finalizers, []string{"other"}) {
		t.Errorf("finalizers not restored after failed remove: %v", cm.Finalizers)
	}
}

func TestRemoveFinalizerNotFound(t *testing.T) {
	c := newFakeClient(t, &counters{}, interceptor.Funcs{})
	gone := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "gone", Namespace: namespace, Finalizers: []string{finalizer}}}

	_, err := patch.RemoveFinalizer(t.Context(), c, gone, finalizer)
	if !apierrors.IsNotFound(err) {
		t.Fatalf("want NotFound, got %v", err)
	}
	if client.IgnoreNotFound(err) != nil {
		t.Errorf("IgnoreNotFound must recognise the wrapped error")
	}
}
