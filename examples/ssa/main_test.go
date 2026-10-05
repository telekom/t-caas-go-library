// SPDX-FileCopyrightText: 2026 Deutsche Telekom AG
//
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
)

func TestRun(t *testing.T) {
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		t.Skip("KUBEBUILDER_ASSETS is not set; run via make test-examples")
	}
	env := &envtest.Environment{}
	cfg, err := env.Start()
	if err != nil {
		t.Fatalf("start envtest: %v", err)
	}
	t.Cleanup(func() {
		if err := env.Stop(); err != nil {
			t.Errorf("stop envtest: %v", err)
		}
	})
	ctx := context.Background()

	var out bytes.Buffer
	args := []string{"--namespace", "default", "--name", "demo", "--data", "mode=fast"}
	if err := run(ctx, cfg, &out, args); err != nil {
		t.Fatalf("run: %v", err)
	}
	if want := "ConfigMap default/demo created\nConfigMap default/demo skipped\n"; out.String() != want {
		t.Errorf("output = %q, want %q", out.String(), want)
	}

	out.Reset()
	if err := run(ctx, cfg, &out, []string{"--name", "demo", "--data", "mode=safe"}); err != nil {
		t.Fatalf("run: %v", err)
	}
	if want := "ConfigMap default/demo patched\nConfigMap default/demo skipped\n"; out.String() != want {
		t.Errorf("output = %q, want %q", out.String(), want)
	}

	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	c, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatal(err)
	}
	var cm corev1.ConfigMap
	if err := c.Get(ctx, types.NamespacedName{Namespace: "default", Name: "demo"}, &cm); err != nil {
		t.Fatal(err)
	}
	if cm.Data["mode"] != "safe" {
		t.Errorf("data = %v, want mode=safe", cm.Data)
	}

	wantErr := errors.New("output unavailable")
	if err := run(t.Context(), cfg, failingOutput{err: wantErr}, args); !errors.Is(err, wantErr) {
		t.Fatalf("writer error lost cause: %v", err)
	}
	cancelled, cancel := context.WithCancel(t.Context())
	cancel()
	if err := run(cancelled, cfg, io.Discard, args); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled apply: %v", err)
	}
}

type failingOutput struct{ err error }

func (w failingOutput) Write([]byte) (int, error) { return 0, w.err }

func TestRunRejectsInvalidClientConfig(t *testing.T) {
	if err := run(t.Context(), &rest.Config{Host: "://invalid"}, io.Discard, nil); err == nil {
		t.Fatal("invalid client configuration was accepted")
	}
}

func TestRunRejectsInvalidFlags(t *testing.T) {
	var out bytes.Buffer
	if err := run(context.Background(), nil, &out, []string{"--data", "novalue"}); err == nil {
		t.Error("run accepted --data without '='")
	}
}

func TestRunUsesKubeconfigFlag(t *testing.T) {
	t.Setenv("KUBECONFIG", "")
	missing := filepath.Join("does-not-exist", "missing-kubeconfig")
	err := run(context.Background(), nil, io.Discard, []string{"--kubeconfig", missing})
	if err == nil || !strings.Contains(err.Error(), "missing-kubeconfig") {
		t.Errorf("run did not load --kubeconfig: %v", err)
	}
}

func TestKeyValuesString(t *testing.T) {
	kv := keyValues{"a": "1"}
	if got := kv.String(); got != "a=1" {
		t.Errorf("String() = %q", got)
	}
}
