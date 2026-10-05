// SPDX-FileCopyrightText: 2026 Deutsche Telekom AG
//
// SPDX-License-Identifier: Apache-2.0

package namespaceselector_test

import (
	"context"
	"os"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	"github.com/telekom/t-caas-go-library/pkg/namespaceselector"
)

func TestLiveLabelsEnvtest(t *testing.T) {
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		t.Skip("run make test to install and configure envtest")
	}
	environment := &envtest.Environment{}
	config, err := environment.Start()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := environment.Stop(); err != nil {
			t.Error(err)
		}
	})
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	reader, err := client.New(config, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	namespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
		Name: "team", Labels: map[string]string{"team": "one"},
	}}
	if err := reader.Create(ctx, namespace); err != nil {
		t.Fatal(err)
	}
	request, err := namespaceselector.NewRequest(reader, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	selector := &metav1.LabelSelector{MatchLabels: map[string]string{"team": "one"}}
	if matched, err := request.Matches(ctx, namespace.Name, selector); err != nil || !matched {
		t.Fatalf("initial read: matched=%t err=%v", matched, err)
	}
	namespace.Labels["team"] = "two"
	if err := reader.Update(ctx, namespace); err != nil {
		t.Fatal(err)
	}
	if matched, err := request.Matches(ctx, namespace.Name, selector); err != nil || !matched {
		t.Fatalf("memoized read: matched=%t err=%v", matched, err)
	}
	next, err := namespaceselector.NewRequest(reader, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if matched, err := next.Matches(ctx, namespace.Name, selector); err != nil || matched {
		t.Fatalf("fresh request: matched=%t err=%v", matched, err)
	}
}
