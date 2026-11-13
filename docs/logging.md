# Logging

The library has no logger, log level or log format of its own. It logs through
the caller's [logr](https://github.com/go-logr/logr) logger, so the consuming
binary decides the level, format and destination of every line.

## Contract

- Library code obtains its logger with controller-runtime's
  `log.FromContext(ctx)` from the context passed to the call (or, for
  long-running components, the context their `Start`/`Run` method received).
  It never calls `ctrl.SetLogger`, never configures a global logger and never
  writes to stdout or stderr itself.
- To attach request fields or a logger name, put the logger into the context:
  `ctx = log.IntoContext(ctx, logger.WithValues("cluster", name))`.
  Inside a controller-runtime reconciler, `ctx` already carries the
  reconciler's logger (with `controller`, `reconcileID` and the object key).
- When the context carries no logger, `log.FromContext` returns
  controller-runtime's global `log.Log`. It forwards to the logger set with
  `ctrl.SetLogger`; until that call, lines are dropped and controller-runtime
  eventually prints a one-time warning that `SetLogger` was never called.
  Manager runnables (for example the `pkg/certrotation` readiness runnables)
  receive such a context, so they log through the global logger.
- `utilruntime.HandleErrorWithContext` (used by `pkg/controllerruntime/dynamiccache`)
  logs through the logger in the context as well, via klog's contextual
  logging. Without a logger in the context it falls back to klog's global
  logger (klog text on stderr unless the binary calls `klog.SetLogger`).
- Messages use logr levels: `Error` lines, `Info` lines (`V(0)`), and debug
  lines at `V(2)` or `V(3)`. Fields are logr key/value pairs.

## Controlling the output in a consumer

With controller-runtime's zap logger, bind its flags and install it once in
`main`:

```go
opts := zap.Options{} // sigs.k8s.io/controller-runtime/pkg/log/zap
opts.BindFlags(flag.CommandLine)
flag.Parse()
ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))
```

| Library level | Shown at zap level | zap flag |
|---------------|--------------------|----------|
| `error` | `error` and lower | `--zap-log-level=error` |
| `info` (`V(0)`) | `info` and lower (default) | `--zap-log-level=info` |
| `V(1)` | `debug` | `--zap-log-level=debug` |
| `V(2)` | `2` | `--zap-log-level=2` |
| `V(3)` | `3` | `--zap-log-level=3` |

`--zap-encoder` selects `json` or `console`. A consumer with its own level flag
(`debug`, `info`, ...) shows the library's `V(2)` and `V(3)` lines only when it
maps a level to zap level `-2`/`-3` or lower; plain `debug` stops at `V(1)`.

The example binaries under `examples/` that log (`certrotation`,
`dynamiccache`, `ssa`) accept these `--zap-*` flags. Their defaults are
controller-runtime's: `info`, `json`, stderr. The other examples print their
results to stdout and do not log.

## What each package logs

| Package | `error` | `info` | Debug (`V(n)`) |
|---------|---------|--------|----------------|
| `pkg/certrotation` | - | Waiting for webhook certificates, certificate found in the cert dir (`certDir`) or rotator ready, webhook setup complete. Logger name `certrotation`. | - |
| `pkg/controllerruntime/dynamiccache` | Per-namespace cache construction, indexer, informer and event-handler failures (retried on resync, with `namespace` and `gvk`), a cache that stopped with an error, failed snapshots of synthetic deletes, and event conversion errors. | Synthetic deletes skipped for an informer that never synced (`namespace`, `gvk`). | - |
| `pkg/discovery/tracker` | Failed discovery refresh and failed CRD watch. | - | - |
| `pkg/ssa` | - | - | `V(2)`: status apply skipped because unchanged, or applied unconditionally because the object is not in the cache (`name`). `V(3)`: apply skipped because unchanged, with the object key: the lower-camel kind (for example `configMap`) as key and the name as value, plus `namespace` for namespaced kinds. |
| `pkg/namespaceselector`, `pkg/netutil`, `pkg/patch`, `pkg/ratelimit`, `pkg/redact`, `pkg/remoteclient`, `pkg/redfish/redfishtest` | - | - | - |

Packages without log lines report failures only as returned errors.

### Dependencies that log on their own

- `pkg/certrotation` runs [cert-controller](https://github.com/open-policy-agent/cert-controller)'s
  rotator, which logs through controller-runtime's global logger under the name
  `cert-rotation` (start and stop, certificate refreshes, rotation errors).
- `pkg/controllerruntime/dynamiccache` and `pkg/remoteclient` build
  controller-runtime caches and clients, which log through the global logger
  (and client-go through klog).

Their level and format follow the consumer's `ctrl.SetLogger` (and
`klog.SetLogger`) configuration.

### Personal data

Logged fields contain Kubernetes object names, namespaces, GVKs and file
paths. Error texts can quote API server responses with object names. The
library logs no credentials, tokens or certificate material.

## Log reference

Every log call under `pkg/`, generated from the code. `<expr>` marks a value
computed at run time (for example the kind name configured on an `ssa.Applier`).

<!-- BEGIN GENERATED LOG TABLE -->
| Package | Level | Message | Fields |
|---|---|---|---|
| `pkg/certrotation` | info | `` cert rotator reported readiness `` | - |
| `pkg/certrotation` | info | `` waiting for webhook certificates `` | - |
| `pkg/certrotation` | info | `` webhook certificate found in cert dir `` | `certDir` |
| `pkg/certrotation` | info | `` webhook certificates ready, setup complete `` | - |
| `pkg/controllerruntime/dynamiccache` | error | `` dynamiccache: adding indexers failed (namespace stays unsynced, retried on resync) `` | `error`, `gvk`, `namespace` |
| `pkg/controllerruntime/dynamiccache` | error | `` dynamiccache: cluster-scoped cache stopped with error `` | `error` |
| `pkg/controllerruntime/dynamiccache` | error | `` dynamiccache: converting namespace event `` | `error` |
| `pkg/controllerruntime/dynamiccache` | error | `` dynamiccache: getting informer failed (namespace stays unsynced, retried on resync) `` | `error`, `gvk`, `namespace` |
| `pkg/controllerruntime/dynamiccache` | error | `` dynamiccache: namespace cache stopped with error `` | `error`, `namespace` |
| `pkg/controllerruntime/dynamiccache` | error | `` dynamiccache: registering event handler failed (handler stays unsynced, retried on resync) `` | `error`, `gvk`, `namespace` |
| `pkg/controllerruntime/dynamiccache` | error | `` dynamiccache: removing stale event handler `` | `error`, `namespace` |
| `pkg/controllerruntime/dynamiccache` | error | `` dynamiccache: snapshot failed — synthetic deletes are LOST, reconcilers will not observe object removal `` | `error`, `gvk`, `namespace` |
| `pkg/controllerruntime/dynamiccache` | error | `` failed to apply field index to namespace cache, will retry on resync `` | `error`, `namespace`, `field` |
| `pkg/controllerruntime/dynamiccache` | error | `` failed to construct namespace cache, will retry on resync `` | `error`, `namespace` |
| `pkg/controllerruntime/dynamiccache` | info | `` skipping synthetic deletes for never-synced informer `` | `namespace`, `gvk` |
| `pkg/discovery/tracker` | error | `` CRD watch failed `` | `error` |
| `pkg/discovery/tracker` | error | `` discovery collection failed `` | `error` |
| `pkg/ssa` | V(2) | `` <s.Kind> not in cache, applying status unconditionally `` | `name` |
| `pkg/ssa` | V(2) | `` <s.Kind> status unchanged, skipping apply `` | `name` |
| `pkg/ssa` | V(3) | `` <a.Kind> unchanged, skipping SSA apply `` | `<target.logKV>` |
<!-- END GENERATED LOG TABLE -->

## Keeping this document current

`logging_doc_test.go` parses every non-test file under `pkg/` and collects each
logr call (`Info`, `V(n).Info`, `Error`) and `utilruntime.HandleErrorWithContext`
call with its level, message and keys. It runs in `make test` and fails when:

- the table above differs from the code, so a new, changed or removed log line
  must update this document in the same pull request; or
- library code logs other than through the context logger (`fmt.Print*`,
  `fmt.Fprint*` to stdout/stderr, the standard `log` package, `slog`, `klog`,
  or `SetLogger`).

Regenerate the table with

```sh
go test . -run TestLoggingReference -update
```

and update the package summary above when a change alters what a package logs.
