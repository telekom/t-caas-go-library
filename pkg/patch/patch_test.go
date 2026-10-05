// SPDX-FileCopyrightText: 2026 Deutsche Telekom AG
//
// SPDX-License-Identifier: Apache-2.0

package patch_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/telekom/t-caas-go-library/pkg/patch"
)

const (
	nodeName  = "node-a"
	cmName    = "cm"
	namespace = "default"
	condType  = corev1.NodeConditionType("Example")
)

var (
	nodeKey = client.ObjectKey{Name: nodeName}
	cmKey   = client.ObjectKey{Name: cmName, Namespace: namespace}
)

func newNode() *corev1.Node { return &corev1.Node{} }

func newConfigMap() *corev1.ConfigMap { return &corev1.ConfigMap{} }

func conflictErr(name string) error {
	return apierrors.NewConflict(schema.GroupResource{Resource: "nodes"}, name, errors.New("stale"))
}

// counters records calls that reached the fake client.
type counters struct {
	gets, patches, statusPatches int
}

// newFakeClient returns a fake client seeded with a Node and a ConfigMap that
// counts Get/Patch calls. Non-nil funcs override the counting defaults.
func newFakeClient(t *testing.T, cnt *counters, funcs interceptor.Funcs, objs ...client.Object) client.WithWatch {
	t.Helper()
	if funcs.Get == nil {
		funcs.Get = func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			cnt.gets++
			return c.Get(ctx, key, obj, opts...)
		}
	}
	if funcs.Patch == nil {
		funcs.Patch = func(ctx context.Context, c client.WithWatch, obj client.Object, p client.Patch, opts ...client.PatchOption) error {
			cnt.patches++
			return c.Patch(ctx, obj, p, opts...)
		}
	}
	if funcs.SubResourcePatch == nil {
		funcs.SubResourcePatch = func(
			ctx context.Context, c client.Client, sub string, obj client.Object, p client.Patch, opts ...client.SubResourcePatchOption,
		) error {
			cnt.statusPatches++
			return c.SubResource(sub).Patch(ctx, obj, p, opts...)
		}
	}
	if len(objs) == 0 {
		objs = []client.Object{
			&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: nodeName}, Spec: corev1.NodeSpec{PodCIDR: "192.0.2.0/24"}},
			&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: cmName, Namespace: namespace}, Data: map[string]string{"a": "1"}},
		}
	}
	return fake.NewClientBuilder().
		WithObjects(objs...).
		WithStatusSubresource(&corev1.Node{}).
		WithInterceptorFuncs(funcs).
		Build()
}

func setCondition(n *corev1.Node, t corev1.NodeConditionType) {
	for i := range n.Status.Conditions {
		if n.Status.Conditions[i].Type == t {
			n.Status.Conditions[i].Status = corev1.ConditionTrue
			return
		}
	}
	n.Status.Conditions = append(n.Status.Conditions, corev1.NodeCondition{Type: t, Status: corev1.ConditionTrue})
}

func hasCondition(n *corev1.Node, t corev1.NodeConditionType) bool {
	for _, c := range n.Status.Conditions {
		if c.Type == t {
			return true
		}
	}
	return false
}

func TestStatusPatchesStatusOnly(t *testing.T) {
	cnt := &counters{}
	c := newFakeClient(t, cnt, interceptor.Funcs{})
	before := &corev1.Node{}
	if err := c.Get(t.Context(), nodeKey, before); err != nil {
		t.Fatal(err)
	}

	got, err := patch.Status(t.Context(), c, nil, retry.DefaultRetry, nodeKey, newNode, func(n *corev1.Node) (bool, error) {
		setCondition(n, condType)
		return true, nil
	})
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if !hasCondition(got, condType) {
		t.Errorf("returned object misses condition: %+v", got.Status)
	}
	if got.ResourceVersion == before.ResourceVersion {
		t.Errorf("returned object must carry the post-patch resourceVersion")
	}
	stored := &corev1.Node{}
	if err := c.Get(t.Context(), nodeKey, stored); err != nil {
		t.Fatal(err)
	}
	if !hasCondition(stored, condType) {
		t.Errorf("condition not persisted")
	}
	if stored.Spec.PodCIDR != "192.0.2.0/24" {
		t.Errorf("spec written through status subresource: %q", stored.Spec.PodCIDR)
	}
	if cnt.statusPatches != 1 || cnt.patches != 0 {
		t.Errorf("statusPatches=%d patches=%d, want 1/0", cnt.statusPatches, cnt.patches)
	}
}

func TestMutationCannotChangeIdentity(t *testing.T) {
	for _, changed := range []bool{false, true} {
		for _, status := range []bool{false, true} {
			for _, field := range []string{"name", "namespace", "uid", "gvk"} {
				t.Run(fmt.Sprintf("%s/status=%t/changed=%t", field, status, changed), func(t *testing.T) {
					before := &unstructured.Unstructured{}
					before.SetAPIVersion("v1")
					before.SetKind("ConfigMap")
					before.SetName(cmName)
					before.SetNamespace(namespace)
					before.SetUID("original")
					cnt := &counters{}
					c := newFakeClient(t, cnt, interceptor.Funcs{}, before)
					mutate := func(obj *unstructured.Unstructured) (bool, error) {
						switch field {
						case "name":
							obj.SetName("other")
						case "namespace":
							obj.SetNamespace("other")
						case "uid":
							obj.SetUID("other")
						case "gvk":
							obj.SetKind("Secret")
						}
						return changed, nil
					}
					newObj := func() *unstructured.Unstructured {
						obj := &unstructured.Unstructured{}
						obj.SetAPIVersion("v1")
						obj.SetKind("ConfigMap")
						return obj
					}
					fn := patch.Object[*unstructured.Unstructured]
					if status {
						fn = patch.Status[*unstructured.Unstructured]
					}
					if got, err := fn(t.Context(), c, nil, retry.DefaultRetry, cmKey, newObj, mutate); err == nil || got != nil {
						t.Fatalf("want identity rejection and nil result, got %v, %v", got, err)
					}
					if cnt.patches != 0 || cnt.statusPatches != 0 || cnt.gets != 1 {
						t.Fatalf("unexpected requests: %+v", cnt)
					}
				})
			}
		}
	}
}

func TestStatusRejectsNonStatusMutations(t *testing.T) {
	for _, changed := range []bool{false, true} {
		for _, field := range []string{"spec", "labels", "finalizers"} {
			t.Run(fmt.Sprintf("%s/changed=%t", field, changed), func(t *testing.T) {
				cnt := &counters{}
				c := newFakeClient(t, cnt, interceptor.Funcs{})
				_, err := patch.Status(t.Context(), c, nil, retry.DefaultRetry, nodeKey, newNode, func(n *corev1.Node) (bool, error) {
					setCondition(n, condType)
					switch field {
					case "spec":
						n.Spec.PodCIDR = "other"
					case "labels":
						n.Labels = map[string]string{"example.com/test": "other"}
					case "finalizers":
						n.Finalizers = []string{"example.com/test"}
					}
					return changed, nil
				})
				if err == nil || cnt.statusPatches != 0 || cnt.patches != 0 {
					t.Fatalf("want rejection without writes, got %v and %+v", err, cnt)
				}
			})
		}
	}
}

func TestObjectPatchesMainResource(t *testing.T) {
	cnt := &counters{}
	c := newFakeClient(t, cnt, interceptor.Funcs{})

	got, err := patch.Object(t.Context(), c, nil, retry.DefaultRetry, cmKey, newConfigMap, func(cm *corev1.ConfigMap) (bool, error) {
		cm.Data["b"] = "2"
		return true, nil
	})
	if err != nil {
		t.Fatalf("Object: %v", err)
	}
	if got.Data["b"] != "2" {
		t.Errorf("returned data = %v", got.Data)
	}
	stored := &corev1.ConfigMap{}
	if err := c.Get(t.Context(), cmKey, stored); err != nil {
		t.Fatal(err)
	}
	if stored.Data["a"] != "1" || stored.Data["b"] != "2" {
		t.Errorf("stored data = %v", stored.Data)
	}
	if cnt.patches != 1 || cnt.statusPatches != 0 {
		t.Errorf("patches=%d statusPatches=%d, want 1/0", cnt.patches, cnt.statusPatches)
	}
}

func TestNoChangeSkipsPatch(t *testing.T) {
	cnt := &counters{}
	c := newFakeClient(t, cnt, interceptor.Funcs{})

	got, err := patch.Status(t.Context(), c, nil, retry.DefaultRetry, nodeKey, newNode, func(n *corev1.Node) (bool, error) {
		setCondition(n, condType) // in-memory only
		return false, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !hasCondition(got, condType) {
		t.Errorf("skipped path must return the read object including in-memory edits")
	}
	cm, err := patch.Object(t.Context(), c, nil, retry.DefaultRetry, cmKey, newConfigMap, func(*corev1.ConfigMap) (bool, error) {
		return false, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if cm.Name != cmName {
		t.Errorf("got %q", cm.Name)
	}
	if cnt.patches != 0 || cnt.statusPatches != 0 {
		t.Errorf("patches=%d statusPatches=%d, want 0", cnt.patches, cnt.statusPatches)
	}
	stored := &corev1.Node{}
	if err := c.Get(t.Context(), nodeKey, stored); err != nil {
		t.Fatal(err)
	}
	if hasCondition(stored, condType) {
		t.Errorf("unchanged mutation must not be persisted")
	}
}

func TestMutateErrorIsReturnedWithoutPatchOrRetry(t *testing.T) {
	cnt := &counters{}
	c := newFakeClient(t, cnt, interceptor.Funcs{})
	wantErr := errors.New("transition rejected")

	calls := 0
	got, err := patch.Status(t.Context(), c, nil, retry.DefaultRetry, nodeKey, newNode, func(n *corev1.Node) (bool, error) {
		calls++
		setCondition(n, condType)
		return true, wantErr
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("err = %v, want %v", err, wantErr)
	}
	if got != nil {
		t.Errorf("want zero value on error, got %v", got)
	}
	if calls != 1 || cnt.statusPatches != 0 {
		t.Errorf("calls=%d statusPatches=%d, want 1/0", calls, cnt.statusPatches)
	}
}

func TestMutateConflictIsRetried(t *testing.T) {
	cnt := &counters{}
	c := newFakeClient(t, cnt, interceptor.Funcs{})

	calls := 0
	_, err := patch.Object(t.Context(), c, nil, retry.DefaultRetry, cmKey, newConfigMap, func(cm *corev1.ConfigMap) (bool, error) {
		calls++
		if calls == 1 {
			return false, conflictErr(cm.Name)
		}
		return false, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if calls != 2 || cnt.gets != 2 {
		t.Errorf("calls=%d gets=%d, want 2/2", calls, cnt.gets)
	}
}

func TestNotFoundIsPropagated(t *testing.T) {
	for name, fn := range map[string]func(context.Context, client.Client, client.ObjectKey, patch.MutateFunc[*corev1.ConfigMap]) error{
		"status": func(ctx context.Context, c client.Client, key client.ObjectKey, m patch.MutateFunc[*corev1.ConfigMap]) error {
			_, err := patch.Status(ctx, c, nil, retry.DefaultRetry, key, newConfigMap, m)
			return err
		},
		"object": func(ctx context.Context, c client.Client, key client.ObjectKey, m patch.MutateFunc[*corev1.ConfigMap]) error {
			_, err := patch.Object(ctx, c, nil, retry.DefaultRetry, key, newConfigMap, m)
			return err
		},
	} {
		t.Run(name, func(t *testing.T) {
			cnt := &counters{}
			c := newFakeClient(t, cnt, interceptor.Funcs{})
			called := false
			err := fn(t.Context(), c, client.ObjectKey{Name: "missing", Namespace: namespace}, func(*corev1.ConfigMap) (bool, error) {
				called = true
				return true, nil
			})
			if !apierrors.IsNotFound(err) {
				t.Fatalf("want NotFound, got %v", err)
			}
			if called || cnt.gets != 1 {
				t.Errorf("called=%t gets=%d, want false/1", called, cnt.gets)
			}
		})
	}
}

func TestRetriesInjectedConflictUntilSuccess(t *testing.T) {
	cnt := &counters{}
	c := newFakeClient(t, cnt, interceptor.Funcs{
		SubResourcePatch: func(
			ctx context.Context, c client.Client, sub string, obj client.Object, p client.Patch, opts ...client.SubResourcePatchOption,
		) error {
			cnt.statusPatches++
			if cnt.statusPatches <= 2 {
				return conflictErr(obj.GetName())
			}
			return c.SubResource(sub).Patch(ctx, obj, p, opts...)
		},
	})

	calls := 0
	got, err := patch.Status(t.Context(), c, nil, retry.DefaultRetry, nodeKey, newNode, func(n *corev1.Node) (bool, error) {
		calls++
		setCondition(n, condType)
		return true, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !hasCondition(got, condType) {
		t.Errorf("condition missing after retry")
	}
	if calls != 3 || cnt.gets != 3 || cnt.statusPatches != 3 {
		t.Errorf("calls=%d gets=%d statusPatches=%d, want 3/3/3", calls, cnt.gets, cnt.statusPatches)
	}
}

func TestGivesUpAfterBackoff(t *testing.T) {
	for name, steps := range map[string]int{"single attempt": 1, "three attempts": 3} {
		t.Run(name, func(t *testing.T) {
			cnt := &counters{}
			c := newFakeClient(t, cnt, interceptor.Funcs{
				Patch: func(context.Context, client.WithWatch, client.Object, client.Patch, ...client.PatchOption) error {
					cnt.patches++
					return conflictErr(cmName)
				},
			})
			got, err := patch.Object(t.Context(), c, nil, wait.Backoff{Steps: steps}, cmKey, newConfigMap,
				func(cm *corev1.ConfigMap) (bool, error) {
					cm.Data["b"] = "2"
					return true, nil
				})
			if !apierrors.IsConflict(err) {
				t.Fatalf("want conflict, got %v", err)
			}
			if got != nil {
				t.Errorf("want zero value on error, got %v", got)
			}
			if cnt.patches != steps || cnt.gets != steps {
				t.Errorf("patches=%d gets=%d, want %d", cnt.patches, cnt.gets, steps)
			}
		})
	}
}

// A concurrent writer between read and patch must make the optimistic-lock
// patch fail and re-run the cycle on the fresh object.
func TestRetriesRealConflictOnFreshObject(t *testing.T) {
	cnt := &counters{}
	c := newFakeClient(t, cnt, interceptor.Funcs{})

	calls := 0
	got, err := patch.Status(t.Context(), c, nil, retry.DefaultRetry, nodeKey, newNode, func(n *corev1.Node) (bool, error) {
		calls++
		if calls == 1 {
			other := &corev1.Node{}
			if err := c.Get(t.Context(), nodeKey, other); err != nil {
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
	if calls != 2 || cnt.statusPatches != 2 {
		t.Errorf("calls=%d statusPatches=%d, want 2/2", calls, cnt.statusPatches)
	}
	if !hasCondition(got, condType) || !hasCondition(got, "Other") {
		t.Errorf("concurrent write lost: %+v", got.Status.Conditions)
	}
}

func TestUsesReaderForReads(t *testing.T) {
	writerCnt, readerCnt := &counters{}, &counters{}
	writer := newFakeClient(t, writerCnt, interceptor.Funcs{})
	reader := newFakeClient(t, readerCnt, interceptor.Funcs{})

	if _, err := patch.Status(t.Context(), writer, reader, retry.DefaultRetry, nodeKey, newNode, func(*corev1.Node) (bool, error) {
		return false, nil
	}); err != nil {
		t.Fatal(err)
	}
	if readerCnt.gets != 1 || writerCnt.gets != 0 {
		t.Errorf("reader gets=%d writer gets=%d, want 1/0", readerCnt.gets, writerCnt.gets)
	}
}
