# Synthetic Redfish tests

Use [`github.com/stmcginnis/gofish`](https://github.com/stmcginnis/gofish) directly
for production Redfish clients. Its `ConnectContext`, `APIClient.WithContext`,
`Service.Systems`, `ComputerSystem.Reset`, `ComputerSystem.SetBoot`,
`Manager.VirtualMedia`, `VirtualMedia.InsertMedia` / `EjectMedia`, resource
`Patch`, and inventory schemas already cover standard protocol operations.
Configure HTTP timeouts and trust using `ClientConfig.HTTPClient`; use HTTPS for
real services. Keep vendor-specific policies in consumers, not this library.

## Upstream alternatives considered

Gofish v0.26.0 (latest release verified during development) supplies resource
decoding, action discovery, ETags, structured errors and an in-memory
`schemas.TestClient`. That client is useful for schema unit tests but does not
exercise HTTP authentication, request cancellation or a stateful service.
The stdlib `httptest.Server` provides the transport. This package adds only
synthetic resource routing, synchronized mutable state and fault injection on
top of it. It does not wrap or replace gofish production APIs.

The separate module keeps Redfish-specific compatibility tests and dependencies
out of the root module. Only the Go standard library is used by the fake itself;
gofish is used by tests and the runnable example.

## Usage

```go
server := redfishtest.New(t, redfishtest.Config{})
client, err := gofish.ConnectContext(t.Context(), gofish.ClientConfig{
    Endpoint: server.URL,
    BasicAuth: true,
})
// Check err, then use the standard gofish APIs.
```

`New` registers `t.Cleanup`; `Start` requires explicit `Close`. See the
[runnable example](../examples/redfish/) for a complete direct-gofish workflow:
`cd examples/redfish && go run .`.

The standalone example uses the Redfish module from this checkout through a
local-path replacement, so it runs without downloading a historical revision.
Consumers can import the separately released public Redfish module at v0.1.0.

Systems have configurable IDs, initial power states, reset actions and boot
overrides. IDs must be unique nonempty ASCII letters, digits, underscores or
hyphens; construction panics on invalid IDs or a negative system count.
`ResetDelay` simulates PoweringOn/PoweringOff transitions. Managers
expose two synthetic CD/DVD slots with insert/eject actions and PATCH.
SessionService supports its service-wide timeout, not session login or renewal.
The root is anonymously discoverable; other resources optionally require Basic
Auth. `TLS` uses the stdlib synthetic certificate; trust it with `server.Client()`.

`Snapshot` returns detached synchronized state. `SetFault` matches an exact
method/path and injects statuses, cancellable delays, consumable failures and
ExtendedInfo bodies. A successful status can acknowledge without mutating.
`RequireETag` enforces conditional writes; ETags are service-wide (not per
resource), deliberately making concurrent writes conflict. ETags are emitted
only on GET responses; refresh a resource before the next conditional write.
Faults bypass normal
resource handling. This is a bounded test fake, not a conformant Redfish emulator:
no OEM behavior, schema validation, TaskService, real image fetches or sessions.
Use synthetic credentials and image URLs only.

## Adoption / provenance

No external consumer was identified in the
[2026-10-07 consumer audit](../README.md#consumer-audit-and-retention).
BOOTy [#574](https://github.com/telekom/BOOTy/pull/574) tried and rejected adoption
because virtual-media insertion, boot defaults and collection names differ.
Its existing fixtures must not be replaced without preserving those contracts.
The audit recommends future nested-module deprecation unless a compatible
consumer is demonstrated; no API is deprecated or removed by that assessment.

The original extraction targets below are provenance and possible future
use, **not** evidence of current adoption. The fake generalizes test plumbing
without copying real BMC responses:

- BOOTy: its `test/e2e/redfish/mock_server.go` remains consumer-local.
  `redfishtest.New` / `Snapshot` are not a semantics-preserving drop-in.
- An internal bare-metal provisioning operator: replace its generic HTTP fixture constructor
  with this fake; keep vendor-policy fixtures
  private. Production connection and media helpers should adopt the standard
  gofish APIs listed above, retaining only policies not supplied upstream.
- Another internal bare-metal provisioning operator: use gofish directly for generic inventory/Redfish operations
  where needed. Its consumer-specific provisioning adapters are not replaced by
  this module; no internal schemas or fixtures have been extracted.

Client, power, media and inventory wrapper implementations were intentionally
discarded under the upstream-first policy. Polling/readback, retries and
vendor-specific fallbacks remain explicit consumer policy; no shared wrapper
was retained without demonstrated repeated glue.
