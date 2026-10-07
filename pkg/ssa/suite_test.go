// SPDX-FileCopyrightText: 2026 Deutsche Telekom AG
//
// SPDX-License-Identifier: Apache-2.0

package ssa_test

import (
	"context"
	"os"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
)

var (
	testCtx   = context.Background()
	testEnv   *envtest.Environment
	k8sClient client.Client
)

func TestSSA(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "SSA Suite")
}

var _ = BeforeSuite(func() {
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		Skip("KUBEBUILDER_ASSETS is not set; run the tests via make test")
	}
	testEnv = &envtest.Environment{}
	cfg, err := testEnv.Start()
	Expect(err).NotTo(HaveOccurred())
	k8sClient, err = client.New(cfg, client.Options{})
	Expect(err).NotTo(HaveOccurred())
})

var _ = AfterSuite(func() {
	if testEnv != nil {
		Expect(testEnv.Stop()).To(Succeed())
	}
})

// counting returns a fault-injectable client that counts Apply calls.
func counting() *countingClient {
	return &countingClient{Client: k8sClient}
}

type countingClient struct {
	client.Client
	ApplyCalls int
	OnGet      func(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error
	OnApply    func(ctx context.Context, obj runtime.ApplyConfiguration, opts ...client.ApplyOption) error
}

// Get implements client.Client.
func (c *countingClient) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	if c.OnGet != nil {
		return c.OnGet(ctx, key, obj, opts...)
	}
	return c.Client.Get(ctx, key, obj, opts...)
}

// Apply implements client.Client.
func (c *countingClient) Apply(ctx context.Context, obj runtime.ApplyConfiguration, opts ...client.ApplyOption) error {
	c.ApplyCalls++
	if c.OnApply != nil {
		return c.OnApply(ctx, obj, opts...)
	}
	return c.Client.Apply(ctx, obj, opts...)
}
