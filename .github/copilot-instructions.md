# T-CaaS Go Library — Copilot Instructions

Repository conventions for GitHub Copilot. Keep in sync with `AGENTS.md`.

## Purpose

`github.com/telekom/t-caas-go-library` provides shared, generic Go helpers for
Telekom T-CaaS Kubernetes operators (first consumers: `telekom/auth-operator`,
`telekom/k8s-breakglass`). This repository is **public** — treat every
file as open source.

## Quick Start

```bash
make all           # tidy-check fmt vet lint test test-examples reuse-lint vuln licenses fuzz
make test          # unit + envtest (pinned setup-envtest, absolute KUBEBUILDER_ASSETS)
make test-examples # build + test examples/
make lint          # golangci-lint v2 (pinned, installed into ./bin)
```

Tool versions live in `versions.env` (shared by Makefile and CI).

## Layout

```
doc.go               root package doc (no code)
pkg/<name>/          library packages (one concern each)
examples/<name>/     runnable sample per package (package main + main_test.go)
e2e/<name>/          opt-in E2E suites (skipped by `make test`, own path-filtered workflow)
.github/workflows/   CI, REUSE, security scan, scorecard, release
```

## Rules

**MANDATORY upstream-first rule (here and in consumer repositories):** before
adding any helper, check
[docs/upstream-libraries.md](../docs/upstream-libraries.md)
and this library's available packages. Do not write custom code when a
well-known maintained upstream package covers the need, even if consumer
adoption PRs have not merged. Convenience wrappers are allowed only for the
same glue repeated across multiple consumer repositories: cite `repo:file:line`
call sites and justify the delta in the PR's **Upstream alternatives considered**
section. Keep single-consumer policy local; explain concretely any unsuitable
upstream semantics. Never expose internal paths/hosts in public evidence.

1. **Dependencies:** only the Go standard library plus, as needed,
   `k8s.io/*`, `sigs.k8s.io/*` (controller-runtime etc.), `go.opentelemetry.io/*`
   and `github.com/prometheus/*`. Test-only: Ginkgo/Gomega or testify. Anything
   else needs explicit justification in the PR.
2. **Never depend on consumer repositories** (auth-operator, k8s-breakglass, …).
   No operator-specific types, names, CRDs or defaults — APIs must be generic
   (accept interfaces / `client.Object`, functional options, explicit field
   manager names, etc.).
3. **Every package needs:**
   - a package doc comment and godoc on **all** exported identifiers;
   - unit tests (target > 80% coverage);
   - envtest-based tests where API-server semantics matter (SSA, status
     subresources, conflicts, managed fields);
   - at least one `Example` / `ExampleXxx` test;
   - a runnable sample in `examples/<name>/` (`package main`) with a test, built
     and tested by `make test-examples` in CI;
   - an entry in the README package index.
4. **Errors:** wrap with `fmt.Errorf("context: %w", err)`; never `%v` for errors.
   Export sentinel errors / typed errors where callers need `errors.Is/As`.
5. **Logging:** context-aware only — `log.FromContext(ctx)` (controller-runtime
   / logr). Library code must not configure global loggers, tracers or metric
   registries; accept them as parameters/options instead.
6. **Context:** functions doing I/O take `ctx context.Context` as first argument.
7. **Standard constants:** `http.MethodGet`, `rbacv1.GroupName`, etc. instead of
   string literals.
8. **Import aliases** (enforced by `importas`): `corev1`, `rbacv1`, `metav1`,
   `apierrors`, `apimeta`, `ctrl`.
9. **Licensing (REUSE):** every `.go` file starts with
   ```go
   // SPDX-FileCopyrightText: 2026 Deutsche Telekom AG
   //
   // SPDX-License-Identifier: Apache-2.0
   ```
   Other files must be covered by `REUSE.toml` or carry a header. Run
   `make reuse-lint`.
10. **CI:** GitHub Actions must be pinned by full commit SHA with a `# vX.Y.Z`
    comment. Keep the tooling lean: no Docker images or Helm charts of our own;
    E2E suites only where envtest cannot prove a contract (e.g.
    `e2e/dynamiccache` against a kind cluster), opt-in and path-filtered.

## Compatibility Policy

- Semver; while `v0.x`, minor releases may break APIs (must be documented in
  release notes), patch releases never do.
- Track the Kubernetes minor of the consumers (currently Go 1.26,
  `k8s.io/*` v0.37, controller-runtime v0.25). Bumping a k8s/controller-runtime
  minor is a library minor release and updates the README compatibility matrix.
- Deprecate with `// Deprecated:` for at least one minor release before removal.

## Commits & PRs

Conventional Commits (`feat(ssa): …`, `fix: …`, `chore: …`). One package or
concern per PR. `make all` must pass.
