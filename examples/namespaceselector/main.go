// SPDX-FileCopyrightText: 2026 Deutsche Telekom AG
//
// SPDX-License-Identifier: Apache-2.0

// Command namespaceselector demonstrates request-local namespace evaluation.
// Replace the fake client with manager.GetAPIReader() for live labels.
package main

import (
	"context"
	"fmt"
	"log"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/telekom/t-caas-go-library/pkg/namespaceselector"
)

const (
	demoNamespace = "demo"
	teamLabel     = "team"
	platformTeam  = "platform"
)

func run(ctx context.Context, reader client.Reader) (bool, error) {
	request, err := namespaceselector.NewRequest(reader, time.Second)
	if err != nil {
		return false, err
	}
	return request.Matches(ctx, demoNamespace, &metav1.LabelSelector{MatchLabels: map[string]string{teamLabel: platformTeam}})
}

func main() {
	reader := fake.NewClientBuilder().WithObjects(&corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: demoNamespace, Labels: map[string]string{teamLabel: platformTeam}},
	}).Build()
	matched, err := run(context.Background(), reader)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(matched)
}
