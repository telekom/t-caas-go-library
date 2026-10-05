// SPDX-FileCopyrightText: 2026 Deutsche Telekom AG
//
// SPDX-License-Identifier: Apache-2.0

package dynamiccache

import (
	"fmt"

	"k8s.io/apimachinery/pkg/runtime/schema"
)

// ErrClusterScopedUnsupported is returned for reads and informer requests on
// cluster-scoped types other than Namespace. With per-namespace RBAC such types
// cannot be watched; configure the manager's client to bypass the cache for
// them via client.Options.Cache.DisableFor.
type ErrClusterScopedUnsupported struct {
	GVK schema.GroupVersionKind
}

func (e *ErrClusterScopedUnsupported) Error() string {
	return fmt.Sprintf(
		"dynamiccache: cluster-scoped type %s is not supported by the dynamic namespace cache; "+
			"read it with a non-cached client (client.Options.Cache.DisableFor)", e.GVK)
}

// ErrCacheNotStarted is returned for reads before Start was called.
type ErrCacheNotStarted struct{}

func (*ErrCacheNotStarted) Error() string {
	return "dynamiccache: the cache is not started, can not read objects"
}

// ErrNamespaceNotReady is returned for reads touching a namespace that matches
// the selector but whose cache is not (yet) running — either because the
// namespace was selected moments ago, or because its cache failed to
// initialize and is awaiting retry. This is a transient error: retry.
// It is deliberately distinct from NotFound so a selected namespace in a
// degraded state is never mistaken for a deleted one.
type ErrNamespaceNotReady struct {
	Namespace string
}

func (e *ErrNamespaceNotReady) Error() string {
	return fmt.Sprintf("dynamiccache: namespace %q is selected but its cache is not ready yet, retry", e.Namespace)
}
