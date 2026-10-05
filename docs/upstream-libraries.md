# Before writing helpers: use upstream

**Mandatory for humans and AI agents, here and in consumer repositories:** check
this guide and the [available library packages](../README.md#packages) before
adding a helper. When a well-known maintained upstream package covers the need,
use it directly; do not create another implementation or framework.
This applies even when an adoption PR in a consumer repository has not merged.
An unmerged adoption is not a reason to duplicate the upstream implementation.

Convenience wrappers are an exception only when **the same glue is demonstrably
repeated across multiple consumer repositories**. Cite `repo:file:line` call sites
in the PR's **Upstream alternatives considered** section and explain the exact
delta. Single-consumer defaults, predicates, formatting and lifecycle policies
stay local. If no suitable upstream exists, explain the missing capability or
unacceptable semantics concretely; do not claim that upstream supplies a
domain-specific policy it does not implement.

## Decision table

Package links below are exact Go import paths, not suggestions to import a whole
module. These are recommendations, **not dependencies added by this document**.
Check licenses, dependency weight and the consumer's Go/Kubernetes versions;
heavy or niche adapters should use nested modules. The repository compatibility
matrix is not a claim that every older consumer exposes the newest APIs.

**Available** means merged on main at the documentation snapshot (2026-10-04).
**Pending** means proposal/recommendation only: check the linked PR before
importing it. Never adopt code from a closed-as-redundant branch.

| Concern | Upstream import path and use | What this library adds / pitfalls |
|---|---|---|
| Conditions | [`github.com/fluxcd/pkg/runtime/conditions`](https://pkg.go.dev/github.com/fluxcd/pkg/runtime/conditions): `Get`, `Set`, `MarkTrue`, `MarkFalse`, `MarkReconciling`, `MarkStalled`, summaries; [`github.com/fluxcd/pkg/apis/meta`](https://pkg.go.dev/github.com/fluxcd/pkg/apis/meta): condition constants | No `pkg/conditions`; use upstream conditions APIs. Flux interfaces also require `metav1.Object`; observed generation comes from the object. Formatted helpers need `"%s", message` for a literal message. |
| Condition slices / transition time | [`k8s.io/apimachinery/pkg/api/meta`](https://pkg.go.dev/k8s.io/apimachinery/pkg/api/meta): `FindStatusCondition`, `SetStatusCondition`, `RemoveStatusCondition` | Setters return a changed boolean. `LastTransitionTime` tracks **status transitions**, not every reason/message/generation edit; preserve timestamp/order expectations in migration tests. Choose slice-level setters if explicit observed generation/change detection is needed. |
| Generic resource readiness / kstatus | [`sigs.k8s.io/cli-utils/pkg/kstatus/status`](https://pkg.go.dev/sigs.k8s.io/cli-utils/pkg/kstatus/status): `Compute` for arbitrary resources | No custom engine. Ready has positive polarity; Reconciling/Stalled are abnormal-true conditions, not success flags. Flux `IsReady` excludes active abnormal conditions; `MarkReconciling`/`MarkStalled` clear the opposite abnormal condition but do not coordinate Ready writes. Preserve consumer Ready=False vs Unknown and generation policy explicitly. |
| Snapshot status / object patching | [`github.com/fluxcd/pkg/runtime/patch`](https://pkg.go.dev/github.com/fluxcd/pkg/runtime/patch): `NewHelper`, `NewSerialPatcher`, `WithOwnedConditions` for deferred patches | Do not recreate a patch engine. Available [`pkg/patch`](../pkg/patch) supplies only repeated fresh-read/mutate/no-op/optimistic-lock retry composition, not a competing snapshot helper. |
| Optimistic locking / conflict retry | [`sigs.k8s.io/controller-runtime/pkg/client`](https://pkg.go.dev/sigs.k8s.io/controller-runtime/pkg/client): `MergeFromWithOptions` + `MergeFromWithOptimisticLock`; [`k8s.io/client-go/util/retry`](https://pkg.go.dev/k8s.io/client-go/util/retry): `RetryOnConflict` | GET fresh state inside each retry, keep mutations idempotent and return raw update errors. Retry sleeps are not a context-aware polling substitute. Use a live reader when stale cached reads would repeatedly conflict. |
| SSA / bulk manifest reconciliation | [`github.com/fluxcd/pkg/ssa`](https://pkg.go.dev/github.com/fluxcd/pkg/ssa): `ResourceManager.Apply` / `ApplyAll`; [`github.com/fluxcd/pkg/ssa/normalize`](https://pkg.go.dev/github.com/fluxcd/pkg/ssa/normalize): normalization | Prefer Flux for server-evaluated drift/defaulting, metadata cleanup, staged bulk apply, waiting and garbage collection. It uses dry-run PATCH before drift comparison and forces ownership; `ApplyOptions.Force` concerns immutable-resource recreation, not an ownership opt-out. Only [`pkg/ssa`](../pkg/ssa)'s typed cache/managedFields gate avoids both dry-run and real no-op admission traffic, as exercised in auth-operator [#577](https://github.com/telekom/auth-operator/pull/577)/[#580](https://github.com/telekom/auth-operator/pull/580). |
| Typed apply / status apply | [`sigs.k8s.io/controller-runtime/pkg/client`](https://pkg.go.dev/sigs.k8s.io/controller-runtime/pkg/client): `Apply`, `SubResource("status").Apply`, `FieldOwner`, `ApplyConfigurationFromUnstructured`; [`k8s.io/client-go/applyconfigurations`](https://pkg.go.dev/k8s.io/client-go/applyconfigurations): generated builders and extractors | Available [`pkg/ssa`](../pkg/ssa) supplies only repeated cache/managedFields comparison gates and explicit status-write convention; writes use these native APIs. Preserve caller ownership options. Cached no-op gates cannot detect changed admission policy or revoked permissions; always apply at authorization boundaries, and retain managedFields in caches. Status skips require exclusive controller ownership. CR-specific builders/comparators stay local. |
| envtest / test assets | [`sigs.k8s.io/controller-runtime/pkg/envtest`](https://pkg.go.dev/sigs.k8s.io/controller-runtime/pkg/envtest): `Environment`, binary-download/version options, `Start` / `Stop`, `WaitForCRDs` | No `pkg/envtestutil`. Pin assets and use an **absolute** `BinaryAssetsDirectory` or `KUBEBUILDER_ASSETS`; provision via setup-envtest in Make/CI. Do not infer versions from directory names. envtest has no kubelet. |
| Test assertions / managed test environments | [`sigs.k8s.io/controller-runtime/pkg/envtest/komega`](https://pkg.go.dev/sigs.k8s.io/controller-runtime/pkg/envtest/komega): `New(client).WithContext(ctx)`; [`github.com/onsi/gomega`](https://pkg.go.dev/github.com/onsi/gomega): `Eventually`; [`github.com/fluxcd/pkg/runtime/testenv`](https://pkg.go.dev/github.com/fluxcd/pkg/runtime/testenv): managed client/manager lifecycle | Prefer native envtest for raw clients and webhook-specific suites. Flux testenv's manager/cache/global environment assumptions are not interchangeable. Use `testing.TB.Cleanup` or Ginkgo `DeferCleanup`, not a second assertion framework. |
| E2E waits | [`sigs.k8s.io/e2e-framework/klient/wait`](https://pkg.go.dev/sigs.k8s.io/e2e-framework/klient/wait): `For`, `WithContext`, timeout/interval options; [`sigs.k8s.io/e2e-framework/klient/wait/conditions`](https://pkg.go.dev/sigs.k8s.io/e2e-framework/klient/wait/conditions): `New`, `PodReady`, `DeploymentAvailable`, `ResourceDeleted` | `pkg/kubetest` was never opened. Preserve observed generation, replica counts, empty-list handling and dependent-Pod deletion checks. Running/container status is not automatically equivalent to PodReady. |
| Manifest decoding / test cleanup | [`sigs.k8s.io/e2e-framework/klient/decoder`](https://pkg.go.dev/sigs.k8s.io/e2e-framework/klient/decoder): `DecodeAll`, `DecodeEach`; native client apply and test cleanup hooks | Apply with an explicit field owner; omit `ForceOwnership` when conflicts must fail. Cleanup uses a fresh bounded context, `IgnoreNotFound` and UID preconditions. Do not pre-delete an existing object unless destructive setup is intentional. |
| Port-forwarding | [`k8s.io/client-go/tools/portforward`](https://pkg.go.dev/k8s.io/client-go/tools/portforward): `NewOnAddressesForStreamingWithContext` or compatibility `NewOnAddressesWithContext`, `ForwardPorts`, ready channel, `GetPorts`; [`k8s.io/client-go/transport/spdy`](https://pkg.go.dev/k8s.io/client-go/transport/spdy): transport/dialer | Bind `127.0.0.1`, `"0:<remotePort>"` for atomic ephemeral allocation, not reserve-close-rebind. Service/target-port resolution stays local. In client-go v0.37.1 SPDY upgrade response-header reads can block despite cancellation; a context-bearing request alone does not fix startup. Keep context-bound kubectl where that contract is required; data-path tests need a real kubelet. |
| Predicates / handlers / indexers | [`sigs.k8s.io/controller-runtime/pkg/predicate`](https://pkg.go.dev/sigs.k8s.io/controller-runtime/pkg/predicate): `NewPredicateFuncs`, `TypedFuncs`, generation/label/annotation predicates; [`sigs.k8s.io/controller-runtime/pkg/handler`](https://pkg.go.dev/sigs.k8s.io/controller-runtime/pkg/handler): `EnqueueRequestsFromMapFunc`; client `FieldIndexer.IndexField` | `crhelpers` was never opened. Domain Node/Pod/status comparison and mapping stay local. Preserve create/delete/generic defaults when composing predicates; generation filtering alone misses relevant status changes. |
| Owner references / finalizers | [`sigs.k8s.io/controller-runtime/pkg/controller/controllerutil`](https://pkg.go.dev/sigs.k8s.io/controller-runtime/pkg/controller/controllerutil): owner setters, `HasOwnerReference`, `RemoveOwnerReference`, finalizer helpers; [`k8s.io/apimachinery/pkg/apis/meta/v1`](https://pkg.go.dev/k8s.io/apimachinery/pkg/apis/meta/v1): `NewControllerRef`; [`sigs.k8s.io/controller-runtime/pkg/client/apiutil`](https://pkg.go.dev/sigs.k8s.io/controller-runtime/pkg/client/apiutil): `GVKForObject` | Owner setters validate scope/controller exclusivity. `HasOwnerReference` compares group/kind/name, not UID: check UID explicitly for identity. Available `pkg/patch` adds retry composition around finalizer mutation; cleanup policy remains the consumer's. |
| Leader election | [`k8s.io/client-go/tools/leaderelection`](https://pkg.go.dev/k8s.io/client-go/tools/leaderelection): `NewLeaderElector`, callback contexts; [`k8s.io/client-go/tools/leaderelection/resourcelock`](https://pkg.go.dev/k8s.io/client-go/tools/leaderelection/resourcelock): `LeaseLock`; [`github.com/fluxcd/pkg/runtime/leaderelection`](https://pkg.go.dev/github.com/fluxcd/pkg/runtime/leaderelection): `Options.BindFlags` | No lifecycle/election package. Use per-term callback contexts, not reused closed channels. Flux flags do not supply reacquisition; one-shot IPAM and continuous manager leadership have different contracts. Election is not fencing. |
| Manager readiness / shutdown | [`sigs.k8s.io/controller-runtime/pkg/manager`](https://pkg.go.dev/sigs.k8s.io/controller-runtime/pkg/manager): runnables, `AddReadyzCheck`, `GracefulShutdownTimeout`; [`sigs.k8s.io/controller-runtime/pkg/healthz`](https://pkg.go.dev/sigs.k8s.io/controller-runtime/pkg/healthz): checks; [`k8s.io/client-go/tools/cache`](https://pkg.go.dev/k8s.io/client-go/tools/cache): `WaitForCacheSync`; [`github.com/fluxcd/pkg/runtime/probes`](https://pkg.go.dev/github.com/fluxcd/pkg/runtime/probes): probe flags | No custom lifecycle framework. Preserve selected-informer readiness and unexpected-error policy locally. Post-cancel cleanup uses `context.WithoutCancel` **plus** a finite timeout; `errors.Join` preserves multiple failures. |
| Config decoding / YAML | [`encoding/json`](https://pkg.go.dev/encoding/json): `Decoder.DisallowUnknownFields`; [`sigs.k8s.io/yaml`](https://pkg.go.dev/sigs.k8s.io/yaml): `UnmarshalStrict`; [`go.yaml.in/yaml/v3`](https://pkg.go.dev/go.yaml.in/yaml/v3): `Decoder.KnownFields(true)` for YAML-tagged streams | No reload/decoder wrapper. sigs YAML uses JSON tags; native YAML uses YAML tags. Strictness, validation, EOF/trailing-document rejection and last-known-good reload policy must be explicit. Do not silently tighten an existing permissive schema. |
| Tracing | [`go.opentelemetry.io/otel/sdk/trace`](https://pkg.go.dev/go.opentelemetry.io/otel/sdk/trace): provider, batching, `ParentBased(TraceIDRatioBased(rate))`, flush/shutdown; [`go.opentelemetry.io/otel/sdk/resource`](https://pkg.go.dev/go.opentelemetry.io/otel/sdk/resource): service metadata; [`go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc`](https://pkg.go.dev/go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc): exporter | No `pkg/tracing`. Application main owns globals; preserve scopes, attributes, validated sampling and bounded shutdown. Trace-specific endpoint env var precedes generic OTLP endpoint. |
| Exporter selection / propagation / disabled tracing | [`go.opentelemetry.io/contrib/exporters/autoexport`](https://pkg.go.dev/go.opentelemetry.io/contrib/exporters/autoexport): `NewSpanExporter`; [`go.opentelemetry.io/otel/propagation`](https://pkg.go.dev/go.opentelemetry.io/otel/propagation): composite propagator; [`go.opentelemetry.io/otel/trace/noop`](https://pkg.go.dev/go.opentelemetry.io/otel/trace/noop): no-op provider | autoexport defaults to http/protobuf: set `OTEL_EXPORTER_OTLP_PROTOCOL=grpc` to retain gRPC. Upstream `console` differs from a consumer's `stdout` spelling. Disabled tracing and enabled-with-no-exporter are distinct policies. |
| Metrics / events | [`github.com/prometheus/client_golang/prometheus`](https://pkg.go.dev/github.com/prometheus/client_golang/prometheus): registry, collectors, counter vectors and constrained labels; [`github.com/fluxcd/pkg/runtime/metrics`](https://pkg.go.dev/github.com/fluxcd/pkg/runtime/metrics): `NewRecorder`; [`github.com/fluxcd/pkg/runtime/events`](https://pkg.go.dev/github.com/fluxcd/pkg/runtime/events): event recorder | No `pkg/metrics`. controller-runtime already instruments admission. Flux readiness/suspension metrics are not arbitrary domain outcomes. Preserve names/labels, initialize zero series before registration and make collectors concurrency-safe; native Gather is not a custom scrape semaphore/timeout contract. |
| Logging / fan-out | [`sigs.k8s.io/controller-runtime/pkg/log`](https://pkg.go.dev/sigs.k8s.io/controller-runtime/pkg/log): `FromContext`; [`github.com/fluxcd/pkg/runtime/logger`](https://pkg.go.dev/github.com/fluxcd/pkg/runtime/logger): CLI options; [`log/slog`](https://pkg.go.dev/log/slog): Go 1.26 `NewMultiHandler`; [`github.com/samber/slog-multi`](https://pkg.go.dev/github.com/samber/slog-multi): older-Go fan-out/routing | No custom fan-out. Application entry points configure logging; pass context across helpers rather than configuring globals. |
| Webhook certificates / rotation | [`github.com/open-policy-agent/cert-controller/pkg/rotator`](https://pkg.go.dev/github.com/open-policy-agent/cert-controller/pkg/rotator): generation, renewal, Secret/CA injection; [`sigs.k8s.io/controller-runtime/pkg/certwatcher`](https://pkg.go.dev/sigs.k8s.io/controller-runtime/pkg/certwatcher): file reload | Available [`pkg/certrotation`](../pkg/certrotation): only repeated setup gating/mounted readiness/SAN glue; [four-consumer provenance and migration](../README.md#certificate-rotation-provenance-and-adoption). certwatcher reloads, not rotates. Non-leader readiness validates the mounted TLS pair and leaf dates, not CA injection; preserve CA identity, leadership and restart policies. Enable manager leader election for leader-only rotation; combine setup readiness with the webhook server's `StartedChecker`. Keep single-consumer Secret bootstrap local. |
| Kubeconfig / transports | [`k8s.io/client-go/tools/clientcmd`](https://pkg.go.dev/k8s.io/client-go/tools/clientcmd): `Write`, `RESTConfigFromKubeConfig`; [`k8s.io/client-go/tools/clientcmd/api`](https://pkg.go.dev/k8s.io/client-go/tools/clientcmd/api): `Config`; [`k8s.io/client-go/rest`](https://pkg.go.dev/k8s.io/client-go/rest): `TransportFor`; [`github.com/fluxcd/pkg/runtime/client`](https://pkg.go.dev/github.com/fluxcd/pkg/runtime/client): local flow-control/mapper options | No kubeconfig builder. Keep identity/context defaults and validation local. Use `http.Transport.Clone`, explicit roots/TLS/timeouts and context-bound requests rather than a generic HTTP-client framework. |
| Remote clients / fleets | [`sigs.k8s.io/controller-runtime/pkg/cluster`](https://pkg.go.dev/sigs.k8s.io/controller-runtime/pkg/cluster): cluster cache/lifecycle; [`sigs.k8s.io/multicluster-runtime/providers/kubeconfig`](https://pkg.go.dev/sigs.k8s.io/multicluster-runtime/providers/kubeconfig): dynamic remote controller fleets | Available [`pkg/remoteclient`](../pkg/remoteclient): **uncached** registry, generation fencing and Secret invalidation. Failed refresh keeps old client; returned clients are not revoked. Registry is not authorization/freshness fencing; perform live checks and wire Secret watches. Embedded credentials only; zero timeout means no timeout. |
| Kubernetes polling / backoff | [`k8s.io/apimachinery/pkg/util/wait`](https://pkg.go.dev/k8s.io/apimachinery/pkg/util/wait): `ExponentialBackoffWithContext`, `PollUntilContextTimeout`, `PollUntilContextCancel` | No custom retry scheduler. Validate finite backoff parameters and use context-aware waits when cancellation must interrupt sleeps. |
| General retry | [`github.com/cenkalti/backoff/v5`](https://pkg.go.dev/github.com/cenkalti/backoff/v5): `Retry`, `WithMaxTries`, `WithMaxElapsedTime`, `WithNotify`, `Permanent` | No retry engine. Capture context in the operation and keep provisioning/error classification local. |
| Circuit breaking | [`github.com/sony/gobreaker/v2`](https://pkg.go.dev/github.com/sony/gobreaker/v2): `CircuitBreaker.Execute`, `TwoStepCircuitBreaker.Allow`, `IsExcluded` setting | No breaker engine. Configure thresholds, neutral errors, HTTP classification, lifecycle and metrics explicitly; do not assume defaults preserve an existing breaker's semantics. |
| Token buckets / keyed limits | [`golang.org/x/time/rate`](https://pkg.go.dev/golang.org/x/time/rate): `NewLimiter`, token accounting | Available [`pkg/ratelimit`](../pkg/ratelimit/README.md): only bounded per-key lookup/LRU/idle expiry shared by auth-operator and k8s-breakglass. Use rate directly for global/single limits. The adapter validates rates in `(0, 1e9]`; choose cardinality, eviction, trusted keys and TTL explicitly. Eviction resets budgets; retain an independent global limiter. |
| Workqueue limiting / debounce | [`k8s.io/client-go/util/workqueue`](https://pkg.go.dev/k8s.io/client-go/util/workqueue): typed controller/max-of/exponential/bucket limiters; delaying queue `AddAfter`, `Get`/`Done`, `AddRateLimited`, `Forget` | No debounce/controller-options wrapper. Stable keys coalesce delayed additions; `Done` preserves triggers received during execution. Cancel workers, shut down queues and wait. Put configuration validation in the consumer. |
| IPs / CIDRs / sets | [`net/netip`](https://pkg.go.dev/net/netip): parse, compare, unmap, next/previous, masked prefixes/overlap; [`go4.org/netipx`](https://pkg.go.dev/go4.org/netipx): `PrefixLastIP`, ranges, `IPSetBuilder`; [`k8s.io/utils/net`](https://pkg.go.dev/k8s.io/utils/net): legacy IP operations | Available [`pkg/netutil`](../pkg/netutil): checked signed big-integer addition, budgeted fixed-prefix subdivision and repeated usable/broadcast conventions. Range-to-minimal-prefix covering is not fixed-size subdivision. Legacy integer offsets/overflow/saturation differ. Validate family/mapped-address policy. |
| MACs / numeric arithmetic | [`net`](https://pkg.go.dev/net): `ParseMAC`, `HardwareAddr.String`; [`math/big`](https://pkg.go.dev/math/big): arbitrary-precision arithmetic | No MAC wrapper. Go 1.26 accepts compact hex; older consumers must check version behavior. IPv6 has no broadcast. Preserve consumer /31, /32, /127, /128 policy and host-bit input rejection rather than assuming netutil is drop-in. |
| Redfish / Redfish test servers | [`github.com/stmcginnis/gofish`](https://pkg.go.dev/github.com/stmcginnis/gofish): `ConnectContext`; [`github.com/stmcginnis/gofish/schemas`](https://pkg.go.dev/github.com/stmcginnis/gofish/schemas): inventory, reset, boot, virtual media and `TestClient`; [`net/http/httptest`](https://pkg.go.dev/net/http/httptest): HTTP tests | Use gofish directly; no client/provisioning wrappers. Available nested-module [`pkg/redfish/redfishtest`](../pkg/redfish/redfishtest): only repeated stateful HTTP protocol/fault fixture ([usage and limitations](redfish.md)). Unit `TestClient` is not HTTP authentication/cancellation coverage. Preserve vendor policy privately. |
| HTTP clients / polling | [`net/http`](https://pkg.go.dev/net/http): `Transport.Clone`, `Client`, `NewRequestWithContext`; [`k8s.io/apimachinery/pkg/util/wait`](https://pkg.go.dev/k8s.io/apimachinery/pkg/util/wait): `PollUntilContextCancel` | No `pkg/httpclient`. Configure TLS/CA roots/timeouts directly, close polling response bodies and preserve unbounded download reads where required. BOOTy and internal provisioning clients keep their application-specific policy. |
| Checksums / digests | [`github.com/opencontainers/go-digest`](https://pkg.go.dev/github.com/opencontainers/go-digest): `Parse`, `Digest.Verifier` | No checksum wrapper. Register required crypto hash implementations; check read/copy errors before `Verified`, and distinguish incomplete input from mismatch. |
| OCI / image references | [`github.com/distribution/reference`](https://pkg.go.dev/github.com/distribution/reference): `ParseNormalizedNamed`, `FamiliarString` | No OCI wrapper. Tags/digests are reference syntax, not URL authority ports/userinfo. Keep registry authentication separate and remove an application-owned `oci://` prefix before parsing. BOOTy keeps credential-bearing source-URI diagnostics local; no repeated multi-repository OCI wrapper was demonstrated. |
| Process execution | [`os/exec`](https://pkg.go.dev/os/exec): `CommandContext`; [`k8s.io/utils/exec`](https://pkg.go.dev/k8s.io/utils/exec): injectable interface; [`k8s.io/utils/exec/testing`](https://pkg.go.dev/k8s.io/utils/exec/testing): fake | No exec wrapper. PID registry, output bounds and sanitized diagnostics stay application-owned. Cancellation of the direct process is not a universal subprocess-tree cleanup contract. |
| Build information | [`runtime/debug`](https://pkg.go.dev/runtime/debug): `ReadBuildInfo` and VCS settings | No buildinfo wrapper. Keep linker variables, dirty/default values and user-facing formatting local. |
| Names / validation / labels | [`k8s.io/apimachinery/pkg/util/validation`](https://pkg.go.dev/k8s.io/apimachinery/pkg/util/validation): Kubernetes name validation; [`k8s.io/apimachinery/pkg/labels`](https://pkg.go.dev/k8s.io/apimachinery/pkg/labels): selectors; metav1 `LabelSelectorAsSelector`, `ObjectMeta.GenerateName` | No hash/name sanitizer. Domain identity is not interchangeable with generated names. Available [`pkg/namespaceselector`](../pkg/namespaceselector) adds request-local live-read/error memoization; use a fresh resolver per request and an uncached reader. |
| Discovery / REST mapping / CRD waits | [`k8s.io/client-go/discovery`](https://pkg.go.dev/k8s.io/client-go/discovery): `ServerGroupsAndResourcesWithContext`; [`k8s.io/client-go/discovery/cached/memory`](https://pkg.go.dev/k8s.io/client-go/discovery/cached/memory): cached discovery; client apiutil dynamic mapper; wait/envtest CRD APIs | Available [`pkg/discovery/tracker`](../pkg/discovery/tracker): canonical snapshots/change callbacks and failed-version retention only. Partial errors are not complete success; cancelled transforms must not publish snapshots. No custom discovery protocol or CRD-wait engine. |
| Namespace caches | [`sigs.k8s.io/controller-runtime/pkg/cache`](https://pkg.go.dev/sigs.k8s.io/controller-runtime/pkg/cache): `New`, `DefaultNamespaces`, `ByObject.Namespaces` for static sets; [`k8s.io/client-go/tools/cache`](https://pkg.go.dev/k8s.io/client-go/tools/cache): ordinary shared informers; [`github.com/fluxcd/pkg/cache`](https://pkg.go.dev/github.com/fluxcd/pkg/cache): general-purpose TTL/LRU storage, not an informer cache | Available [`pkg/controllerruntime/dynamiccache`](../pkg/controllerruntime/dynamiccache/README.md) builds on upstream caches/informers, adding only runtime namespace-label membership, handler/index replay and cleanup deletes. Static consumers should keep stock controller-runtime; do not recreate its caching, indexing or informer engine while adoption changes are pending. See the [package's upstream notes](../pkg/controllerruntime/dynamiccache/README.md#upstream-alternatives-considered). |
| Secrets / URL diagnostics | Native client `Reader.Get`, `corev1.Secret.Data`; [`github.com/fluxcd/pkg/runtime/secrets`](https://pkg.go.dev/github.com/fluxcd/pkg/runtime/secrets): TLS/proxy/auth conversion; [`net/url`](https://pkg.go.dev/net/url): parsing / `URL.Redacted` | Fake fixtures should use Data; test StringData conversion with envtest. Available [`pkg/redact`](../pkg/redact): repeated URL stripping from BOOTy and an internal bare-metal provisioning operator, sanitized error chains, embedded diagnostics and UTF-8 bounds only. `URL.Redacted` masks password, not username/query/fragment. Keep consumer-specific secret/path scrubbing; never log unwrapped causes. See [package upstream notes](../pkg/redact/README.md) for dropped utilities and OCI handling; do not recreate them while adoption changes are pending. |

## Migration hints by source repository

These are recommendations from read-only extraction snapshots, **not statements
that consumer adoption has merged**. Line numbers are historical navigation aids;
verify present call sites before editing. Keep consumer-specific behavior and add
migration tests for semantic differences above.

- **[auth-operator](https://github.com/telekom/auth-operator):**
  `pkg/conditions/{getter,setter,kstatus}.go` → Flux/apimachinery/kstatus; retain
  coordinated Ready=False reconciliation policy. `pkg/tracing/tracing.go:94` →
  OTel directly with main-owned globals and bounded shutdown.
  `internal/controller/authorization/suite_test.go:69` and webhook suites →
  native envtest with pinned absolute assets. `test/utils/utils.go:172,183,203,412`
  → upstream waits/decoder/apply; preserve intentional SSA ForceOwnership.
  `test/e2e/cleanup.go:66` → cleanup hooks; existing context-bound kubectl
  forwarding can stay. `pkg/ssa/patchhelper.go:298,469,547,621,700` → consider
  `pkg/ssa` only for the repeated cache gate, not local RBAC semantics.
  `internal/webhook/certrotator/certrotator.go:18` / `cmd/webhook.go:146` →
  cert-controller, optionally `pkg/certrotation` gating. Discovery tracking can use the
  available tracker; preserve live authorization checks.
- **[k8s-breakglass](https://github.com/telekom/k8s-breakglass):**
  `api/v1alpha1/condition_helpers.go:66` → apimachinery setters;
  `pkg/utils/kstatus.go:82` already uses cli-utils. `pkg/telemetry/telemetry.go:66`
  → OTel/optional autoexport, preserving enabled-vs-disabled and exporter policy.
  `e2e/helpers/{wait,retry,client,portforward}.go` → upstream APIs; do not silently
  substitute PodReady for Running/container checks or discard destructive setup.
  `pkg/leaderelection/leaderelection.go:25` → per-term client-go contexts, keeping
  reacquisition policy local. `pkg/cluster/cache.go:74,791,955,963,1103` /
  `pkg/cluster/watchers.go:17` → consider available remoteclient for registry
  lifecycle only; retain OIDC, TTL, authorization and privileged live checks.
  `pkg/cluster/circuitbreaker.go:27` / `pkg/audit/circuit_breaker.go:54` →
  gobreaker with explicit classification. `pkg/utils/patchhelper.go:69,132`
  → native apply / `pkg/ssa` cache gates; `pkg/cert/cert.go:70` →
  cert-controller / optional `pkg/certrotation`.
- **[whereabouts](https://github.com/k8snetworkplumbingwg/whereabouts):**
  `pkg/iphelpers/iphelpers.go:40,52,64,79,93,166,175,290,304` → netip/netipx;
  `:105,111,115,340` → available netutil where checked arithmetic/budgeted
  subdivision is required. Preserve single-host and IPv6 endpoint behavior.
  `pkg/storage/kubernetes/ipam.go:574` already uses client-go: retain one-shot
  election. `e2e/client/{pod,replicaset,statefulset}.go` → context-aware waits
  with existing workload predicates and dependent-Pod checks.
  `internal/webhook/metrics.go:22` → direct CounterVec, preserving labels and
  zero series; webhook rotator/setup → cert-controller / optional `pkg/certrotation`.
- **[das-schiff-network-operator](https://github.com/telekom/das-schiff-network-operator):**
  `pkg/reconciler/intent/ipmath/ipmath.go:75,109` → netutil usable/broadcast
  convention; preserve /32 rejection. `pkg/monitoring/collector.go:65` →
  native Prometheus registry while retaining local collector setup and scrape
  duration/success series. `controllers/sync/remote_client.go:48,87,99,112` →
  available registry delta, leaving namespace enumeration local.
  `controllers/shared/predicates.go:34` → upstream name predicate with explicit
  delete/generic behavior; resource-specific comparators stay local.
  `pkg/debounce/debounce.go:11` → typed workqueue, not a custom timer engine.
  `e2etests/framework/cluster.go:173` → decoder/apply/waits;
  `cmd/operator/main.go:193` → cert-controller / optional `pkg/certrotation`.
- **[multi-networkpolicy-nftables](https://github.com/k8snetworkplumbingwg/multi-networkpolicy-nftables):**
  `pkg/controller/predicates.go:60` → name filter on `NewPredicateFuncs`;
  `:14` keeps domain Pod comparison in typed functions.
  `pkg/controller/indexes.go:23` / `mappers.go:17` → native indexing/mapping,
  preserving node scope. `cmd/multi-networkpolicy-nftables/main.go:63,210` →
  manager readiness/cache sync and bounded local cleanup. No repeated shared
  crhelpers/lifecycle wrapper was justified.
- **[BOOTy](https://github.com/telekom/BOOTy):**
  `pkg/retry/retry.go:11` / `pkg/provision/retry.go:13` → backoff/v5;
  `pkg/image/verify/verify.go:15` → go-digest with explicit incomplete-read checks;
  `pkg/executil/executil.go:17` → exec directly; `pkg/buildinfo/buildinfo.go:26` →
  ReadBuildInfo; `pkg/logging/multi.go:1` → upstream slog fan-out.
  `pkg/config/loader.go:15` → native decoders, preserving optional strictness,
  validation and trailing-document rejection. Use gofish directly for Redfish;
  `test/e2e/redfish/mock_server.go:46` is a candidate for the shared HTTP
  fixture, not another Redfish client. URL diagnostics may use `pkg/redact`;
  keep non-URL sensitive-field sanitization local.
- **Other/internal consumers (generic only):** construct kubeconfigs with
  clientcmd; use native envtest/komega and typed predicate/owner APIs; retain
  domain schemas, vendor policy, private fixtures and reload defaults locally.
  Use gofish for generic inventory/provisioning operations, and the shared HTTP
  fake only when its public protocol contract fits. Dynamic namespace-label
  informer membership may justify `pkg/controllerruntime/dynamiccache`; static namespace sets do not.
  Never publish internal repository paths, hosts, credentials or fixtures as
  wrapper evidence: describe the repeated glue generically in public material
  and keep sensitive verification evidence private.

## Decision record and verification

The decisions are recorded here so consumer adoption cannot be mistaken for
permission to reinvent upstream.

- Conditions, tracing/metrics, envtest and lifecycle/election/config wrappers
  were discarded as redundant with the upstream APIs above.
- Controller-helper and Kubernetes-test wrappers were discarded after comparing
  native predicates, handlers, indexers, owners, limiters, waits, decoders,
  retries and forwarding. These are not installable packages.
- Available deltas include patch, dynamiccache, redact, remoteclient, netutil,
  keyed ratelimit, API helpers, the Redfish HTTP test fixture, SSA cache gates
  and certificate-rotation setup glue.
- The packages' **Upstream alternatives considered** sections document removed
  duplicate engines and retained deltas; availability does not weaken upstream
  reuse.

Import paths and the referenced upstream APIs were checked against pkg.go.dev
or module source while preparing this guide. Pin compatible versions in the
consumer; in particular Go 1.26 MAC/slog and client-go v0.37.1 forwarding APIs
are not promises about older releases. This is a decision guide, not a second
dependency manifest or a guarantee of drop-in equivalence.
