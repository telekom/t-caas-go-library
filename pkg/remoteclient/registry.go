// SPDX-FileCopyrightText: 2026 Deutsche Telekom AG
//
// SPDX-License-Identifier: Apache-2.0

// Package remoteclient manages remote Kubernetes clients and their Secret dependencies.
//
// Invalidation prevents in-flight refreshes from repopulating an invalidated entry.
// It does not revoke clients already returned by Get or cancel active requests.
// Callers must enforce authorization and security freshness checks themselves,
// deliver Secret change/delete events, and invalidate on cluster-reference changes.
// Use a live API reader for credential loading when cache staleness is unacceptable.
package remoteclient

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"math"
	"net/http"
	"slices"
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// ErrSuperseded indicates that a newer refresh or invalidation won the race.
var ErrSuperseded = errors.New("refresh superseded")

// ErrInvalidKubeconfig is returned without parser details, which can contain credentials.
var ErrInvalidKubeconfig = errors.New("invalid or non-self-contained kubeconfig")

// Factory constructs a client using the supplied configuration and owned HTTP client.
// It must honor ctx and must not retain or mutate config after returning.
type Factory func(ctx context.Context, config *rest.Config, httpClient *http.Client) (client.Client, error)

// Options configures a registry. Callbacks must be safe for concurrent use.
type Options struct {
	// Scheme is used by the default controller-runtime client factory.
	Scheme *runtime.Scheme
	// QPS and Burst use client-go defaults when zero. QPS must be finite; both must be non-negative.
	QPS   float32
	Burst int
	// Timeout is the per-request timeout; zero explicitly means no timeout.
	Timeout time.Duration
	// Factory overrides construction, for example for testing.
	Factory Factory
	// WrapTransport adds middleware, such as a caller-owned circuit breaker.
	WrapTransport func(http.RoundTripper) http.RoundTripper
	// OnInvalidate runs outside the registry lock after removal, including Secret invalidation.
	// It may be invoked even when only a pending refresh existed.
	OnInvalidate func(types.NamespacedName)
}

type entry struct {
	client client.Client
	config *rest.Config
	close  func()
}

// Registry is a concurrency-safe registry. Construct it with New; do not copy it.
type Registry struct {
	mu           sync.RWMutex
	options      Options
	sequence     uint64
	generations  map[types.NamespacedName]uint64
	entries      map[types.NamespacedName]*entry
	dependencies map[types.NamespacedName]map[types.NamespacedName]struct{}
}

// New constructs an empty registry and validates request tunables.
func New(options Options) (*Registry, error) {
	if options.QPS < 0 || math.IsNaN(float64(options.QPS)) || math.IsInf(float64(options.QPS), 0) ||
		options.Burst < 0 || options.Timeout < 0 {
		return nil, errors.New("QPS must be finite and non-negative; burst and timeout must be non-negative")
	}
	if options.Factory == nil {
		options.Factory = func(ctx context.Context, cfg *rest.Config, hc *http.Client) (client.Client, error) {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			return client.New(cfg, client.Options{Scheme: options.Scheme, HTTPClient: hc})
		}
	}
	return &Registry{
		options: options, generations: make(map[types.NamespacedName]uint64),
		entries:      make(map[types.NamespacedName]*entry),
		dependencies: make(map[types.NamespacedName]map[types.NamespacedName]struct{}),
	}, nil
}

// Get returns the current client and whether it exists. Already returned clients
// remain usable after removal; removal only closes idle transport connections.
func (r *Registry) Get(key types.NamespacedName) (client.Client, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	e, ok := r.entries[key]
	if !ok {
		return nil, false
	}
	return e.client, true
}

// Config returns an independent copy of the current REST configuration.
// It contains credentials: do not log it. Absence is indicated by false.
func (r *Registry) Config(key types.NamespacedName) (*rest.Config, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	e, ok := r.entries[key]
	if !ok {
		return nil, false
	}
	return copyConfig(e.config), true
}

// Refresh constructs and replaces a client from self-contained kubeconfig bytes.
// The latest started refresh wins, not the latest completed refresh. A failed
// refresh preserves the previous client, but still fences earlier refreshes.
// Exec/auth-provider plugins and filesystem credential references are rejected.
func (r *Registry) Refresh(ctx context.Context, key types.NamespacedName, data []byte) error {
	version, err := r.begin(key)
	if err != nil {
		return err
	}
	return r.build(ctx, key, version, data, nil)
}

func (r *Registry) begin(key types.NamespacedName) (uint64, error) {
	if key.Name == "" || key.Namespace == "" {
		return 0, errors.New("cluster namespace and name are required")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sequence++
	r.generations[key] = r.sequence
	return r.sequence, nil
}

func (r *Registry) build(
	ctx context.Context, key types.NamespacedName, version uint64, data []byte, secret *types.NamespacedName,
) error {
	cfg, err := parse(data)
	if err != nil {
		return err
	}
	cfg.QPS, cfg.Burst, cfg.Timeout = r.options.QPS, r.options.Burst, r.options.Timeout
	cfg.RateLimiter = nil
	var owned *http.Transport
	var closeMiddleware func()
	cfg.WrapTransport = func(base http.RoundTripper) http.RoundTripper {
		// client-go caches transports globally. Clone to avoid closing another registry's idle connections.
		if transport, ok := base.(*http.Transport); ok {
			owned = transport.Clone()
			base = owned
		}
		if r.options.WrapTransport != nil {
			wrapped := r.options.WrapTransport(base)
			if closer, ok := wrapped.(interface{ CloseIdleConnections() }); ok {
				closeMiddleware = closer.CloseIdleConnections
			}
			return wrapped
		}
		return base
	}
	transport, err := rest.TransportFor(cfg)
	if err != nil {
		return fmt.Errorf("construct remote transport: %w", err)
	}
	hc := &http.Client{Transport: transport, Timeout: cfg.Timeout}
	closeIdle := func() {
		hc.CloseIdleConnections()
		if closeMiddleware != nil {
			closeMiddleware()
		}
		if owned != nil {
			owned.CloseIdleConnections()
		}
	}
	c, err := r.options.Factory(ctx, copyConfig(cfg), hc)
	if err != nil {
		closeIdle()
		return fmt.Errorf("construct remote client: %w", err)
	}
	if c == nil {
		closeIdle()
		return errors.New("factory returned a nil client")
	}
	if err := ctx.Err(); err != nil {
		closeIdle()
		return fmt.Errorf("refresh canceled: %w", err)
	}
	// Do not expose the transport-building closure or its owned mutable state.
	cfg.WrapTransport = r.options.WrapTransport
	r.mu.Lock()
	if r.generations[key] != version {
		r.mu.Unlock()
		closeIdle()
		return ErrSuperseded
	}
	old := r.entries[key]
	r.entries[key] = &entry{client: c, config: copyConfig(cfg), close: closeIdle}
	if secret == nil {
		delete(r.dependencies, key)
	} else {
		r.dependencies[key] = map[types.NamespacedName]struct{}{*secret: {}}
	}
	r.mu.Unlock()
	if old != nil {
		old.close()
	}
	return nil
}

func parse(data []byte) (*rest.Config, error) {
	raw, err := clientcmd.Load(data)
	if err != nil {
		return nil, ErrInvalidKubeconfig
	}
	for _, cluster := range raw.Clusters {
		if cluster.CertificateAuthority != "" {
			return nil, ErrInvalidKubeconfig
		}
	}
	for _, auth := range raw.AuthInfos {
		if auth.Exec != nil || auth.AuthProvider != nil || auth.TokenFile != "" ||
			auth.ClientCertificate != "" || auth.ClientKey != "" {
			return nil, ErrInvalidKubeconfig
		}
	}
	cfg, err := clientcmd.RESTConfigFromKubeConfig(data)
	if err != nil {
		return nil, ErrInvalidKubeconfig
	}
	return cfg, nil
}

func copyConfig(cfg *rest.Config) *rest.Config {
	cloned := rest.CopyConfig(cfg)
	cloned.CAData = append([]byte(nil), cfg.CAData...)
	cloned.CertData = append([]byte(nil), cfg.CertData...)
	cloned.KeyData = append([]byte(nil), cfg.KeyData...)
	cloned.Impersonate.Groups = slices.Clone(cfg.Impersonate.Groups)
	cloned.Impersonate.Extra = maps.Clone(cfg.Impersonate.Extra)
	for key, values := range cloned.Impersonate.Extra {
		cloned.Impersonate.Extra[key] = slices.Clone(values)
	}
	return cloned
}

// Remove invalidates a client, fences pending refreshes and closes idle connections.
// The hook is called outside the lock; it must not assume the entry remains absent.
func (r *Registry) Remove(key types.NamespacedName) {
	r.mu.Lock()
	old := r.removeLocked(key)
	r.mu.Unlock()
	r.notify(key, old)
}

func (r *Registry) removeLocked(key types.NamespacedName) *entry {
	old := r.entries[key]
	delete(r.entries, key)
	delete(r.generations, key)
	delete(r.dependencies, key)
	return old
}

func (r *Registry) notify(key types.NamespacedName, old *entry) {
	if old != nil {
		old.close()
	}
	if r.options.OnInvalidate != nil {
		r.options.OnInvalidate(key)
	}
}
