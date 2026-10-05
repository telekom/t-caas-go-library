// SPDX-FileCopyrightText: 2026 Deutsche Telekom AG
//
// SPDX-License-Identifier: Apache-2.0

// Package tracker maintains discovery snapshots using polling and CRD watches.
// No synthetic resources or RBAC verbs are added unless a Transform hook does so.
package tracker

import (
	"context"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Source is the context-aware client-go discovery method used by the tracker.
// A discovery.DiscoveryClient implements it directly. Partial resource results
// must accompany discovery.ErrGroupDiscoveryFailed; other errors are ignored.
type Source interface {
	ServerGroupsAndResourcesWithContext(ctx context.Context) ([]*metav1.APIGroup, []*metav1.APIResourceList, error)
}
