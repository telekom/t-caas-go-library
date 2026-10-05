# Examples

Each library package has a runnable sample in `examples/<name>/`:

- `package main` with a `main.go` showing realistic usage of the package;
- a `main_test.go` exercising it (use envtest via `KUBEBUILDER_ASSETS` when an
  API server is required — `make test-examples` provides it);
- no external cluster or network access required.

Most examples are part of the root module. Niche examples such as `redfish/`
have their own nested module to keep dependencies out of root consumers; run
them from their directory. All examples are built and tested in CI by
`make test-examples`, which discovers both root and nested modules.
