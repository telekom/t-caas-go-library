// SPDX-FileCopyrightText: 2026 Deutsche Telekom AG
//
// SPDX-License-Identifier: Apache-2.0

package namespaceselector_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/telekom/t-caas-go-library/pkg/namespaceselector"
)

func TestRequest(t *testing.T) {
	scheme := runtime.NewScheme()
	corev1.AddToScheme(scheme)
	var reads atomic.Int32
	reader := fake.NewClientBuilder().WithScheme(scheme).WithObjects(&corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: "team", Labels: map[string]string{"team": "one"}},
	}).WithInterceptorFuncs(interceptor.Funcs{
		Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			reads.Add(1)
			return c.Get(ctx, key, obj, opts...)
		},
	}).Build()
	request, err := namespaceselector.NewRequest(reader, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for _, test := range []struct {
		namespace string
		selector  *metav1.LabelSelector
		want      bool
	}{
		{"team", nil, false}, {"", &metav1.LabelSelector{}, false},
		{"team", &metav1.LabelSelector{}, true},
		{"team", &metav1.LabelSelector{MatchLabels: map[string]string{"team": "one"}}, true},
		{"team", &metav1.LabelSelector{MatchLabels: map[string]string{"team": "two"}}, false},
		{"team", &metav1.LabelSelector{MatchExpressions: []metav1.LabelSelectorRequirement{
			{Key: "team", Operator: metav1.LabelSelectorOpIn, Values: []string{"one"}},
		}}, true},
	} {
		got, err := request.Matches(ctx, test.namespace, test.selector)
		if err != nil || got != test.want {
			t.Fatalf("%+v => %v %v", test, got, err)
		}
	}
	if reads.Load() != 1 {
		t.Fatalf("reads=%d", reads.Load())
	}
	namespace := &corev1.Namespace{}
	reader.Get(ctx, types.NamespacedName{Name: "team"}, namespace)
	namespace.Labels["team"] = "two"
	reader.Update(ctx, namespace)
	if ok, err := request.Matches(ctx, "team", &metav1.LabelSelector{MatchLabels: map[string]string{"team": "one"}}); err != nil || !ok {
		t.Fatal("request snapshot changed")
	}
	next, _ := namespaceselector.NewRequest(reader, time.Second)
	if ok, err := next.Matches(ctx, "team", &metav1.LabelSelector{MatchLabels: map[string]string{"team": "two"}}); err != nil || !ok {
		t.Fatal("new request did not see live labels")
	}
	before := reads.Load()
	for range 2 {
		if _, err := request.Matches(ctx, "missing", &metav1.LabelSelector{}); !apierrors.IsNotFound(err) {
			t.Fatalf("missing namespace: %v", err)
		}
	}
	if reads.Load() != before+1 {
		t.Fatal("missing namespace not memoized")
	}
	invalid := &metav1.LabelSelector{MatchExpressions: []metav1.LabelSelectorRequirement{{Key: "x", Operator: "invalid"}}}
	if _, err := request.Matches(ctx, "bad", invalid); err == nil {
		t.Fatal("invalid selector accepted")
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := request.Matches(cancelled, "team", nil); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := namespaceselector.NewRequest(nil, time.Second); err == nil {
		t.Fatal("nil reader accepted")
	}
	if _, err := namespaceselector.NewRequest(reader, 0); err == nil {
		t.Fatal("zero timeout accepted")
	}
}

func TestReadTimeoutAndConcurrentMemoization(t *testing.T) {
	scheme := runtime.NewScheme()
	corev1.AddToScheme(scheme)
	var reads atomic.Int32
	reader := fake.NewClientBuilder().WithScheme(scheme).WithInterceptorFuncs(interceptor.Funcs{
		Get: func(ctx context.Context, _ client.WithWatch, _ client.ObjectKey, _ client.Object, _ ...client.GetOption) error {
			reads.Add(1)
			<-ctx.Done()
			return ctx.Err()
		},
	}).Build()
	request, _ := namespaceselector.NewRequest(reader, 10*time.Millisecond)
	var workers sync.WaitGroup
	for range 5 {
		workers.Go(func() {
			_, err := request.Matches(context.Background(), "timeout", &metav1.LabelSelector{})
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Error(err)
			}
		})
	}
	workers.Wait()
	if reads.Load() != 1 {
		t.Fatal("concurrent callers repeated the read")
	}
}

func ExampleRequest_Matches() {
	scheme := runtime.NewScheme()
	corev1.AddToScheme(scheme)
	reader := fake.NewClientBuilder().WithScheme(scheme).WithObjects(&corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: "team", Labels: map[string]string{"environment": "test"}},
	}).Build()
	request, _ := namespaceselector.NewRequest(reader, time.Second)
	matched, err := request.Matches(context.Background(), "team",
		&metav1.LabelSelector{MatchLabels: map[string]string{"environment": "test"}})
	fmt.Println(matched, err)
	// Output: true <nil>
}

func TestCancellationWhileWaitingForRead(t *testing.T) {
	scheme := runtime.NewScheme()
	corev1.AddToScheme(scheme)
	entered := make(chan struct{}, 1)
	reader := fake.NewClientBuilder().WithScheme(scheme).WithInterceptorFuncs(interceptor.Funcs{
		Get: func(ctx context.Context, _ client.WithWatch, _ client.ObjectKey, _ client.Object, _ ...client.GetOption) error {
			entered <- struct{}{}
			<-ctx.Done()
			return ctx.Err()
		},
	}).Build()
	request, _ := namespaceselector.NewRequest(reader, time.Second)
	first, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		request.Matches(first, "team", &metav1.LabelSelector{})
		close(done)
	}()
	<-entered
	second, stop := context.WithTimeout(context.Background(), 5*time.Millisecond)
	defer stop()
	if _, err := request.Matches(second, "other", &metav1.LabelSelector{}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	cancel()
	<-done
}
