// SPDX-FileCopyrightText: 2026 Deutsche Telekom AG
//
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"errors"
	"os"
	"slices"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
)

func TestRun(t *testing.T) {
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		t.Skip("KUBEBUILDER_ASSETS not set; run via `make test-examples`")
	}

	got, err := runEnvtest(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"finalizers: [example.com/cleanup]",
		"count: 4",
		"node condition: Maintenance=True",
		"deleted: true",
	}
	if !slices.Equal(got, want) {
		t.Errorf("report mismatch\ngot:  %q\nwant: %q", got, want)
	}
}

func TestRunReportsDeletionReadError(t *testing.T) {
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		t.Skip("KUBEBUILDER_ASSETS not set; run via `make test-examples`")
	}
	testEnv := &envtest.Environment{}
	cfg, err := testEnv.Start()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := testEnv.Stop(); err != nil {
			t.Errorf("stop envtest: %v", err)
		}
	})
	c, err := client.NewWithWatch(cfg, client.Options{})
	if err != nil {
		t.Fatal(err)
	}
	wantErr := errors.New("transport unavailable")
	wrapped := interceptor.NewClient(c, interceptor.Funcs{
		Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			err := c.Get(ctx, key, obj, opts...)
			if key.Name == "counter" && apierrors.IsNotFound(err) {
				return wantErr
			}
			return err
		},
	})
	out := &report{}
	if err := run(t.Context(), wrapped, out); !errors.Is(err, wantErr) {
		t.Fatalf("want deletion-check error, got %v", err)
	}
	if slices.Contains(out.lines, "deleted: false") {
		t.Error("unexpected errors must not be reported as successful runs")
	}
}
