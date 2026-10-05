# dynamiccache

A [controller-runtime](https://github.com/kubernetes-sigs/controller-runtime)
`cache.Cache` implementation that watches objects **only in namespaces selected
by a label selector**, starting and stopping per-namespace informers
dynamically as namespaces gain or lose the selecting labels. Cluster-scoped
resources are fully supported alongside namespaced ones (mixed-scope
operators).

```
go get github.com/telekom/t-caas-go-library
```

```go
import "github.com/telekom/t-caas-go-library/pkg/controllerruntime/dynamiccache"
```

A runnable sample lives in [`examples/dynamiccache`](../../../examples/dynamiccache/).

## Why

The Kubernetes API server cannot list or watch namespaced resources across
"namespaces matching a label selector" — a LIST/WATCH is either cluster-wide or
scoped to a single namespace. Operators that only own a labeled subset of
namespaces (multi-tenant platforms) are therefore forced to either watch
cluster-wide (requires cluster-wide RBAC and caches everything) or restart with
a regenerated static namespace list.

`dynamiccache` instead runs:

- **one** cluster-scoped informer on `Namespace` objects, filtered server-side
  by the label selector — the only mandatory cluster-scoped permission, and
- **one single-namespace cache per matching namespace**, started/stopped at
  runtime as namespaces are (un)labeled, and
- an optional **cluster-scoped side cache** serving cluster-scoped types
  (cluster-scoped CRDs, `ClusterRole`s, ...) for mixed-scope operators.

Per-namespace RBAC is expected to be provisioned externally — on T-CaaS
clusters by [auth-operator](https://github.com/telekom/auth-operator)
`BindDefinition`s using the same namespace selector (see
[RBAC with auth-operator](#rbac-with-auth-operator)).

### Upstream alternatives considered

**Use upstream instead for:** static namespace caches with
[`sigs.k8s.io/controller-runtime/pkg/cache`](https://pkg.go.dev/sigs.k8s.io/controller-runtime/pkg/cache),
ordinary shared informers with
[`k8s.io/client-go/tools/cache`](https://pkg.go.dev/k8s.io/client-go/tools/cache),
and general-purpose TTL/LRU storage with
[`github.com/fluxcd/pkg/cache`](https://pkg.go.dev/github.com/fluxcd/pkg/cache).
Adoption changes being pending is not a reason to recreate these engines.

Use controller-runtime's stock `cache.New` when the namespace set is static:
`cache.Options.DefaultNamespaces` and `ByObject.Namespaces` already cover that.
Its object label selectors do not select namespaces by their labels, and its
namespace maps are fixed at construction. Flux's `github.com/fluxcd/pkg/cache` provides
TTL/LRU object storage, not a controller-runtime namespace cache; Flux's
`github.com/fluxcd/pkg/runtime` does not offer this namespace-selection lifecycle either.

This package builds on controller-runtime's caches and client-go's shared
informers rather than replacing them. The delta is label-driven namespace
membership, handler/index replay, and synthetic deletes on deselection.

## Quickstart

Only the manager initialization changes; controllers, builders, watches, field
indexes and reconcilers work unchanged:

```go
import (
    "k8s.io/apimachinery/pkg/labels"
    ctrl "sigs.k8s.io/controller-runtime"

    "github.com/telekom/t-caas-go-library/pkg/controllerruntime/dynamiccache"
)

mgr, err := ctrl.NewManager(cfg, ctrl.Options{
    NewCache: dynamiccache.NewCacheFunc(dynamiccache.Options{
        NamespaceSelector: labels.SelectorFromSet(labels.Set{"t-caas.telekom.com/tenant": "my-tenant"}),
    }),
})
// ... SetupWithManager for your controllers as usual ...
```

Everything obtained from the manager behaves normally: `mgr.GetClient()` reads
from the dynamic cache, `builder.ControllerManagedBy(mgr).For(...)` /
`Owns(...)` / `Watches(...)` wire their event sources through it, and
`mgr.GetFieldIndexer().IndexField(...)` indexes apply to every current and
future selected namespace.

The manager fills in `Scheme`, `Mapper` and `HTTPClient`; all other
`cache.Options` (per-object selectors, transforms, `SyncPeriod`, ...) act as the
template for every per-namespace cache and the cluster-scoped side cache.
`DefaultNamespaces` is owned by `dynamiccache` and must not be set.
`ByObject.Namespaces` must be nil (even an empty map overrides namespace
inheritance); other per-object selectors and transforms remain supported.
Namespaced structured, unstructured and metadata informer representations are
kept independent, including synthetic-delete snapshots and informer removal.
Namespace reads and events also preserve the requested representation.

## Options

| Option | Type | Default | Meaning |
| --- | --- | --- | --- |
| `NamespaceSelector` | `labels.Selector` | — (required, non-empty) | Selects the namespaces whose objects are cached and watched. Empty selectors, including `labels.Nothing()` (whose wire form is match-all), are rejected: for "all namespaces" use the stock cache. |
| `NamespaceResync` | `time.Duration` | `30s` | Resync period of the internal namespace informer. Doubles as the retry cadence for per-namespace wiring that failed transiently (cache construction, informer creation, indexers, handler registration). |
| `RejectClusterScoped` | `bool` | `false` | Disables the cluster-scoped side cache. Reads/watches of cluster-scoped types then fail fast with `*ErrClusterScopedUnsupported` instead of retrying watches that purely namespaced RBAC can never satisfy. |

## Semantics

| Situation | Behavior |
| --- | --- |
| Namespace matches the selector | Its objects are cached and served; informers watch only that namespace. |
| Namespace starts matching later ("late namespace") | Its informers are started and every registered event handler receives replayed ADD events for the namespace's existing objects (standard client-go replay). Late namespaces are observationally identical to initial ones. |
| Namespace stops matching (labels changed / namespace deleted) | Its informers are stopped and every registered handler receives **synthetic DELETE** events (`cache.DeletedFinalStateUnknown` carrying the last cached object state) for every object of every watched type in that namespace, so level-based reconcilers observe the disappearance and converge. |
| `Get` in a non-matching namespace | `NotFound` API error. Absence from the selection is data, not an error. |
| `Get`/`List` in a **matching** namespace whose cache or requested informer is not ready (just selected, RBAC pending, failed init awaiting retry) | Transient `*ErrNamespaceNotReady` after a bounded sync probe (at most 250ms) — never `NotFound`, so a degraded tenant is never mistaken for a deleted one. Cross-namespace `List` also errors instead of silently omitting the namespace. Caller cancellation and `ReaderFailOnMissingInformer` retain their upstream error behavior. |
| `List` across all namespaces | Only returns objects from matching namespaces, deterministic namespace order; `Limit` is honored progressively, `Continue` pagination is not supported. |
| Reading/watching `Namespace` objects | Served from the internal namespace informer; only matching namespaces are visible. Field indexes on Namespaces are not supported. |
| Any other cluster-scoped type | Served by the cluster-scoped side cache (see [Mixed scopes](#mixed-scopes-cluster-scoped--namespaced)); with `RejectClusterScoped` set: `*ErrClusterScopedUnsupported`. |
| RBAC grant not there yet (e.g. 403 on initial LIST) | The per-namespace informer keeps retrying with backoff (standard informer behavior). `WaitForCacheSync` and handler sync checks report synced only once **every currently selected** namespace (and the side cache) has synced. |
| Reads before `Start` + initial namespace list | `*ErrCacheNotStarted` (relevant for webhooks, which the manager starts before caches sync — use `mgr.GetAPIReader()` there, or tolerate the transient error). |

## Mixed scopes (cluster-scoped + namespaced)

Watching cluster-scoped CRDs together with namespaced resources requires no
configuration:

```go
// namespaced, label-selected namespaces only:
builder.ControllerManagedBy(mgr).For(&appsv1.Deployment{})
// cluster-scoped, cluster-wide (side cache):
builder.ControllerManagedBy(mgr).For(&tenantv1.ClusterTenant{})
```

Rules of thumb:

- Cluster-scoped resources have no namespace, so they inherently need
  **cluster-wide read RBAC** for exactly those types (see below). This does not
  weaken the per-namespace model for namespaced types.
- Without that grant, the side cache's informers retry silently and
  `WaitForCacheSync` never completes. If your operator is meant to run with
  **purely namespaced grants**, set `RejectClusterScoped: true` to turn
  accidental cluster-scoped watches into an immediate, loud error.
- `Namespace` itself is never served by the side cache — it always comes from
  the internal selector-filtered informer, so unselected namespaces are never
  observable.

## RBAC with auth-operator

Three grants are involved; with auth-operator all of them are declarative:

1. **Cluster-scoped, mandatory**: `get`/`list`/`watch` on `namespaces` — for
   the single namespace informer.
2. **Per selected namespace**: `get`/`list`/`watch` on the watched namespaced
   types. This is the dynamic part auth-operator automates: a `BindDefinition`
   (`authorization.t-caas.telekom.com/v1alpha1`, cluster-scoped) with a
   `spec.roleBindings[].namespaceSelector` matching the **same labels** as the
   cache's `NamespaceSelector` creates/deletes the `RoleBinding`s as
   namespaces are (un)labeled.

   > **Label key constraint:** auth-operator's validating webhook only admits
   > **tracked ownership label keys** in BindDefinition namespace selectors:
   > `t-caas.telekom.com/owner`, `t-caas.telekom.com/tenant`,
   > `t-caas.telekom.com/thirdparty`, or `kubernetes.io/metadata.name`.
   > Pick your `NamespaceSelector` from these keys (differentiate via the
   > label *value*), otherwise the BindDefinition is rejected with a 422.
3. **Cluster-scoped, mixed-scope operators only**: reads on exactly the
   cluster-scoped types you watch, via `clusterRoleBindings.clusterRoleRefs`.

```yaml
apiVersion: authorization.t-caas.telekom.com/v1alpha1
kind: BindDefinition
metadata:
  name: my-operator
spec:
  targetName: my-operator
  subjects:
    - kind: ServiceAccount
      name: my-operator
      namespace: my-operator-system
  clusterRoleBindings:
    clusterRoleRefs:
      - my-operator-namespace-reader    # (1) namespaces get/list/watch
      - my-operator-clustertenant-read  # (3) cluster-scoped CRD reads, if needed
  roleBindings:
    - clusterRoleRefs:
        - my-operator-tenant-reader   # (2) ClusterRole with the per-namespace rules
      # a LIST of metav1.LabelSelector — entries are ORed
      namespaceSelector:
        - matchLabels:
            t-caas.telekom.com/tenant: my-tenant
```

Because the `RoleBinding` is created asynchronously after a namespace is
labeled, the freshly selected namespace's informer may initially receive 403s —
`dynamiccache` retries until the grant lands (see semantics table). The
[`e2e/dynamiccache`](../../../e2e/dynamiccache/) suite in this repository verifies exactly this contract against a kind
cluster with auth-operator installed.

## Errors

| Error | Meaning | Reaction |
| --- | --- | --- |
| `*ErrNamespaceNotReady` | Namespace is selected but its cache is not running yet (grant race, fresh selection, failed init awaiting resync retry). | Transient — return it from `Reconcile` and let the workqueue retry. |
| `*ErrCacheNotStarted` | Read before `Start`/initial namespace list. | Transient at startup; webhooks should use `mgr.GetAPIReader()`. |
| `*ErrClusterScopedUnsupported` | Cluster-scoped type while `RejectClusterScoped` is set. | Configuration decision — grant cluster RBAC and clear the option, or bypass the cache via `client.Options.Cache.DisableFor`. |
| `NotFound` (API error) | Object absent — including "its namespace does not match the selector". | Normal level-based handling ("object deleted"). |

## Observability & troubleshooting

- **`WaitForCacheSync`/manager start hangs**: some selected namespace (or the
  cluster-scoped side cache) cannot complete its initial LIST — almost always
  missing RBAC. Look for `dynamiccache` error logs (repeated every
  `NamespaceResync`) naming the namespace/type.
- **A controller's `Watch` never becomes ready and `GetInformer` errors repeat
  every 10s**: the watched type's REST mapping failed (CRD not installed);
  controller-runtime retries forever by design and the log line names the GVK.
- **"synthetic deletes are LOST" in logs**: a deselected namespace's object
  snapshot failed even after retry; reconcilers will not observe the removal
  of those objects until the operator restarts. Logged loudly with GVK and
  namespace.
- Custom **stateful** `ResourceEventHandler`s (expectation caches etc.) must
  tolerate a relaxed ordering during deselection: a synthetic DELETE may
  interleave with the dying informer's last buffered events.
  controller-runtime's standard workqueue handlers are unaffected.

## Compatibility policy

- The library module tracks the Kubernetes minor of its consumers (see the
  [root README](../../../README.md#compatibility)): `go.mod` requires
  controller-runtime v0.25 / k8s.io v0.37.
- **Source floor: controller-runtime v0.24 / k8s.io v0.36.** The
  `cache.Informer` interface of controller-runtime v0.24 requires client-go
  v0.36's `tools/cache` API (`DoneChecker`), so older dependency lines cannot
  be supported by a single implementation.
- CI ([`dynamiccache.yml`](../../../.github/workflows/dynamiccache.yml)) runs
  this package's suite against pinned floor controller-runtime/k8s.io pairs
  plus a non-blocking canary against `controller-runtime@main` to surface
  upstream breakage early.

## Testing

- `make test` (envtest; pinned binaries via `setup-envtest`, race detector,
  randomized spec order) — behavioral suite incl. late-namespace replay,
  synthetic deletes, mixed scopes, field indexes, failure recovery and full
  manager/builder integration.
- [`e2e/dynamiccache`](../../../e2e/dynamiccache/) runs the contract against a
  kind cluster with a real auth-operator install, including the RBAC grant
  race with a restricted ServiceAccount. Path-filtered in CI
  ([`e2e-dynamiccache.yml`](../../../.github/workflows/e2e-dynamiccache.yml));
  run locally with `make test-e2e-dynamiccache` (requires docker and kind,
  creates and deletes its own throwaway cluster).

## Versioning

`dynamiccache` is part of the `github.com/telekom/t-caas-go-library` module
and released with it (`vX.Y.Z` tags, see the
[root README](../../../README.md#versioning)).

## Limitations

- `List` rejects non-empty `Continue` pagination tokens on every path.
  Like the stock controller-runtime cache, merged and Namespace lists set
  `Continue` to `continue-not-supported`; a limited result must not be treated
  as a complete API-server page.
- `cache.InformerGetOption`s (e.g. `BlockUntilSynced`) are ignored for
  namespaced types — informers are always materialized non-blocking and sync
  is reported via `WaitForCacheSync`/`HasSynced`; the cluster-scoped side
  cache honors them.
- Field indexes on `Namespace` objects are not supported.
- Namespace-set churn is processed by one goroutine; extremely hot label
  flapping across many namespaces serializes (by design — it keeps
  add/remove event ordering strict).
