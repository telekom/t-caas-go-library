// SPDX-FileCopyrightText: 2026 Deutsche Telekom AG
//
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"errors"
	"testing"
)

func TestRun(t *testing.T) {
	snapshot, err := run(context.Background(), source{})
	if err != nil || len(snapshot["v1"]) != 1 || snapshot["v1"][0].Name != "pods" {
		t.Fatalf("snapshot=%v err=%v", snapshot, err)
	}
}

func Example() {
	main()
	// Output: pods
}

func TestRunFailures(t *testing.T) {
	if _, err := run(t.Context(), nil); err == nil {
		t.Fatal("nil discovery source was accepted")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := run(ctx, source{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled discovery: %v", err)
	}
}
