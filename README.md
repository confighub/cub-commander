# cub commander

A `cub` plugin: a full-screen terminal lab for the ConfigHub data model. One pipeline query
language over the server's real primitives (list + where, Filters, Views, functions), with
`EXPLAIN` printing the equivalent `cub` command. SQL SELECT is accepted as an on-ramp. Design in `docs/design.md`, milestones
in `docs/roadmap.md`.

### Resource evidence (unreleased)

| User question | Surface | Evidence and limits |
|---|---|---|
| Can I inspect the live object behind this selected configuration resource? | Resource detail, `3 Evidence` | An explicit Target ID to kube-context binding and an exact API version, kind, namespace and name. No automatic cluster selection. |
| What did the observer actually read? | Evidence JSON and displayed command | One discovery document and one object GET per successful cold read. Ownership, object-local readiness, observed source metadata and omissions, not a desired/live diff or proof of deployment success. |
| Will navigating tabs keep hitting my cluster? | Captured snapshot, `r` refresh | Tab revisits reuse the selected snapshot without requests. Capture/expiry timestamps and `STALE` label remain visible; refresh discards the old success before reading. No background polling. |
| Can a missing binding or failed read look healthy? | Unavailable state | Missing identity/binding prevents execution. Failed, incompatible and mismatched responses are unavailable; no guessed source links or healthy fallback. |

Requires a Scout build containing the bounded `explain` contract (planned v2.10;
v2.9.0 does not include it). Start with an explicit binding:

```sh
cub commander --scout-binding '<target-id>=<kube-context>'
# Or use a locally built standalone provider:
cub commander --scout-binary /absolute/path/to/cub-scout --scout-binding '<target-id>=<kube-context>'
```

Open a Resource row and select `3 Evidence`; `r` refreshes. This tab is read-only;
existing Data editing and rollout actions are separate. See
[resource evidence](docs/resource-evidence.md) for scope, examples and proof.

## Install

You need [`cub`](https://docs.confighub.com) logged in (`cub auth login`).

```
cub plugin install confighub/cub-commander
cub commander
```

That fetches the latest release binary for your OS and architecture. To build from source
instead (Go 1.25+):

```
git clone https://github.com/confighub/cub-commander
cd cub-commander
make plugin        # builds and runs: cub plugin install ./bin/cub-commander
```

Queries and the Evidence tab are read-only. `e` on a unit's Data tab opens `$EDITOR`
and saves your edit as a new revision, guarded by the hash you read; rollout
promotion and release are separate, confirmation-gated writes. The first screen is the "browse by"
chooser; `^/` shows the keys. This is an early lab, so expect rough edges and a moving
language; the design is in `docs/design.md`.

## Examples

```
make plugin
cub commander -e "Unit | in * | where Labels.Environment = 'prod' | columns Slug, Space.Slug, Target.Slug"
cub commander -e "EXPLAIN Unit | where HeadRevisionNum > LastReleasedRevisionNum | where Slug LIKE 'a%' OR Slug LIKE 'b%'"
cub commander -e "Unit | in * | columns Labels.Environment as env, COUNT(*) as n | group by env | order by n desc"
cub commander -e "SHOW ENTITIES; SHOW JOINS FROM Unit"
```

`cub commander` with no flags opens the TUI on a "browse by" chooser built from the org's
label keys: pick a path (Component → Environment → Region → Cluster, Space → Unit, …) and
move through Finder-style panes with counts; `g` turns the selections into where steps and
shows the grid. `m` marks a selection as side A, `m` again as B, and `d` diffs the like units
across them (dev vs prod, one cluster vs another) pair by pair. Below that sits a command area (Tab completes,
Enter runs a complete statement, Alt+Enter always runs, Up/Down walk history, Ctrl+R
searches it), a
results grid (Shift+Tab to focus; `f` filters by the focused cell, `-` drops the last chip, `o`
orders by the column, Enter opens the row, `s t u d r l` pivot to the row's space, target,
upstream, downstreams, revisions, links), Ctrl+X for the plan and cub command, Ctrl+/ for
help, Ctrl+Q to quit. No F-keys.
`-e` stays as the scripting and test surface.
