# Resource evidence

Available in Commander v0.3.0 with Scout v2.10.0. The bounded `explain` contract was merged in
[provider #522](https://github.com/confighub/cub-scout/pull/522) and extended with
observed origin metadata in [#523](https://github.com/confighub/cub-scout/pull/523).
Scout v2.9.0 does not have this contract. Publish/install the provider first.
Work is tracked in
[observer #519](https://github.com/confighub/cub-scout/issues/519).

## Use

```sh
cub commander --scout-binding 'target-id=team-a-context'
# Repeat for another target. Duplicate IDs are errors, not last-one-wins.
cub commander --scout-binding 'target-a=context-a' --scout-binding 'target-b=context-b'
```

In the command area, run `Resource | in *`, focus the results, open one row, and
select `3 Evidence`. `1` returns to Metadata, `2` to Data, `r` refreshes evidence,
and Esc leaves detail. `e` does nothing in Evidence; Data editing and rollout
actions retain their existing behavior. The displayed command can also be run
directly for CLI access to the same evidence.

Default provider: `cub scout`. To use a standalone binary without changing installed
plugins, pass `--scout-binary /absolute/path/to/cub-scout`. The executable and its
kubeconfig/credential helpers must be trusted. Credentials are inherited through
the normal environment/configuration, never inserted in command arguments.

## Identity and meaning

Only the selected Resource's own `ResourceID`, `SpaceID`, `UnitID`, `TargetID`,
`ResourceType` and `ResourceName` are used. The type must encode the exact API
version and kind, such as `apps/v1/Deployment`. Names are `namespace/name`, with
`/name` for cluster-scoped objects. A custom column selection that omits required
identity is unavailable. Missing Target ID or binding means no provider process.
Target slugs, labels, current kubectl context, joined Unit fields and observed
origin metadata never supply missing bindings. Bindings are fixed for this run;
restart with different bindings to change cluster selection.

The provider receives only `explain Kind/name --bounded --api-version ...
--kube-context ... --namespace ... --format json`. No shell, fallback operation,
controller command, ConfigHub lookup or mutation is requested. Secret payloads
and subresources are excluded. The existing resource-pane query selects TargetID
alongside its other identity fields; this adds no extra metadata request.

The panel preserves bounded ownership/readiness, source-annotation evidence,
omissions and notes. Object-local readiness does **not** prove source delivery,
desired/live agreement, dependency completion or application success. Origin is
an observed annotation claim, not an authenticated link to a release, target,
component or variant. This first slice does not traverse those entities or
replace the standalone explorer.

## Request and freshness policy

- Each successful provider process reports one API discovery document plus one
  object GET. No object LIST, source/controller, pod or event fan-out. Provider
  retries/redirects are disabled. Authentication helper traffic and the host's
  normal ConfigHub browsing are outside this bound.
- Only one selected-resource snapshot is retained. Switching tabs reuses it with
  zero provider requests, even after expiry. It is always labeled a snapshot;
  capture/expiry times and `STALE` distinguish old evidence. There is no polling.
- `r` discards the old snapshot before starting a new process. A failed refresh
  never restores old success. A new process reads current credential configuration;
  merely viewing a retained snapshot does not revalidate credentials or access.
- Navigating away cancels an in-flight read and rejects late results. Changing the
  selected identity discards evidence. Application shutdown waits for active
  provider processes to finish cancellation and be reaped.
- Whole subprocess timeout: 15 seconds. stdout: 2 MiB; stderr: 64 KiB. macOS/Linux
  cancellation kills the process group, including the plugin wrapper's children.
  The buffer cannot grow past its limit. Raw stderr is not displayed.
- A response must match exact context/API/kind/namespace/name and the bounded
  count/freshness contract. Missing or incompatible envelopes, duplicate JSON keys,
  implausibly future timestamps (over one second), invalid capture/expiry windows
  and mismatched scopes are unavailable. JSON strings
  are escaped; unknown top-level payload fields are not displayed. Unavailable
  reads do not display ownership/health fields as observations.

## Reproducible proof

For an interactive terminal check with a synthetic intended-state server, use
the [resource evidence example](../examples/resource-evidence/README.md).

The [offline observation fixture](../internal/scout/testdata/observation.json)
contains a synthetic exact identity, origin claim, omission and capture window.
It is not production data or a claim about current cluster state.

```sh
go test ./internal/scout ./internal/tui -run 'Test(Bindings|Resolve|CommandHas|Decode|Subprocess|ProcessSession|Evidence)' -count=2
go test ./...
go vet ./...
go build ./...
```

Tests cover exact identity (including cluster scope), missing metadata/binding,
argument quoting/no shell, scope mismatches, malformed/duplicate JSON, integer
precision, output overflow, cancellation, tab reuse, stale status, refresh
failure, read-only tab actions, selection collisions and narrow viewport wrapping.

The opt-in integration test uses a local mock ConfigHub Resource endpoint and the
real provider against an **existing** Kubernetes object. It creates no cluster
resources, changes no installed plugins, and does not use ConfigHub authentication:

```sh
COMMANDER_SCOUT_LIVE_BINARY=/absolute/path/to/cub-scout \
COMMANDER_SCOUT_LIVE_CONTEXT=explicit-nonproduction-context \
COMMANDER_SCOUT_LIVE_TYPE=apps/v1/Deployment \
COMMANDER_SCOUT_LIVE_NAME=namespace/name \
go test ./internal/tui -run '^TestEvidenceLive$' -count=1 -v
```

It asserts one intended-state GET, two provider invocations (open plus refresh),
zero provider invocations on tab revisits, available matched observations and
rendered evidence. Each successful response's discovery/object counts are
validated by the adapter. This is not proof of a production ConfigHub-to-cluster
mapping; the mapping and selected intended-state row are explicit test inputs.
