# cub commander

A `cub` plugin: a full-screen terminal lab for the ConfigHub data model. One pipeline query
language over the server's real primitives (list + where, Filters, Views, functions), with
`EXPLAIN` printing the equivalent `cub` command. SQL SELECT is accepted as an on-ramp. Design in `docs/design.md`, milestones
in `docs/roadmap.md`.

## Install

You need [`cub`](https://docs.confighub.com) logged in (`cub auth login`) and Go 1.25+.

```
git clone https://github.com/confighub/cub-commander
cd cub-commander
make plugin        # builds and runs: cub plugin install ./bin/cub-commander
cub commander
```

Everything is read-only against your ConfigHub org. The first screen is the "browse by"
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
