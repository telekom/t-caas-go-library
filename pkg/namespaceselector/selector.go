// SPDX-FileCopyrightText: 2026 Deutsche Telekom AG
//
// SPDX-License-Identifier: Apache-2.0

// Package namespaceselector evaluates selectors using live namespace labels.
// A Request memoizes both reads and read errors only for one evaluation request.
package namespaceselector

import (
	"context"
	"fmt"
	"maps"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type entry struct {
	labels labels.Set
	err    error
}

// Request is a concurrency-safe, request-scoped namespace label cache.
// Create a new Request for each authorization/admission evaluation; do not
// share it across requests. Use an uncached API reader for live-label semantics.
type Request struct {
	reader  client.Reader
	timeout time.Duration
	gate    chan struct{}
	cache   map[string]entry
}

// NewRequest constructs a request with a required reader and positive per-read
// timeout. The caller's context can impose a shorter deadline.
func NewRequest(reader client.Reader, timeout time.Duration) (*Request, error) {
	if reader == nil {
		return nil, fmt.Errorf("namespace reader is required")
	}
	if timeout <= 0 {
		return nil, fmt.Errorf("namespace read timeout must be positive")
	}
	return &Request{reader: reader, timeout: timeout, cache: make(map[string]entry), gate: make(chan struct{}, 1)}, nil
}

// Matches evaluates a Kubernetes label selector. A nil selector matches no
// namespaces; an empty nonnil selector matches every existing namespace.
// An empty namespace (cluster-scoped request) never matches and is not read.
// Missing namespaces and all other read errors are returned (and memoized),
// preserving errors.Is and apierrors.IsNotFound. Invalid selectors fail before
// I/O. Label snapshots remain fixed within this Request even if labels change.
func (r *Request) Matches(ctx context.Context, namespace string, selector *metav1.LabelSelector) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if namespace == "" || selector == nil {
		return false, nil
	}
	parsed, err := metav1.LabelSelectorAsSelector(selector)
	if err != nil {
		return false, fmt.Errorf("parse namespace selector: %w", err)
	}
	cached := r.namespace(ctx, namespace)
	if cached.err != nil {
		return false, cached.err
	}
	return parsed.Matches(cached.labels), nil
}

func (r *Request) namespace(ctx context.Context, name string) entry {
	// ponytail: serialize request-local reads; use per-namespace coalescing if
	// callers routinely evaluate many namespaces concurrently in one request.
	select {
	case r.gate <- struct{}{}:
		defer func() { <-r.gate }()
	case <-ctx.Done():
		return entry{err: ctx.Err()}
	}
	if err := ctx.Err(); err != nil {
		return entry{err: err}
	}
	if cached, ok := r.cache[name]; ok {
		return cached
	}
	readCtx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()
	var namespace corev1.Namespace
	cached := entry{}
	if err := r.reader.Get(readCtx, types.NamespacedName{Name: name}, &namespace); err != nil {
		cached.err = fmt.Errorf("get namespace %q: %w", name, err)
	} else {
		cached.labels = labels.Set(maps.Clone(namespace.Labels))
	}
	r.cache[name] = cached
	return cached
}
