# Contributing

Thank you for your interest in contributing to the T-CaaS Go Library!

## Code of Conduct

This project follows the [Contributor Covenant](CODE_OF_CONDUCT.md). By
participating you agree to uphold it.

## Reporting Issues

- **Bugs and feature requests:** open a GitHub issue with a clear description
  and, for bugs, a minimal reproduction.
- **Security vulnerabilities:** never open a public issue; follow
  [SECURITY.md](SECURITY.md).

## Development Setup

Requirements: Go (version from `go.mod`), `make`, and optionally
[`reuse`](https://reuse.software/) (or `uvx`/`pipx`) for license checks.
All other tools (golangci-lint, setup-envtest, govulncheck) are pinned in
`versions.env` and installed into `./bin` automatically.

```bash
make all   # tidy-check, fmt, vet, lint, test, test-examples, reuse-lint, vuln, licenses, fuzz
```

## Pull Requests

1. Fork the repository and create a topic branch from `main`.
2. Keep changes focused; one package or concern per PR.
3. Use [Conventional Commits](https://www.conventionalcommits.org/) for
   commit messages and PR titles (`feat(ssa): ...`, `fix: ...`, `chore: ...`).
4. Make sure `make all` passes locally.
5. Fill out the pull request template.

### Requirements for new packages and APIs

- Live under `pkg/<name>/` with a package doc comment.
- Godoc on **every** exported identifier.
- Unit tests (target > 80% coverage); envtest-based tests where API-server
  semantics matter (SSA, status subresource, conflicts, field managers).
- At least one `Example` test (rendered on pkg.go.dev).
- A runnable sample under `examples/<name>/` that is built and tested in CI.
- Dependencies limited to the standard library and the Kubernetes,
  controller-runtime, OpenTelemetry and Prometheus ecosystems. Never import a
  consumer repository.
- Generic APIs — no operator-specific types, names or behaviour.
- Breaking changes must be called out in the PR description (see the
  versioning policy in [README.md](README.md#versioning)).

### Licensing

Run `make licenses` to check production and test dependency licenses in every
module, and `make fuzz` for a short run of the IP arithmetic and subdivision
fuzzers. CI runs both checks.

All files must be [REUSE](https://reuse.software/) compliant. Go files start with:

```go
// SPDX-FileCopyrightText: 2026 Deutsche Telekom AG
//
// SPDX-License-Identifier: Apache-2.0
```

Other files are covered by globs in [REUSE.toml](REUSE.toml). Run
`make reuse-lint` before pushing.

By contributing you agree that your contributions are licensed under the
Apache License 2.0.
