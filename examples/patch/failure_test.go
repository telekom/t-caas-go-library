// SPDX-FileCopyrightText: 2026 Deutsche Telekom AG
//
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

func TestRunFailures(t *testing.T) {
	boom := errors.New("injected client failure")
	for _, stage := range []string{"create ConfigMap", "ensure finalizer", "increment counter", "create Node", "patch Node status",
		"delete ConfigMap", "get ConfigMap after deletion", "remove finalizer"} {
		t.Run(stage, func(t *testing.T) {
			c := fake.NewClientBuilder().WithStatusSubresource(&corev1.Node{}).
				WithInterceptorFuncs(interceptor.Funcs{
					Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
						if _, node := obj.(*corev1.Node); stage == "create Node" && node {
							return boom
						}
						if stage == "create ConfigMap" {
							return boom
						}
						return c.Create(ctx, obj, opts...)
					},
					Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
						err := c.Get(ctx, key, obj, opts...)
						if key.Name == counterName && (stage == "increment counter" ||
							(stage == "get ConfigMap after deletion" && obj.GetDeletionTimestamp() != nil)) {
							return boom
						}
						return err
					},
					Patch: func(ctx context.Context, c client.WithWatch, obj client.Object, p client.Patch, opts ...client.PatchOption) error {
						if stage == "ensure finalizer" || (stage == "remove finalizer" && obj.GetDeletionTimestamp() != nil) {
							return boom
						}
						return c.Patch(ctx, obj, p, opts...)
					},
					SubResourcePatch: func(ctx context.Context, c client.Client, sub string, obj client.Object,
						p client.Patch, opts ...client.SubResourcePatchOption) error {
						if stage == "patch Node status" {
							return boom
						}
						return c.SubResource(sub).Patch(ctx, obj, p, opts...)
					},
					Delete: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.DeleteOption) error {
						if stage == "delete ConfigMap" {
							return boom
						}
						return c.Delete(ctx, obj, opts...)
					},
				}).Build()
			if err := run(t.Context(), c, &report{}); !errors.Is(err, boom) {
				t.Fatalf("stage %q lost error: %v", stage, err)
			}
		})
	}
}

func TestIncrementRejectsCorruptCounter(t *testing.T) {
	cm := &corev1.ConfigMap{Data: map[string]string{"count": "not-a-number"}}
	cm.Name, cm.Namespace = counterName, namespace
	c := fake.NewClientBuilder().WithObjects(cm).Build()
	if err := incrementConcurrently(t.Context(), c, client.ObjectKeyFromObject(cm)); err == nil ||
		!strings.Contains(err.Error(), "parse count") {
		t.Fatalf("corrupt count error: %v", err)
	}
}

func TestMainOutput(t *testing.T) {
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		t.Skip("KUBEBUILDER_ASSETS not set; run via make test-examples")
	}
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	previous := os.Stdout
	os.Stdout = writer
	t.Cleanup(func() { os.Stdout = previous })
	main()
	os.Stdout = previous
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	output, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"count: 4", "node condition: Maintenance=True", "deleted: true"} {
		if !strings.Contains(string(output), want) {
			t.Errorf("missing %q in %q", want, output)
		}
	}
}
