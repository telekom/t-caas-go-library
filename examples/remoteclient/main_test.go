// SPDX-FileCopyrightText: 2026 Deutsche Telekom AG
//
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/telekom/t-caas-go-library/pkg/remoteclient"
)

func TestRun(t *testing.T) {
	if err := run(context.Background(), remoteclient.Options{Factory: offlineClient}); err != nil {
		t.Fatal(err)
	}
}

func Example() {
	main()
	// Output: Secret-backed client loaded and invalidated
}

func TestRunFailures(t *testing.T) {
	if err := run(t.Context(), remoteclient.Options{QPS: -1}); err == nil {
		t.Fatal("invalid registry configuration was accepted")
	}
	boom := errors.New("remote client unavailable")
	factory := func(context.Context, *rest.Config, *http.Client) (client.Client, error) { return nil, boom }
	if err := run(t.Context(), remoteclient.Options{Factory: factory}); !errors.Is(err, boom) {
		t.Fatalf("factory failure lost cause: %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := run(ctx, remoteclient.Options{Factory: offlineClient}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled refresh: %v", err)
	}
}
