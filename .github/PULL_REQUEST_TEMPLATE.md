## Description

<!-- What does this PR change and why? -->

## Type of Change

- [ ] 🐛 Bug fix (non-breaking)
- [ ] ✨ New package / feature (non-breaking)
- [ ] 💥 Breaking API change (call out in description; see versioning policy)
- [ ] 📚 Documentation
- [ ] 🔧 CI / tooling
- [ ] 🏗️ Refactoring (no functional change)

## Related Issues

<!-- Fixes #123 / Relates to #123 -->

## Checklist

- [ ] `make all` passes locally (tidy-check, fmt, vet, lint, test, test-examples, reuse-lint, vuln, licenses, fuzz)
- [ ] All exported identifiers have godoc comments
- [ ] Unit tests added/updated (envtest where API-server semantics matter)
- [ ] `Example` tests added/updated
- [ ] Runnable sample under `examples/<name>/` added/updated
- [ ] No dependencies beyond stdlib / k8s / controller-runtime / OpenTelemetry / Prometheus
- [ ] No imports of consumer repositories
- [ ] README package index / compatibility matrix updated (if applicable)
- [ ] New files are REUSE compliant (SPDX header or covered by `REUSE.toml`)

## Consumer Impact

<!-- How does this affect auth-operator, k8s-breakglass or other consumers? Migration notes? -->
