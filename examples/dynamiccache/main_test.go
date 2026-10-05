// SPDX-FileCopyrightText: 2026 Deutsche Telekom AG
//
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
)

// syncBuffer is a goroutine-safe bytes.Buffer.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func TestRunWatchesOnlyLabelledNamespaces(t *testing.T) {
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

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	c, err := client.New(cfg, client.Options{})
	if err != nil {
		t.Fatal(err)
	}
	const label = "example.com/watched"
	for _, ns := range []*corev1.Namespace{
		{ObjectMeta: metav1.ObjectMeta{Name: "watched", Labels: map[string]string{label: "true"}}},
		{ObjectMeta: metav1.ObjectMeta{Name: "ignored"}},
	} {
		if err := c.Create(ctx, ns); err != nil {
			t.Fatal(err)
		}
		cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: ns.Name, Name: "demo"}}
		if err := c.Create(ctx, cm); err != nil {
			t.Fatal(err)
		}
	}

	out := &syncBuffer{}
	done := make(chan error, 1)
	kubeconfigPath := filepath.Join(".", "test-kubeconfig")
	t.Cleanup(func() { _ = os.Remove(kubeconfigPath) })
	kubeconfig := clientcmdapi.Config{
		Clusters: map[string]*clientcmdapi.Cluster{"test": {
			Server: cfg.Host, CertificateAuthorityData: cfg.CAData,
		}},
		AuthInfos: map[string]*clientcmdapi.AuthInfo{"test": {
			ClientCertificateData: cfg.CertData, ClientKeyData: cfg.KeyData,
		}},
		Contexts:       map[string]*clientcmdapi.Context{"test": {Cluster: "test", AuthInfo: "test"}},
		CurrentContext: "test",
	}
	if err := clientcmd.WriteToFile(kubeconfig, kubeconfigPath); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KUBECONFIG", filepath.Join(".", "nonexistent-default-kubeconfig"))
	go func() {
		done <- run(ctx, nil, out, []string{"--selector", label + "=true", "--kubeconfig", kubeconfigPath})
	}()

	waitFor := func(line string) {
		t.Helper()
		deadline := time.Now().Add(30 * time.Second)
		for !strings.Contains(out.String(), line) {
			if time.Now().After(deadline) {
				t.Fatalf("timed out waiting for %q; output:\n%s", line, out.String())
			}
			time.Sleep(100 * time.Millisecond)
		}
	}

	waitFor("reconciled ConfigMap watched/demo\n")
	// Give the manager a moment to (wrongly) pick up the unlabelled namespace.
	time.Sleep(time.Second)
	if strings.Contains(out.String(), "ignored/") {
		t.Fatalf("reconciled an object from an unselected namespace:\n%s", out.String())
	}

	// Labelling the namespace at runtime brings its objects into the cache.
	ignored := &corev1.Namespace{}
	if err := c.Get(ctx, client.ObjectKey{Name: "ignored"}, ignored); err != nil {
		t.Fatal(err)
	}
	ignored.Labels = map[string]string{label: "true"}
	if err := c.Update(ctx, ignored); err != nil {
		t.Fatal(err)
	}
	waitFor("reconciled ConfigMap ignored/demo\n")

	cancel()
	if err := <-done; err != nil {
		t.Fatalf("run: %v", err)
	}
}

func TestRunRejectsInvalidSelectors(t *testing.T) {
	for _, args := range [][]string{
		{"--selector", "!!invalid"},
		{"--selector", ""},
		{"--unknown-flag"},
	} {
		if err := run(context.Background(), nil, &bytes.Buffer{}, args); err == nil {
			t.Errorf("run(%q) succeeded, want error", args)
		}
	}
}

func TestRunRejectsUnavailableConfiguration(t *testing.T) {
	missing := filepath.Join("does-not-exist", "kubeconfig")
	if err := run(t.Context(), nil, &bytes.Buffer{}, []string{"--kubeconfig", missing}); err == nil {
		t.Fatal("missing kubeconfig was accepted")
	}
	if err := run(t.Context(), &rest.Config{Host: "://invalid"}, &bytes.Buffer{}, nil); err == nil {
		t.Fatal("invalid client configuration was accepted")
	}
}
