// SPDX-FileCopyrightText: 2026 Deutsche Telekom AG
//
// SPDX-License-Identifier: Apache-2.0

package dynamiccache_test

// Contract 1: constructor option validation. These are pure unit specs — they
// exercise only the argument checks at the top of New and never talk to the
// envtest control plane (the dummy rest.Config is never dialed).

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"

	dynamiccache "github.com/telekom/t-caas-go-library/pkg/controllerruntime/dynamiccache"
)

var _ = Describe("New option validation", func() {
	// Never dialed: validation fails before any client is constructed.
	dummyCfg := &rest.Config{Host: "http://127.0.0.1:1"}
	validOpts := func() dynamiccache.Options {
		return dynamiccache.Options{NamespaceSelector: newSelector("validation")}
	}

	It("rejects a nil rest.Config", func() {
		c, err := dynamiccache.New(nil, cache.Options{}, validOpts())
		Expect(err).To(MatchError(ContainSubstring("rest.Config must not be nil")))
		Expect(c).To(BeNil())
	})

	It("rejects a nil namespace selector", func() {
		c, err := dynamiccache.New(dummyCfg, cache.Options{}, dynamiccache.Options{})
		Expect(err).To(MatchError(ContainSubstring("NamespaceSelector must be set and non-empty")))
		Expect(c).To(BeNil())
	})

	It("rejects an empty (select-everything) namespace selector", func() {
		c, err := dynamiccache.New(dummyCfg, cache.Options{}, dynamiccache.Options{
			NamespaceSelector: labels.Everything(),
		})
		Expect(err).To(MatchError(ContainSubstring("NamespaceSelector must be set and non-empty")))
		Expect(c).To(BeNil())
	})

	It("rejects labels.Nothing because its empty serialization would select every Namespace on the server", func() {
		selector := labels.Nothing()
		Expect(selector.Empty()).To(BeFalse())
		Expect(selector.String()).To(BeEmpty())
		c, err := dynamiccache.New(dummyCfg, cache.Options{}, dynamiccache.Options{NamespaceSelector: selector})
		Expect(err).To(MatchError(ContainSubstring("NamespaceSelector must be set and non-empty")))
		Expect(c).To(BeNil())
	})

	It("rejects preset cache.Options.DefaultNamespaces", func() {
		c, err := dynamiccache.New(dummyCfg, cache.Options{
			DefaultNamespaces: map[string]cache.Config{"default": {}},
		}, validOpts())
		Expect(err).To(MatchError(ContainSubstring("DefaultNamespaces is owned by dynamiccache")))
		Expect(c).To(BeNil())
	})

	It("rejects non-nil ByObject.Namespaces, including empty maps and cluster-scoped entries", func() {
		for _, obj := range []client.Object{&corev1.ConfigMap{}, &rbacv1.ClusterRole{}} {
			for _, namespaces := range []map[string]cache.Config{{}, {"unselected": {}}} {
				c, err := dynamiccache.New(dummyCfg, cache.Options{
					ByObject: map[client.Object]cache.ByObject{obj: {Namespaces: namespaces}},
				}, validOpts())
				Expect(err).To(MatchError(ContainSubstring("Namespaces is owned by dynamiccache")))
				Expect(c).To(BeNil())
			}
		}
	})

	It("propagates validation errors through NewCacheFunc", func() {
		c, err := dynamiccache.NewCacheFunc(dynamiccache.Options{})(dummyCfg, cache.Options{})
		Expect(err).To(MatchError(ContainSubstring("NamespaceSelector must be set and non-empty")))
		Expect(c).To(BeNil())
	})
})
