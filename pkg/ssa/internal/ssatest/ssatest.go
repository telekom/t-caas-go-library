// SPDX-FileCopyrightText: 2026 Deutsche Telekom AG
//
// SPDX-License-Identifier: Apache-2.0

// Package ssatest provides envtest and fault-injecting client helpers shared
// by the ssa test suites.
package ssatest

import (
	"context"
	"errors"
	"fmt"
	"os"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
)

// ErrNoAssets is returned by Start when KUBEBUILDER_ASSETS is not set.
var ErrNoAssets = errors.New("KUBEBUILDER_ASSETS is not set; run the tests via make test")

// Env is a running envtest API server with a client for core and RBAC types.
type Env struct {
	env    *envtest.Environment
	Client client.Client
}

// Start starts an envtest API server.
func Start() (*Env, error) {
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		return nil, ErrNoAssets
	}
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		return nil, fmt.Errorf("add core/v1 to scheme: %w", err)
	}
	if err := rbacv1.AddToScheme(scheme); err != nil {
		return nil, fmt.Errorf("add rbac/v1 to scheme: %w", err)
	}
	env := &envtest.Environment{}
	cfg, err := env.Start()
	if err != nil {
		return nil, fmt.Errorf("start envtest: %w", err)
	}
	c, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		_ = env.Stop()
		return nil, fmt.Errorf("create client: %w", err)
	}
	return &Env{env: env, Client: c}, nil
}

// Stop stops the API server. It is safe to call on a nil Env.
func (e *Env) Stop() error {
	if e == nil {
		return nil
	}
	return e.env.Stop()
}

// Client wraps a client.Client, counts Apply and Patch calls and lets tests
// inject behaviour into Get, Apply and Patch. A nil hook delegates to the
// wrapped client.
type Client struct {
	client.Client
	ApplyCalls int
	PatchCalls int

	OnGet   func(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error
	OnApply func(ctx context.Context, obj runtime.ApplyConfiguration, opts ...client.ApplyOption) error
	OnPatch func(ctx context.Context, obj client.Object, patch client.Patch, opts ...client.PatchOption) error
}

// Get implements client.Client.
func (c *Client) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	if c.OnGet != nil {
		return c.OnGet(ctx, key, obj, opts...)
	}
	return c.Client.Get(ctx, key, obj, opts...)
}

// Apply implements client.Client.
func (c *Client) Apply(ctx context.Context, obj runtime.ApplyConfiguration, opts ...client.ApplyOption) error {
	c.ApplyCalls++
	if c.OnApply != nil {
		return c.OnApply(ctx, obj, opts...)
	}
	return c.Client.Apply(ctx, obj, opts...)
}

// Patch implements client.Client.
func (c *Client) Patch(ctx context.Context, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
	c.PatchCalls++
	if c.OnPatch != nil {
		return c.OnPatch(ctx, obj, patch, opts...)
	}
	return c.Client.Patch(ctx, obj, patch, opts...)
}
