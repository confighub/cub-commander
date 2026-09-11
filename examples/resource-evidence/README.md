# Resource evidence terminal smoke

This local server supplies a synthetic intended-state Resource row. It cannot
contact ConfigHub or Kubernetes and refuses writes. Live reads are performed only
by the explicitly configured Scout executable after selecting the Evidence tab.

Choose an existing non-production object; do not create one for this example.

```sh
go run ./examples/resource-evidence -type apps/v1/Deployment -name namespace/name
```

In another terminal, use the loopback URL printed by the server:

```sh
go build -o /tmp/cub-commander-proof .
CUB_SERVER=http://127.0.0.1:PORT CUB_TOKEN=fixture-only CUB_CONTEXT=evidence-fixture \
  /tmp/cub-commander-proof --scout-binary /absolute/path/to/cub-scout \
  --scout-binding example-target=explicit-nonproduction-context
```

From the initial chooser, Shift+Tab focuses the command area. Run `Resource | in *`
there. Shift+Tab focuses the results; Enter opens the row. Then
press `3`. Verify the explicit context and resource, capture/expiry timestamps,
JSON and omissions. Switch to Metadata with `1`, then back with `3`: the capture
time must be unchanged. After 15 seconds the snapshot is stale. `r` produces a
new capture. Ctrl+Q exits; stop the local fixture server separately.

Remove the binding and repeat to verify unavailable evidence without executing
the provider. No plugin installation or ConfigHub login is needed. Normal local
statement history still applies; use an isolated home for disposable smoke runs.

See [the contract and automated proof](../../docs/resource-evidence.md).
