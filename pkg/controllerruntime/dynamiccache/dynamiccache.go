// SPDX-FileCopyrightText: 2026 Deutsche Telekom AG
//
// SPDX-License-Identifier: Apache-2.0

// Package dynamiccache provides a controller-runtime cache.Cache implementation
// that watches objects only in namespaces selected by a label selector, and
// dynamically starts and stops per-namespace informers as namespaces gain or
// lose the selecting labels.
//
// The Kubernetes API server cannot list or watch namespaced resources across
// "namespaces matching a label selector" — a LIST/WATCH is either cluster-wide
// or scoped to a single namespace. This cache therefore runs one cluster-scoped
// informer on Namespace objects (filtered server-side by the selector, the only
// cluster-scoped permission required) and one single-namespace cache per
// matching namespace. RBAC for the per-namespace watches is expected to be
// provisioned externally, e.g. by auth-operator BindDefinitions using the same
// namespace selector.
//
// Semantics:
//
//   - Objects in namespaces that match the selector are cached and served.
//   - When a namespace starts matching, its informers are started and all
//     previously registered event handlers receive synthetic ADD events for the
//     namespace's existing objects (standard client-go replay behavior).
//   - When a namespace stops matching (labels changed or namespace deleted),
//     its informers are stopped and all registered event handlers receive
//     synthetic DELETE events (as cache.DeletedFinalStateUnknown) for every
//     object of every watched type in that namespace.
//   - Get for an object in a namespace that does not match the selector returns
//     a NotFound API error, and List over all namespaces only returns objects
//     from matching namespaces. Absence of a namespace from the selection is
//     data, not an error — this mirrors how label-selected caches treat
//     filtered-out objects and lets level-based reconcilers converge after a
//     namespace is deselected.
//   - Namespace objects themselves can be read (Get/List) and watched; they are
//     served from the internal namespace informer and only namespaces matching
//     the selector are visible.
//   - Mixed scopes are supported: all other cluster-scoped types (cluster-scoped
//     CRDs, ClusterRoles, ...) are served by a regular cluster-wide side cache,
//     so one manager can watch namespaced and cluster-scoped resources together.
//     Cluster-scoped resources inherently require cluster-wide read RBAC (e.g.
//     via an auth-operator BindDefinition clusterRoleBindings entry); without
//     it their informers retry silently and never sync — set
//     Options.RejectClusterScoped to fail fast with
//     *ErrClusterScopedUnsupported instead when running with purely
//     namespaced grants.
//
// Usage — only the manager initialization changes:
//
//	mgr, err := ctrl.NewManager(cfg, ctrl.Options{
//		NewCache: dynamiccache.NewCacheFunc(dynamiccache.Options{
//			NamespaceSelector: labels.SelectorFromSet(labels.Set{"t-caas.telekom.com/tenant": "my-tenant"}),
//		}),
//	})
//
// Note: when pairing with auth-operator, its validating webhook only admits
// tracked ownership label keys in BindDefinition namespace selectors
// (t-caas.telekom.com/{owner,tenant,thirdparty} or kubernetes.io/metadata.name)
// — pick the NamespaceSelector key from those and differentiate via the value.
//
// A namespace whose RBAC grant does not yet exist keeps retrying its initial
// LIST with backoff (standard informer behavior); WaitForCacheSync and event
// handler sync checks only report synced once every currently selected
// namespace has synced.
//
// The package README (pkg/controllerruntime/dynamiccache/README.md) documents the
// full semantics table, RBAC pairing with auth-operator, errors and
// troubleshooting; examples/dynamiccache contains a runnable sample.
package dynamiccache

import (
	"errors"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	toolscache "k8s.io/client-go/tools/cache"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client/apiutil"
)

const (
	// defaultNamespaceResync is the resync period of the internal namespace
	// informer. Resyncs re-deliver Update events for all selected namespaces,
	// which doubles as the retry mechanism for namespaces whose per-namespace
	// cache could not be constructed.
	defaultNamespaceResync = 30 * time.Second
)

var namespaceGVK = corev1.SchemeGroupVersion.WithKind("Namespace")

// Options configures the dynamic cache.
type Options struct {
	// NamespaceSelector selects the namespaces whose objects are cached.
	// Required and must not be empty: an empty selector would select every
	// namespace, for which the regular cluster-wide cache is the right tool.
	// labels.Nothing is also rejected because it serializes to the API
	// server's empty (match-all) label selector.
	NamespaceSelector labels.Selector

	// NamespaceResync overrides the resync period of the internal namespace
	// informer. Defaults to 30s.
	NamespaceResync time.Duration

	// RejectClusterScoped disables the cluster-scoped side cache: reads and
	// informer requests for cluster-scoped types (other than Namespace) then
	// fail fast with *ErrClusterScopedUnsupported instead of retrying watches
	// that per-namespace-only RBAC can never satisfy. Set this when the
	// operator's ServiceAccount has no cluster-wide read grants.
	RejectClusterScoped bool

	// newCache constructs the per-namespace caches. Test hook, defaults to
	// cache.New.
	newCache cache.NewCacheFunc
}

// New returns a dynamic label-based multi-namespace cache. cacheOpts is used as
// the template for every per-namespace cache (ByObject selectors, transforms,
// SyncPeriod etc. apply per namespace); its DefaultNamespaces field is owned by
// the dynamic cache and must not be set. ByObject entries must leave Namespaces
// nil so that they inherit the dynamically selected namespace.
func New(config *rest.Config, cacheOpts cache.Options, opts Options) (cache.Cache, error) {
	if config == nil {
		return nil, errors.New("dynamiccache: rest.Config must not be nil")
	}
	if opts.NamespaceSelector == nil || opts.NamespaceSelector.Empty() || opts.NamespaceSelector.String() == "" {
		return nil, errors.New("dynamiccache: Options.NamespaceSelector must be set and non-empty")
	}
	if len(cacheOpts.DefaultNamespaces) > 0 {
		return nil, errors.New("dynamiccache: cache.Options.DefaultNamespaces is owned by dynamiccache and must not be set")
	}
	for obj, byObject := range cacheOpts.ByObject {
		if byObject.Namespaces != nil {
			return nil, fmt.Errorf("dynamiccache: cache.Options.ByObject[%T].Namespaces is owned by dynamiccache and must not be set", obj)
		}
	}

	httpClient := cacheOpts.HTTPClient
	if httpClient == nil {
		var err error
		httpClient, err = rest.HTTPClientFor(config)
		if err != nil {
			return nil, fmt.Errorf("dynamiccache: constructing HTTP client: %w", err)
		}
		cacheOpts.HTTPClient = httpClient
	}
	if cacheOpts.Scheme == nil {
		cacheOpts.Scheme = clientgoscheme.Scheme
	}
	if cacheOpts.Mapper == nil {
		mapper, err := apiutil.NewDynamicRESTMapper(config, httpClient)
		if err != nil {
			return nil, fmt.Errorf("dynamiccache: constructing REST mapper: %w", err)
		}
		cacheOpts.Mapper = mapper
	}

	cs, err := kubernetes.NewForConfigAndClient(config, httpClient)
	if err != nil {
		return nil, fmt.Errorf("dynamiccache: constructing clientset: %w", err)
	}

	resync := opts.NamespaceResync
	if resync <= 0 {
		resync = defaultNamespaceResync
	}
	newCache := opts.newCache
	if newCache == nil {
		newCache = cache.New
	}

	lw := toolscache.NewFilteredListWatchFromClient(
		cs.CoreV1().RESTClient(), "namespaces", metav1.NamespaceAll,
		func(o *metav1.ListOptions) { o.LabelSelector = opts.NamespaceSelector.String() },
	)

	// Mixed-scope support: cluster-scoped types (cluster-scoped CRDs,
	// ClusterRoles, ...) are served by a regular cluster-wide side cache.
	// It creates informers lazily, so it costs nothing until a
	// cluster-scoped type is actually read or watched.
	var clusterCache cache.Cache
	if !opts.RejectClusterScoped {
		var err error
		clusterCache, err = newCache(config, cacheOpts)
		if err != nil {
			return nil, fmt.Errorf("dynamiccache: constructing cluster-scoped cache: %w", err)
		}
	}

	dc := &dynamicCache{
		config:       config,
		cacheOpts:    cacheOpts,
		selector:     opts.NamespaceSelector,
		scheme:       cacheOpts.Scheme,
		mapper:       cacheOpts.Mapper,
		newCache:     newCache,
		clusterCache: clusterCache,
		doneCh:       make(chan struct{}),
		nsEntries:    map[string]*nsEntry{},
		informers:    map[informerKey]*fanoutInformer{},
	}
	dc.nsInformer = toolscache.NewSharedIndexInformer(lw, &corev1.Namespace{}, resync, toolscache.Indexers{})
	return dc, nil
}

// NewCacheFunc adapts New to manager.Options.NewCache / cluster.Options.NewCache.
// The manager fills in Scheme, Mapper and HTTPClient on the cache.Options it
// passes through.
func NewCacheFunc(opts Options) cache.NewCacheFunc {
	return func(config *rest.Config, cacheOpts cache.Options) (cache.Cache, error) {
		return New(config, cacheOpts, opts)
	}
}

// groupResource resolves the GroupResource for NotFound errors, falling back to
// a lower-cased pluralized kind when the mapper has no mapping.
func (dc *dynamicCache) groupResource(gvk schema.GroupVersionKind) schema.GroupResource {
	mapping, err := dc.mapper.RESTMapping(gvk.GroupKind(), gvk.Version)
	if err != nil {
		return schema.GroupResource{Group: gvk.Group, Resource: gvk.Kind}
	}
	return mapping.Resource.GroupResource()
}

// isNamespacedGVK reports whether gvk refers to a namespaced resource.
func (dc *dynamicCache) isNamespacedGVK(gvk schema.GroupVersionKind) (bool, error) {
	return apiutil.IsGVKNamespaced(gvk, dc.mapper)
}
