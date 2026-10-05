// SPDX-FileCopyrightText: 2026 Deutsche Telekom AG
//
// SPDX-License-Identifier: Apache-2.0

package ssa_test

import (
	"context"
	"errors"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/telekom/t-caas-go-library/pkg/ssa/internal/ssatest"
)

var (
	testCtx   = context.Background()
	testEnv   *ssatest.Env
	k8sClient client.Client
)

func TestSSA(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "SSA Suite")
}

var _ = BeforeSuite(func() {
	var err error
	testEnv, err = ssatest.Start()
	if errors.Is(err, ssatest.ErrNoAssets) {
		Skip(err.Error())
	}
	Expect(err).NotTo(HaveOccurred())
	k8sClient = testEnv.Client
})

var _ = AfterSuite(func() {
	Expect(testEnv.Stop()).To(Succeed())
})

// counting returns a fault-injectable client that counts Apply calls.
func counting() *ssatest.Client {
	return &ssatest.Client{Client: k8sClient}
}
