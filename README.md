# T-CaaS Go Library

Shared Go helpers for Telekom T-CaaS Kubernetes operators.

`github.com/telekom/t-caas-go-library` collects small, generic, well-tested
building blocks that were previously duplicated across operators such as
[telekom/auth-operator](https://github.com/telekom/auth-operator) and
[telekom/k8s-breakglass](https://github.com/telekom/k8s-breakglass).
It builds on the Go standard library and established upstream libraries —
never on a consumer repository.

> **Status:** early bootstrap (`v0.x`). Packages land incrementally; APIs may
> still change between minor versions (see [Versioning](#versioning)).

## Before writing helpers: use upstream

**Check [the upstream decision guide](docs/upstream-libraries.md) and this
library before adding helpers, here or in consumer repositories.** Use
well-known upstream packages directly instead of custom implementations, even
when consumer adoption PRs have not merged. Convenience wrappers require the
same demonstrated glue in multiple consumer repositories, with cited call sites.
The guide records exact imports, semantic pitfalls and migration hints.

## Packages

| Package | Purpose | Status |
|---------|---------|--------|
| `pkg/remoteclient` | Generation-fenced multi-cluster clients and Secret invalidation | available |
| `pkg/netutil` | Overflow-checked arithmetic, bounded subdivision and shared address conventions, built on netip/netipx | available |
| [`pkg/ssa`](pkg/ssa) | Cache-only no-op gate for typed Server-Side Apply; delegates writes to controller-runtime ([example](examples/ssa)) | available |
| [`pkg/patch`](pkg/patch) | Thin optimistic-lock retry and finalizer adapters shared by multiple operators ([example](examples/patch)) | available |
| [`pkg/certrotation`](pkg/certrotation) | Shared gating/readiness/SAN glue on [cert-controller](https://github.com/open-policy-agent/cert-controller) ([example](examples/certrotation)) | available |
| `pkg/redact` | URL diagnostic redaction and UTF-8-safe truncation | available |
| [`pkg/ratelimit`](pkg/ratelimit/README.md) | Bounded keyed limiter storage on x/time/rate | available |
| [`pkg/discovery/tracker`](pkg/discovery/tracker) | Periodic discovery snapshots and CRD-watch change callbacks | available |
| [`pkg/namespaceselector`](pkg/namespaceselector) | Live namespace label selectors with request-local memoization | available |
| `pkg/redfish/redfishtest` (separate module) | Stateful synthetic Redfish server with synchronized fault injection | available |
| [`pkg/controllerruntime/dynamiccache`](pkg/controllerruntime/dynamiccache/README.md) | controller-runtime `cache.Cache` that watches only namespaces matching a label selector, with dynamic per-namespace informers and synthetic deletes on deselection | available |

All entries above are available on main (snapshot 2026-10-05).
`conditions`, `tracing`, `metrics`, `envtestutil` and lifecycle/config
packages were closed as redundant; crhelpers and kubetest were never opened.
Use the [recommended upstream packages](docs/upstream-libraries.md), not those
branches. Only the justified deltas above are available.

Available packages have godoc, unit tests, `Example` tests and a runnable sample
under [`examples/`](examples/).

### Remote clients

Use upstream `k8s.io/client-go/tools/clientcmd/api.Config` and `clientcmd.Write`
to construct kubeconfig YAML, and `clientcmd.RESTConfigFromKubeConfig` to parse it.
There is no shared kubeconfig builder: the only identified consumer (an internal
cluster provisioning operator) can use these APIs directly, retaining
its input validation, identity defaults and context naming locally.

`remoteclient.New` accepts QPS/burst, timeout, an injectable factory, and transport
middleware. Zero timeout means **no timeout**; zero QPS/burst use client-go defaults.
Refreshes are ordered by start generation, so an older refresh cannot overwrite
new credentials or resurrect a removed entry. Failed refreshes preserve the old
client. Removal/replacement closes owned idle connections, not active requests.
Secret dependencies are registered before reads and retained after failed refreshes.
Wire Secret create/update/delete events to `InvalidateSecret` and cluster-reference
changes to `Remove`; invalidation hooks can enqueue dependent reconcilers.

The registry is not an authorization or credential-freshness boundary: previously
returned clients are not revoked, and callers must perform their own live checks
for privileged operations. Prefer an uncached API reader for `RefreshSecret`.
Only embedded kubeconfigs are accepted: exec/auth-provider plugins and local
credential-file references are rejected. Parser error details are intentionally
redacted to avoid leaking credential bytes.

See [`examples/remoteclient`](examples/remoteclient). CLI RESTClientGetter helpers
and circuit breaking are intentionally outside this package.

#### Upstream alternatives considered

The registry composes client-go's `clientcmd`, `rest.TransportFor`, and
controller-runtime's `client.New`; it does not implement Kubernetes configuration
parsing, authentication, discovery or API calls.

- [Flux runtime/client](https://github.com/fluxcd/pkg/tree/main/runtime/client)
  supplies local configuration/flow-control defaults and a REST mapper, not a
  remote-client registry or Secret dependency invalidation.
- [controller-runtime cluster](https://pkg.go.dev/sigs.k8s.io/controller-runtime/pkg/cluster)
  manages a cluster's cache and lifecycle. Use it when remote informers are needed.
- [multicluster-runtime](https://github.com/kubernetes-sigs/multicluster-runtime),
  particularly its `providers/kubeconfig`, is preferred for dynamic fleets of
  remote controllers. Its provider starts clusters/caches and engages a
  multicluster manager; it does not supply this registry's latest-started refresh
  fencing, failed-refresh preservation, arbitrary cluster-to-Secret references
  (including many clusters per Secret), or explicit synchronous invalidation.

The retained delta is shared lifecycle glue for **uncached** remote clients:
network-operator `controllers/sync/remote_client.go:48,87,99,112` and breakglass
`pkg/cluster/cache.go:74,791,955,963,1103` both maintain remote credential/client
registries and evict entries. Replace those portions with `Registry`, mapping
Secret-backed references through `Resolver` and watch events through
`InvalidateSecret` (breakglass `pkg/cluster/watchers.go:17`). Keep namespace
enumeration in network-operator and OIDC, clientsets, TTL, authorization and live
privileged-input checks in breakglass. This is not a drop-in replacement for
either consumer's full client provider.

### Kubernetes API helpers

Run the offline samples (no cluster or credentials required):

```bash
go run ./examples/discovery/tracker
go run ./examples/namespaceselector
make test-examples
```

The namespace sample uses a fake client; production namespace reads should use an
uncached `client.Reader` (for example `manager.GetAPIReader()`). Create a fresh
`namespaceselector.Request` for every admission/authorization request. A nil
selector matches nothing, a nonnil empty selector matches every existing
namespace, cluster-scoped requests never match, and missing namespaces return
the wrapped API error. Both labels and read errors are memoized within a request.

For live discovery use `discovery.NewDiscoveryClientForConfig(config)` from
`k8s.io/client-go/discovery`, then `tracker.New(source, watchClient, options)` with an uncached
`client.NewWithWatch` and the apiextensions/v1 scheme registered.
Add the tracker to a manager using `manager.Add(instance)` or call its blocking
`Start(ctx)` directly. It runs on every replica, periodically refreshes and
reconnects CRD watches, and coalesces watch bursts with a trailing-edge refresh.
Partial discovery retains stale data **only** for failed group/versions; healthy
groups continue updating, and healthy removals are reflected. Snapshot reads
and callback arguments are deep copies. Hooks allow caller-owned metrics, and
an optional Transform hook can add application-specific resources/verbs.
Hooks and callbacks run synchronously and must not call `Refresh` or block.
Transform hooks must be idempotent because failed versions retain previously
transformed resources.

### Upstream alternatives considered

The discovery tracker adds canonical deep-copy snapshots, per-failed-version
retention, and change callbacks to client-go discovery; it does **not** implement
the Kubernetes discovery protocol. REST mapping consumers should instead use
controller-runtime's dynamic REST mapper or client-go's cached discovery.
Neither mapper exposes this complete resource/verb snapshot and change callback
contract. Flux runtime has no equivalent discovery tracker.

Namespace matching delegates parsing and matching to
`metav1.LabelSelectorAsSelector` and Kubernetes `labels.Selector`. The only
additional glue is a bounded, live namespace read with request-local memoization
of labels and errors. This replaces the repeated authorization reads in
auth-operator `internal/webhook/authorization/webhook_authorizer.go:831` and
k8s-breakglass `pkg/webhook/controller.go:1682` together with selector evaluation
in `pkg/policy/deny.go:647` / `pkg/utils/namespace_matcher.go:127`; callers must
translate custom selector terms to `metav1.LabelSelector` and retain their
policy-specific nil/empty, name-pattern and remote-cluster semantics. Create a
separate request for each cluster; this is not a drop-in policy replacement.

The initial extraction's other packages were dropped:

| Concern | Recommended upstream and migration |
|---------|------------------------------------|
| CRD waits | Kubernetes `wait.PollUntilContextCancel` plus CRD Established conditions; envtest `WaitForCRDs` for discovery in tests. auth-operator `pkg/discovery/crd.go:34` should retain explicit plurals and pass the caller's context, not infer Kind plurals. |
| RBAC subjects | Standard-library `slices.Contains`, `slices.SortFunc`, `slices.Compact` and comparable `rbacv1.Subject` map keys. Replace auth-operator `pkg/helpers/subjects.go:16` locally; API defaulting must remain explicit. |
| Names | `k8s.io/apimachinery/pkg/util/validation` for DNS/label validation and `pkg/api/names` for generated names. Keep whereabouts `pkg/iphelpers/iphelpers.go:21` domain-specific; a new hash sanitizer would change existing resource identities. |
| Secret reads | `client.Reader.Get` and `Secret.Data` for arbitrary keys; Flux `github.com/fluxcd/pkg/runtime/secrets` for TLS, proxy and authentication conversion. Keep caller-specific missing-key handling in each consumer. |
| Secret test conversion | Set `Secret.Data` in fake-client fixtures; use controller-runtime envtest when API-server `StringData` conversion matters. No repeated multi-repository interceptor was demonstrated. |

No new non-Kubernetes runtime dependency is required.

### Networking upstream alternatives

`pkg/netutil` retains only signed arbitrary-precision arithmetic with same-family
overflow rejection (`Add`), subdivision with a mandatory allocation budget
(`Subdivide`), and shared first-usable/broadcast conventions (`FirstUsable`,
`Broadcast`). Prefix endpoints use [`go4.org/netipx`](https://pkg.go.dev/go4.org/netipx).
`FirstUsable` uses the network address for /31 and /127, rejects /32 and /128,
and otherwise returns network+1. It is not universal allocation policy.

Use netipx directly for IP sets or minimal range-to-prefix covers; those are not
fixed-length, budgeted subdivision. Use `k8s.io/utils/net` for legacy `net.IP`
conversion/arithmetic where its contracts fit. Its `AddIPOffset` has no signed
big-integer, same-family overflow-error contract. Use `netip.Addr` methods for
comparison, unmapping and next/previous addresses; `Prefix.Masked().Addr()` for
network addresses; `Prefix.Overlaps` for overlap; netipx `IPRange.Contains` and
`RangeOfPrefix` for ranges; `net.ParseMAC` then `HardwareAddr.String()` for MACs.
No wrappers for those upstream APIs are provided.

Migration: whereabouts `pkg/iphelpers/iphelpers.go` can use direct `netip`/netipx
for comparison, ranges and overlap; its bounded subdivision and checked addition
can use `Subdivide` and `Add` after converting legacy IPs explicitly. Its
`FirstUsableIP` currently accepts single-host prefixes: keep that policy locally
instead of blindly replacing it with `FirstUsable`. network-operator
`pkg/reconciler/intent/ipmath/ipmath.go` can share the first-usable and IPv4
endpoint conventions, retaining CIDR formatting and gateway assignment locally
(its broadcast helper rejects /32; preserve that guard). An internal bare-metal
provisioning operator's MAC
normalization needs only Go 1.26 `net.ParseMAC`, not a library wrapper.

### Recommended upstream libraries

- Snapshot/deferred object and status patching: [`github.com/fluxcd/pkg/runtime/patch`](https://pkg.go.dev/github.com/fluxcd/pkg/runtime/patch)
  (`NewHelper`, `NewSerialPatcher`, `WithOwnedConditions`). Use these directly
  instead of a local diff engine; condition ownership must be explicit.
- Native conflict retry and optimistic-lock patches: client-go `retry.RetryOnConflict`
  and controller-runtime `client.MergeFromWithOptions(base, client.MergeFromWithOptimisticLock{})`.
  `pkg/patch` only combines these operations where the same glue is repeated
  across auth-operator and k8s-breakglass; it implements neither algorithm.
- Server-Side Apply: generated apply configuration builders or
  controller-runtime `client.ApplyConfigurationFromUnstructured`. Do not convert
  a whole live object when only a subset of fields should be owned.

### Certificate rotation: provenance and adoption

`pkg/certrotation` keeps cert-controller's rotation logic, with generic
configuration and readiness wiring extracted from these consumers:

| Consumer / replaceable helpers | Existing behavior | Library mapping / differences |
|---|---|---|
| auth-operator: `internal/webhook/certrotator/certrotator.go:18-51`, `cmd/webhook.go:146` onward | Leader-only rotation, restart on refresh, webhook registration after certificate readiness; atomic readiness flag | `RequireLeaderElection: true`, `RestartOnSecretRefresh: true`, `CAName: "cert"`, `CAOrganization: "t-caas"`; `SetupWhenReady` + `ReadyChecker` replace goroutines/atomic flag. Non-leader readiness requires a matching, currently valid TLS pair rather than non-empty files. |
| k8s-breakglass: `pkg/cert/cert.go:70-181` (`setupRotator`, `Start`, `newCertRotator`, `Ensure`) | Dedicated manager gated by an external leadership signal; no restart; service-derived SANs; readiness plus a 30-second PEM wait | Keep the dedicated manager/leadership gate in the consumer; use `ServiceName`, `CAName: "<service>-ca"`, `CAOrganization: "breakglass"`, `FieldOwner`, and wait on `AddRotator`'s channel. With leader gating disabled, cert-controller already waits for mounted certificates and CA injection. |
| whereabouts: `internal/webhook/certrotator/certrotator.go:23-129`, `internal/webhook/setup.go:18-76` | Direct-client TLS Secret bootstrap tolerates AlreadyExists; rotation on all replicas, restart on refresh; runnable waits before registering three webhooks and sets atomic readiness | Keep Secret bootstrap local or pre-create it in manifests; use `RequireLeaderElection: false`, `RestartOnSecretRefresh: true`, explicit CA name/org and webhook names, and ordered callbacks in `SetupWhenReady`. Its completion channel replaces the atomic readiness flag. Cancellation while waiting is a clean shutdown, and callback failures stop the manager. |
| network-operator: `cmd/operator/main.go:193-265` | Leader-only rotation, optional restart, service/FQDN SANs; readiness checks certificate file existence; controllers/webhooks registered before manager start | Map `RequireLeaderElection: true` and `RestartOnSecretRefresh: !disableRestartOnCertRefresh`, DNS/SANs and explicit CA values. Library readiness validates the mounted TLS pair; deferred setup is optional, not a replacement for pre-start controller/informer registration. |

### Upstream alternatives considered

Certificate generation, renewal, Secret updates and CA injection are delegated
to `open-policy-agent/cert-controller`, not reimplemented. Its `IsReady` channel
already covers leader readiness. Its `ValidCert` helper requires a CA and DNS
name; non-leader file readiness only checks the mounted pair using Go's TLS
parser. Controller-runtime provides certificate watching and manager runnables,
but not the repeated certificate-gated registration/readiness wiring above.
Flux runtime does not supply a certificate rotation adapter.

Only multi-consumer glue is retained: rotator configuration across all four
consumers, service SAN defaults in breakglass/network-operator, mounted-file
readiness in auth-operator/network-operator, and deferred setup/readiness in
auth-operator/whereabouts. Secret bootstrap was removed because only
whereabouts needs it; use deployment manifests or retain its direct
controller-runtime client logic locally. Controller names pass through to
upstream without a custom process-wide naming implementation.

The certificate Secret must exist before rotation starts.
Without leader-only rotation,
every replica may update the shared Secret (matching whereabouts); enable
leader election to avoid that competition.
`RequireLeaderElection` only gates the rotator runnable; also enable the
manager's `ctrl.Options.LeaderElection` with a shared `LeaderElectionID` and
grant the manager access to its coordination Lease. Without manager leader
election, every replica rotates even when `RequireLeaderElection` is true.

`RestartOnSecretRefresh` delegates to cert-controller's process exit behavior,
including after initial certificate generation. It defaults to false. All
webhook setup callbacks run on every replica, independent of leader election;
register controllers/informers before starting the manager, not inside these
callbacks. `ReadyChecker` on the setup completion channel does not replace
the webhook server's own started/health check.

### Keyed rate-limiter adapter

`pkg/ratelimit` adds bounded per-key storage around
[`golang.org/x/time/rate`](https://pkg.go.dev/golang.org/x/time/rate); upstream
performs all token-bucket accounting. The shared delta is O(1) LRU eviction and
amortized O(1) idle pruning, replacing matching bucket lookup/cleanup glue in
`auth-operator:internal/webhook/authorization/webhook_authorizer.go:370-439` and
`k8s-breakglass:pkg/ratelimit/ratelimit.go:104-335`. Both consumers retain their
own authenticated key selection and HTTP/framework response handling.

Run the sample with `go run ./examples/ratelimit`. An injectable Kubernetes
clock supports deterministic tests; no cleanup goroutine is started. Eviction
resets token budgets: attacker-controlled key churn requires a separate global
limiter and trusted key selection. IdleTTL should be at least a full bucket's
refill time if expiration must not accelerate token replenishment. For a single
global bucket, use `rate.NewLimiter` directly instead. Rates must be positive and
at most 1e9 tokens/second, the upstream limiter's nanosecond scheduling ceiling.

### Recommended resilience upstream libraries

Do not duplicate retry, circuit-breaker or coalescing-queue engines here:

| Concern | Recommended upstream | Consumer migration |
|---------|----------------------|--------------------|
| Generic retries | [`github.com/cenkalti/backoff/v5`](https://pkg.go.dev/github.com/cenkalti/backoff/v5) | BOOTy `pkg/retry/retry.go:11-78` and `pkg/provision/retry.go:13-149`: use `backoff.Retry`, `WithMaxTries`, `WithMaxElapsedTime`, `WithNotify`, and `Permanent`; capture ctx in the operation and retain provisioning-specific classification locally. |
| Kubernetes polling/backoff | [`k8s.io/apimachinery/pkg/util/wait`](https://pkg.go.dev/k8s.io/apimachinery/pkg/util/wait) | Operator-local polling: use `ExponentialBackoffWithContext` or `PollUntilContextTimeout` directly; keep finite validated duration/factor values rather than maintaining a generic overflow-safe scheduler. |
| Circuit breaking | [`github.com/sony/gobreaker/v2`](https://pkg.go.dev/github.com/sony/gobreaker/v2) | k8s-breakglass `pkg/cluster/circuitbreaker.go:27-619` and `pkg/audit/circuit_breaker.go:54-495`: use `CircuitBreaker.Execute` or `TwoStepCircuitBreaker.Allow`; retain keyed lifecycle, cluster/audit metrics and HTTP classification locally. Audit already uses gobreaker. Configure thresholds/timeouts explicitly; migration is not a drop-in equivalence of defaults or neutral-error semantics. |
| Single token bucket | [`golang.org/x/time/rate`](https://pkg.go.dev/golang.org/x/time/rate) | Use `NewLimiter` with `Allow`/`Wait` directly. Only shared, bounded multi-key storage belongs in `pkg/ratelimit`. |
| Coalescing/retries | [`k8s.io/client-go/util/workqueue`](https://pkg.go.dev/k8s.io/client-go/util/workqueue) | network-operator `pkg/debounce/debounce.go:11-81`: use a typed delaying/rate-limiting queue with one stable key, `AddAfter`, `Get`/`Done`, `AddRateLimited` and `Forget`. Cancel the worker context and shut down the queue, then wait for the worker. `Done` preserves triggers received during execution; pending delayed additions coalesce by key. |

### Redaction

Run the redaction sample with `go run ./examples/redact`.
Redaction is best-effort, not a comprehensive secret scrubber: URL paths and
unknown secrets may remain visible. Wrapped errors retain their original cause
for `errors.Is/As`; do not log an unwrapped cause.

### Recommended upstream libraries for standard utilities

Prefer established APIs rather than adding parallel utility implementations:

| Need | Recommended API | Consumer migration |
|------|-----------------|--------------------|
| OCI/image references | [`github.com/distribution/reference`](https://pkg.go.dev/github.com/distribution/reference): `ParseNormalizedNamed`, `FamiliarString` | BOOTy `pkg/image/redact.go`: tags/digests are image-reference syntax, not URL authorities; keep authentication separate and strip an application-owned `oci://` prefix before parsing. Keep credential-bearing source-URI diagnostics local rather than adding a single-consumer wrapper. |
| Checksums | [`opencontainers/go-digest`](https://pkg.go.dev/github.com/opencontainers/go-digest): `Parse`, `Digest.Verifier` | BOOTy `pkg/image/verify/verify.go`: register SHA-256/SHA-512, validate the digest, check the streaming copy error before `Verified`; preserve incomplete-read and mismatch errors at the caller. |
| HTTP clients / polling | `net/http`: `DefaultTransport.(*Transport).Clone`, `Client`, `NewRequestWithContext`; `k8s.io/apimachinery/pkg/util/wait.PollUntilContextCancel` | BOOTy network/authentication clients and an internal cluster provisioning operator: configure timeouts/TLS/CA roots directly and close polling response bodies. Preserve unlimited body reads for downloads where required. |
| Build metadata | `runtime/debug.ReadBuildInfo` and caller-owned linker variables | BOOTy `pkg/buildinfo/buildinfo.go`, whereabouts `pkg/version/version.go`, network-operator `pkg/version/version.go`, auth-operator and k8s-breakglass `pkg/system/version.go`: read module/VCS settings directly and retain application-specific formatting/defaults. |
| Command execution | `os/exec.CommandContext`; [`k8s.io/utils/exec`](https://pkg.go.dev/k8s.io/utils/exec) and its testing fake where an interface is needed | BOOTy `pkg/executil/executil.go`: retain its application-owned process registry, bounded writers, and sanitized diagnostics; context cancellation targets the direct child, not a process tree. |
| slog fan-out | Go 1.26 `log/slog.NewMultiHandler`; [`samber/slog-multi`](https://pkg.go.dev/github.com/samber/slog-multi) for older Go or routing | BOOTy `pkg/logging/multi.go`: pass the existing handlers directly to the upstream fan-out. |

`pkg/redact` is the multi-consumer glue exception: BOOTy
`pkg/image/redact.go:8-157` and `pkg/network/http.go:73-178`, plus an internal
bare-metal provisioning operator, repeat URL stripping
and bounded diagnostic handling. It builds on `net/url` and standard string/UTF-8
APIs; `url.URL.Redacted` alone only masks a password, leaving the username,
query and fragment. Keep consumer-specific path/authorization/token scrubbing
in those consumers; this package does not replace their complete sanitizers.

### SSA upstream alternatives

Use [`github.com/fluxcd/pkg/ssa`](https://github.com/fluxcd/pkg/tree/main/ssa)
`ResourceManager.Apply` / `ApplyAll` for general manifest reconciliation,
server-side dry-run drift detection, metadata cleanup, staged apply, waiting
and garbage collection. Its dry-run evaluates admission even for unchanged
objects. `pkg/ssa` provides only the narrower cache-based ownership gate
repeated in auth-operator and k8s-breakglass, for typed controller paths that
must avoid those no-op admission requests.

For adoption, auth-operator keeps its RBAC comparators, canonicalization,
label-selection policy and field-manager defaults locally, replacing the
cached Get/compare/apply logic with `ssa.Applier`. k8s-breakglass can replace
its generic cached apply/status helpers with `Applier` / `StatusApplier`,
keeping generated Extract functions, status builders and empty-list handling.
Both can instead use Flux for manifest-based paths where server-evaluated
state is required. No shared RBAC convenience package is provided: the
source-specific descriptors were only demonstrated in one consumer.

## Compatibility

| t-caas-go-library | Go | k8s.io/* | sigs.k8s.io/controller-runtime | envtest K8s |
|-------------------|----|----------|--------------------------------|-------------|
| `v0.1.x` (main)   | 1.26 | v0.37 | v0.25 | 1.36 |

The library follows the Kubernetes minor of its consumers. A bump of the
`k8s.io/*` or controller-runtime minor version is released as a new library
minor version and recorded in this table.

## Usage

```bash
go get github.com/telekom/t-caas-go-library@latest
```

```go
import "github.com/telekom/t-caas-go-library/pkg/<package>"
```

Consumers should pin a released tag (`vX.Y.Z`) rather than a pseudo-version.

### Redfish module

The synthetic test server is isolated in the nested module
`github.com/telekom/t-caas-go-library/pkg/redfish`. Use
`github.com/stmcginnis/gofish` directly for production clients, power, boot,
virtual media and inventory; no duplicate wrappers are provided. Compatibility
tests use gofish v0.26.0. Root-module consumers do **not** acquire gofish.
The nested module is versioned with
`pkg/redfish/vX.Y.Z` tags independently of the root module.

```bash
go get github.com/telekom/t-caas-go-library/pkg/redfish@latest
cd examples/redfish && go run . # synthetic server; no BMC needed
```

See [Redfish usage and limitations](docs/redfish.md). The example is an independent
module with a local replacement for the Redfish module, so it does not add a
gofish dependency to the root. Makefile checks discover and loop over all modules;
coverage profiles and CI summaries are produced per module.

## Versioning

The module uses [Semantic Versioning](https://semver.org/):

- **`v0.x`** — while in `v0`, a **minor** release (`v0.N.0`) may contain breaking
  API changes; they are called out in the release notes. **Patch** releases
  (`v0.N.x`) are always backwards compatible.
- **`v1+`** — standard semver guarantees: breaking changes only in a new major
  version (with a `/vN` module path).
- Deprecated identifiers are marked with a `// Deprecated:` godoc comment and
  kept for at least one minor release before removal.

Releases are created by pushing a `vX.Y.Z` tag; the
[release workflow](.github/workflows/release.yml) publishes a GitHub release
with generated notes.

## Development

All tool versions are pinned in [`versions.env`](versions.env) and installed
into `./bin` on demand.

```bash
make help          # list all targets
make all           # everything CI runs
make tidy-check    # go.mod/go.sum are tidy
make fmt vet lint  # formatting, vet, golangci-lint
make test          # unit + envtest tests with race detector and coverage
make test-examples # build and test runnable samples under examples/
make reuse-lint    # REUSE / SPDX license compliance
make vuln          # govulncheck
make test-e2e-dynamiccache # dynamiccache E2E on a throwaway kind cluster (docker)
```

## Contributing

Contributions are welcome. Please read [CONTRIBUTING.md](CONTRIBUTING.md) and
the [Code of Conduct](CODE_OF_CONDUCT.md). AI coding agents should follow
[AGENTS.md](AGENTS.md).

## Security

Please do **not** report vulnerabilities via public issues. See
[SECURITY.md](SECURITY.md).

## License

Copyright 2026 Deutsche Telekom AG.

Licensed under the [Apache License, Version 2.0](LICENSE). Documentation and
configuration files are licensed under CC0-1.0. The project is
[REUSE](https://reuse.software/) compliant; see [REUSE.toml](REUSE.toml).
