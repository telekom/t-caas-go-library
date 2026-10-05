# Security Policy

## Reporting a Vulnerability

**Do not** open a public GitHub issue for security vulnerabilities. Instead,
report them privately via:

- 🔒 GitHub private vulnerability reporting (once enabled after publication):
  [Report a vulnerability](https://github.com/telekom/t-caas-go-library/security/advisories/new)
- 📧 [opensource@telekom.de](mailto:opensource@telekom.de)

Until private vulnerability reporting is enabled, use the email address above.

Please include a description of the issue, affected package(s) and
version(s), steps to reproduce and the potential impact.

We acknowledge reports within 48 hours and keep reporters informed about the
assessment, fix and coordinated disclosure. Reporters are credited unless they
request otherwise.

## Supported Versions

This library is pre-1.0. Only the latest released minor version receives
security fixes. Consumers should track the latest `v0.x.y` release.

## Security Practices

- **govulncheck** on every push, pull request and weekly.
- **CodeQL** static analysis and **dependency review** on pull requests
  (enabled once the repository is public / GitHub Advanced Security is available).
- **Dependabot** for Go modules and GitHub Actions.
- **golangci-lint** including `gosec`.
- **OpenSSF Scorecard** (public repository only).
- All GitHub Actions are pinned by full commit SHA.
- **REUSE** license compliance checks.
