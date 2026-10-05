// SPDX-FileCopyrightText: 2026 Deutsche Telekom AG
//
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"errors"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestRun(t *testing.T) {
	reader := fake.NewClientBuilder().WithObjects(&corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: demoNamespace, Labels: map[string]string{teamLabel: platformTeam}},
	}).Build()
	if matched, err := run(context.Background(), reader); err != nil || !matched {
		t.Fatalf("matched=%t err=%v", matched, err)
	}
}

func Example() {
	main()
	// Output: true
}

func TestRunFailures(t *testing.T) {
	if _, err := run(t.Context(), nil); err == nil {
		t.Fatal("nil reader was accepted")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := run(ctx, fake.NewClientBuilder().Build()); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled request: %v", err)
	}
}
