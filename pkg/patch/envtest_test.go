// SPDX-FileCopyrightText: 2026 Deutsche Telekom AG
//
// SPDX-License-Identifier: Apache-2.0

package patch_test

import (
	"fmt"
	"os"
	"strconv"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	"github.com/telekom/t-caas-go-library/pkg/patch"
)

// contended is generous enough for many concurrent writers to all succeed.
var contended = wait.Backoff{Steps: 100, Duration: 5 * time.Millisecond, Factor: 1.2, Jitter: 1, Cap: 200 * time.Millisecond}

// TestEnvtest proves the optimistic lock against a real API server, where a
// JSON merge patch replaces lists as a whole and would lose concurrent updates
// without the resourceVersion precondition.
func TestEnvtest(t *testing.T) {
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		t.Skip("KUBEBUILDER_ASSETS not set; run via `make test`")
	}
	testEnv := &envtest.Environment{}
	cfg, err := testEnv.Start()
	if err != nil {
		t.Fatalf("start envtest: %v", err)
	}
	t.Cleanup(func() {
		if err := testEnv.Stop(); err != nil {
			t.Errorf("stop envtest: %v", err)
		}
	})
	c, err := client.New(cfg, client.Options{})
	if err != nil {
		t.Fatal(err)
	}

	t.Run("concurrent status writers do not lose updates", func(t *testing.T) { testConcurrentStatusWriters(t, c) })
	t.Run("concurrent object writers do not lose increments", func(t *testing.T) { testConcurrentObjectWriters(t, c) })
	t.Run("interleaved write is rejected and retried", func(t *testing.T) { testInterleavedWrite(t, c) })
	t.Run("finalizers use the optimistic lock", func(t *testing.T) { testFinalizers(t, c) })
	t.Run("not found", func(t *testing.T) { testNotFound(t, c) })
	t.Run("status rejects metadata mutations", func(t *testing.T) { testStatusMetadata(t, c) })
}

func testStatusMetadata(t *testing.T, c client.Client) {
	t.Helper()
	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "status-metadata", Finalizers: []string{finalizer}}}
	if err := c.Create(t.Context(), node); err != nil {
		t.Fatal(err)
	}
	for _, deleting := range []bool{false, true} {
		if deleting {
			if err := c.Delete(t.Context(), node); err != nil {
				t.Fatal(err)
			}
		}
		_, err := patch.Status(t.Context(), c, nil, retry.DefaultRetry, client.ObjectKeyFromObject(node), newNode,
			func(n *corev1.Node) (bool, error) {
				n.Labels = map[string]string{"example.com/unintended": "true"}
				n.Finalizers = nil
				setCondition(n, condType)
				return true, nil
			})
		if err == nil {
			t.Fatal("want non-status mutation rejection")
		}
		stored := &corev1.Node{}
		if err := c.Get(t.Context(), client.ObjectKeyFromObject(node), stored); err != nil {
			t.Fatal(err)
		}
		if len(stored.Labels) != 0 || len(stored.Finalizers) != 1 || hasCondition(stored, condType) {
			t.Fatalf("rejected status mutation persisted: %+v", stored)
		}
	}
	if _, err := patch.RemoveFinalizer(t.Context(), c, node, finalizer); err == nil {
		t.Fatal("original object should be stale after delete")
	}
	if err := c.Get(t.Context(), client.ObjectKeyFromObject(node), node); err != nil {
		t.Fatal(err)
	}
	if _, err := patch.RemoveFinalizer(t.Context(), c, node, finalizer); err != nil {
		t.Fatal(err)
	}
}

func testConcurrentStatusWriters(t *testing.T, c client.Client) {
	t.Helper()
	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "concurrent"}}
	if err := c.Create(t.Context(), node); err != nil {
		t.Fatal(err)
	}
	const writers = 8
	var wg sync.WaitGroup
	errs := make(chan error, writers)
	for i := range writers {
		wg.Go(func() {
			typ := corev1.NodeConditionType(fmt.Sprintf("Writer%d", i))
			_, err := patch.Status(t.Context(), c, nil, contended, client.ObjectKeyFromObject(node), newNode,
				func(n *corev1.Node) (bool, error) {
					setCondition(n, typ)
					return true, nil
				})
			errs <- err
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	stored := &corev1.Node{}
	if err := c.Get(t.Context(), client.ObjectKeyFromObject(node), stored); err != nil {
		t.Fatal(err)
	}
	if len(stored.Status.Conditions) != writers {
		t.Errorf("got %d conditions, want %d: %+v", len(stored.Status.Conditions), writers, stored.Status.Conditions)
	}
}

func testConcurrentObjectWriters(t *testing.T, c client.Client) {
	t.Helper()
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "counter", Namespace: namespace},
		Data:       map[string]string{"count": "0"},
	}
	if err := c.Create(t.Context(), cm); err != nil {
		t.Fatal(err)
	}
	const writers, increments = 6, 5
	var wg sync.WaitGroup
	errs := make(chan error, writers*increments)
	for range writers {
		wg.Go(func() {
			for range increments {
				_, err := patch.Object(t.Context(), c, nil, contended, client.ObjectKeyFromObject(cm), newConfigMap,
					func(cm *corev1.ConfigMap) (bool, error) {
						n, err := strconv.Atoi(cm.Data["count"])
						if err != nil {
							return false, err
						}
						cm.Data["count"] = strconv.Itoa(n + 1)
						return true, nil
					})
				errs <- err
			}
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	stored := &corev1.ConfigMap{}
	if err := c.Get(t.Context(), client.ObjectKeyFromObject(cm), stored); err != nil {
		t.Fatal(err)
	}
	if want := strconv.Itoa(writers * increments); stored.Data["count"] != want {
		t.Errorf("count = %s, want %s", stored.Data["count"], want)
	}
}

func testInterleavedWrite(t *testing.T, c client.Client) {
	t.Helper()
	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "interleaved"}}
	if err := c.Create(t.Context(), node); err != nil {
		t.Fatal(err)
	}
	key := client.ObjectKeyFromObject(node)
	calls := 0
	got, err := patch.Status(t.Context(), c, nil, retry.DefaultRetry, key, newNode, func(n *corev1.Node) (bool, error) {
		calls++
		if calls == 1 {
			other := &corev1.Node{}
			if err := c.Get(t.Context(), key, other); err != nil {
				return false, err
			}
			setCondition(other, "Other")
			if err := c.Status().Update(t.Context(), other); err != nil {
				return false, err
			}
		}
		setCondition(n, condType)
		return true, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Errorf("calls = %d, want 2", calls)
	}
	if !hasCondition(got, condType) || !hasCondition(got, "Other") {
		t.Errorf("lost update: %+v", got.Status.Conditions)
	}

	_, err = patch.Status(t.Context(), c, nil, wait.Backoff{Steps: 1}, key, newNode, func(n *corev1.Node) (bool, error) {
		other := &corev1.Node{}
		if err := c.Get(t.Context(), key, other); err != nil {
			return false, err
		}
		setCondition(other, "Third")
		if err := c.Status().Update(t.Context(), other); err != nil {
			return false, err
		}
		setCondition(n, "Fourth")
		return true, nil
	})
	if !apierrors.IsConflict(err) {
		t.Errorf("single attempt must surface the conflict, got %v", err)
	}
}

func testFinalizers(t *testing.T, c client.Client) {
	t.Helper()
	cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "finalized", Namespace: namespace}}
	if err := c.Create(t.Context(), cm); err != nil {
		t.Fatal(err)
	}
	stale := cm.DeepCopy()
	if added, err := patch.EnsureFinalizer(t.Context(), c, cm, finalizer); err != nil || !added {
		t.Fatalf("added=%t err=%v", added, err)
	}
	if _, err := patch.EnsureFinalizer(t.Context(), c, stale, "example.com/other"); !apierrors.IsConflict(err) {
		t.Fatalf("stale object must conflict, got %v", err)
	}
	if len(stale.Finalizers) != 0 {
		t.Errorf("stale finalizers not restored: %v", stale.Finalizers)
	}

	if err := c.Delete(t.Context(), cm); err != nil {
		t.Fatal(err)
	}
	if err := c.Get(t.Context(), client.ObjectKeyFromObject(cm), cm); err != nil {
		t.Fatal(err)
	}
	if removed, err := patch.RemoveFinalizer(t.Context(), c, cm, finalizer); err != nil || !removed {
		t.Fatalf("removed=%t err=%v", removed, err)
	}
	if err := c.Get(t.Context(), client.ObjectKeyFromObject(cm), &corev1.ConfigMap{}); !apierrors.IsNotFound(err) {
		t.Errorf("object must be gone after removing the last finalizer, got %v", err)
	}
}

func testNotFound(t *testing.T, c client.Client) {
	t.Helper()
	_, err := patch.Status(t.Context(), c, nil, retry.DefaultRetry, client.ObjectKey{Name: "missing"}, newNode,
		func(*corev1.Node) (bool, error) { return true, nil })
	if !apierrors.IsNotFound(err) {
		t.Errorf("want NotFound, got %v", err)
	}
}
