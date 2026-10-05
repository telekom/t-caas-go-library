<!-- SPDX-FileCopyrightText: 2026 Deutsche Telekom AG -->
<!-- SPDX-License-Identifier: CC0-1.0 -->

# Redaction

`github.com/telekom/t-caas-go-library/pkg/redact` supplies URL stripping shared
by BOOTy and an internal bare-metal provisioning operator, sanitized error
wrapping, embedded diagnostics and bounded
UTF-8 truncation. It builds on `net/url`; `URL.Redacted` alone masks only the
password, leaving usernames, queries and fragments.

Redaction is best-effort, not a comprehensive secret scrubber. Keep
consumer-specific path and sensitive-field sanitizers. Wrapped causes remain
accessible through `errors.Is`/`errors.As`; never log the original cause.

## Use upstream instead for…

- **OCI/image references:** `github.com/distribution/reference`, using
  `ParseNormalizedNamed` and `FamiliarString` for tags/digests. References are not
  URL authorities: keep registry authentication separate, and strip an
  application-owned `oci://` prefix before parsing. BOOTy retains its
  credential-bearing source-URI diagnostics locally; no shared OCI wrapper
  was demonstrated across consumer repositories.
- **Checksums:** `github.com/opencontainers/go-digest`, using `Parse` and
  `Digest.Verifier`. Register required hashes and check read/copy errors before
  `Verified`; preserve incomplete-input and mismatch handling locally.
- **HTTP clients and polling:** `net/http`, using `Transport.Clone`, `Client`
  and `NewRequestWithContext`; `k8s.io/apimachinery/pkg/util/wait`, using
  `PollUntilContextCancel`. Keep explicit TLS/roots/timeouts and close bodies.
- **Build metadata:** `runtime/debug.ReadBuildInfo`, with application-owned
  linker variables and formatting.
- **Commands:** `os/exec.CommandContext`, or `k8s.io/utils/exec` with
  `k8s.io/utils/exec/testing` when an injectable interface is needed. Keep PID
  registries, output bounds and process-tree cleanup policy application-owned.
- **slog fan-out:** Go 1.26 `log/slog.NewMultiHandler`, or
  `github.com/samber/slog-multi` for older Go and routing.

These utility implementations were dropped, not wrapped or re-exported.
See the [upstream decision guide](../../docs/upstream-libraries.md) for exact
imports and consumer migration notes.
