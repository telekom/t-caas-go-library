// SPDX-FileCopyrightText: 2026 Deutsche Telekom AG
//
// SPDX-License-Identifier: Apache-2.0

package ssatest

import (
	"context"
	"errors"
	"os"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	corev1ac "k8s.io/client-go/applyconfigurations/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func TestEnvironmentAndDelegation(t *testing.T) {
	assets := os.Getenv("KUBEBUILDER_ASSETS")
	t.Setenv("KUBEBUILDER_ASSETS", "")
	if _, err := Start(); !errors.Is(err, ErrNoAssets) {
		t.Fatalf("missing assets: %v", err)
	}
	if err := (*Env)(nil).Stop(); err != nil {
		t.Fatalf("nil environment: %v", err)
	}
	t.Setenv("KUBEBUILDER_ASSETS", assets)
	if assets == "" {
		t.Skip("KUBEBUILDER_ASSETS not set; run via make test")
	}
	env, err := Start()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := env.Stop(); err != nil {
			t.Errorf("stop environment: %v", err)
		}
	})
	c := &Client{Client: env.Client}
	ctx := t.Context()
	if err := c.Apply(ctx, corev1ac.ConfigMap("audit", "default").WithData(map[string]string{"state": "initial"}),
		client.FieldOwner("audit")); err != nil {
		t.Fatal(err)
	}
	cm := &corev1.ConfigMap{}
	key := client.ObjectKey{Namespace: "default", Name: "audit"}
	if err := c.Get(ctx, key, cm); err != nil {
		t.Fatal(err)
	}
	base := cm.DeepCopy()
	cm.Data["state"] = "patched"
	if err := c.Patch(ctx, cm, client.MergeFrom(base)); err != nil {
		t.Fatal(err)
	}
	if err := c.Get(ctx, key, cm); err != nil || cm.Data["state"] != "patched" {
		t.Fatalf("patch delegation: data=%v err=%v", cm.Data, err)
	}
	if c.ApplyCalls != 1 || c.PatchCalls != 1 {
		t.Fatalf("counts: apply=%d patch=%d", c.ApplyCalls, c.PatchCalls)
	}
}

func TestFaultHooks(t *testing.T) {
	boom := errors.New("injected failure")
	c := &Client{
		OnGet: func(context.Context, client.ObjectKey, client.Object, ...client.GetOption) error {
			return boom
		},
		OnApply: func(context.Context, runtime.ApplyConfiguration, ...client.ApplyOption) error {
			return boom
		},
		OnPatch: func(context.Context, client.Object, client.Patch, ...client.PatchOption) error {
			return boom
		},
	}

	obj := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "audit", Namespace: "default"}}
	for _, err := range []error{
		c.Get(t.Context(), client.ObjectKeyFromObject(obj), obj),
		c.Apply(t.Context(), corev1ac.ConfigMap("audit", "default")),
		c.Patch(t.Context(), obj, client.MergeFrom(obj.DeepCopy())),
	} {
		if !errors.Is(err, boom) {
			t.Fatalf("hook error lost cause: %v", err)
		}
	}
	if c.ApplyCalls != 1 || c.PatchCalls != 1 {
		t.Fatalf("failed calls not counted: apply=%d patch=%d", c.ApplyCalls, c.PatchCalls)
	}
}

func TestStartFailure(t *testing.T) {
	t.Setenv("KUBEBUILDER_ASSETS", t.TempDir())
	t.Setenv("KUBEBUILDER_CONTROLPLANE_START_TIMEOUT", "100ms")
	if env, err := Start(); err == nil || env != nil {
		t.Fatalf("missing binaries must fail: env=%v err=%v", env, err)
	}
}
