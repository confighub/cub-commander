# cub commander — design

*A terminal lab for the ConfigHub data model. Status: proposal, 2026-09-02. Working name; see §10.*

## 1. What it is

`cub commander` is a `cub` plugin. It is one command that drops you into one full-screen TUI:
a main area that shows results or lets you browse the data, a multi-line command area at the
bottom, recallable history, and a way to turn what you are looking at into a saved View.
There is no separate REPL binary; the command area *is* the REPL, and the same engine backs
a hidden `-e` flag used for tests and scripts.

It is first a **lab**. `cub` already contains a real query model: an entity type, a SQL-ish
where clause, a column projection, saved Filters and Views, and functions that run over a
selector. It is hidden behind flags. Commander makes that model the primary interface, so that
exploring selectors, columns, joins, views, and functions is the *normal* thing to do, and so
that every gap in the model becomes visible and can be fed back into the product.

Phase 1 is read-only. Write (bulk mutation with what-if preview, changesets as transactions) is
sketched in §9 and designed separately.

## 2. The model as it exists today

Everything below is what the server and the live CLI (`confighub/public/cmd/cub/`) do now.
The `sdk/` checkout is a stale mirror and lacks `component`, `variant`, and `release` commands.

**Selection = From + Where.** A Filter entity is exactly `{From, FromSpaceID, Where, WhereData,
ResourceType}`. The where grammar (`confighub/internal/views/filter_parser.go`) is a flat
`AND`-conjunction of terms:

```
[LEN(] Attr [.mapkey | .jsonpath] [)] <op> ( literal | [Entity.[*.]]Attr ) [IS [NOT] TRUE|FALSE]
```

- Operators: `= != < > <= >=`, `LIKE NOT LIKE ILIKE ~~ !~~`, regex `~ ~* !~ !~*`,
  `IN (…) NOT IN (…)`, `IS NULL`, `IS NOT NULL`, `?` (array/map contains), `LEN()`.
- Literals: integer, `true|false`, single-quoted string with no quotes or backslashes inside.
  UUIDs and timestamps are strings coerced by attribute type. No floats, no `NULL` literal.
- **No `OR`, no `NOT`, no parentheses.** Top-level disjunction means multiple list calls; the CLI
  already does this itself in `unit tree` for links.
- A dot means one of three things, decided by the attribute's declared type: a map key
  (`Labels.tier`, `Values.Image/container-image`, `ApplyGates.team/fn`), a JSON path
  (`Data.spec.containers.?name=nginx.image`), or a join (`Space.Slug`,
  `UpstreamUnit.HeadRevisionNum`). Joins to list-valued references need `*`
  (`FromLink.*.Slug`, `Triggers.*.Slug`).
- The right-hand side may be another attribute: `UpstreamRevisionNum < UpstreamUnit.HeadRevisionNum`.
- Terms on the entity's own columns become SQL; terms on joined or computed attributes are
  evaluated **in memory on the server** after expansion. `Data.` terms are split out into the
  separate `where_data` parameter.
- Joinable prefixes per entity come from `EntityExpanders`. Unit: Organization, Space, Target,
  ChangeSet, UpstreamUnit, UpstreamSpace, ApprovedBy*, FromLink*, BridgeWorker, HeadRevision,
  LastReleasedRevision, HeadMutation, UnitEvent. Space: Triggers*, Attributes*, ReleaseTarget.
  Revision: Unit, User, ChangeSet, Tags*, ChangeOrders*, Releases*. Link: FromUnit, ToUnit,
  ToSpace. (`*` = list-valued.)

**Projection = View.** A View is `{Of | FilterID, Columns[], GroupBy, OrderBy, OrderByDirection}`
where a column is `MetadataAttribute` (`Space.Slug`, `Unit.Labels.Environment`),
`MetadataExpression` (CEL over the extended entity), `DataPath`, or `DataExpression` (CEL over
config data). `--columns` on list commands is the ad hoc subset: dotted paths only, no CEL.
`GroupBy` on a View is a sort-and-band, not an aggregation.

**Functions = the compute layer.** `cub function get|set|vet <fn> [args] --where …` runs one
function over a selector and returns one `FunctionInvocationsResponse` per unit, with an
envelope `{SpaceSlug, UnitSlug, OutputType, Output}`. There is no tabular output; the docs'
idiom is `jq`. Mutating functions support `--dry-run -o mutations`, which is a per-path what-if
diff. Getters, setters and vetters are minted in triples from a path registry (`get-image`,
`set-image`, `get-replicas`, …) and every `Attribute` entity mints a `get-<slug>`/`set-<slug>`
pair per space. Trigger-recorded `Values` make function results filterable
(`Values.Image/container-image LIKE '%:v1.2.%'`).

**Topology = labels plus per-unit pointers.** A Component is the set of Spaces sharing a
`Component` label; a Variant is a Space (its `Variant` label); a base is a variant with no
Target. Upstream/downstream is per unit: `UpstreamUnitID`/`UpstreamRevisionNum` plus an
`UpgradeUnit` Link. `cub unit tree --edge clone|link --node unit|space` walks it.
The docs' own mental model is the component × target matrix.

**History = Revision → Mutation.** Revisions are linear per unit; Mutations are per-path
provenance (which function, trigger, link, merge, or upgrade wrote it). `cub unit diff`
compares revisions of a unit or a unit `--with-unit` another (cross-space allowed). There is
**no live-vs-desired diff command** (you `unit data` and `unit livedata` to files) and **no
changeset diff**.

**Gaps this lab should make loud** (each is a product ask, not something to paper over):

1. No `OR`/`NOT`/parentheses in where.
2. No tabular output for function results across a selector; no way to use a function as a
   column or a predicate.
3. The queryable-attribute and join tables live only in server code; a client can't enumerate
   them (completion has to be derived, §6).
4. No live diff, no changeset diff, no variant-level (space-level) diff.
5. `Space.Slug = 'x'` is an in-memory join on the server when `SpaceID = '<uuid>'` would be SQL.
6. Views can't aggregate; `GroupBy` only bands.

## 3. Principles

1. **Honest compiler.** Every statement compiles to server primitives: list calls with
   `where`/`select`/`filter`/`view`, function invocations, and a clearly labelled *local*
   stage for what the server cannot do. `EXPLAIN` prints the exact `cub` command(s) and API
   calls. Nothing you can express here is un-expressible in `cub`; you just see it. This is
   what turns the tool into a teaching tool (configboard's "show me the query" principle).
2. **Everything is saveable.** An ad hoc query's WHERE is a Filter; its SELECT/GROUP/ORDER is a
   View. `CREATE FILTER` and `CREATE VIEW … AS SELECT` round-trip into real entities that
   `cub` and the web UI use. Saved Views are queryable sources (`FROM ops-home/by-target`).
3. **Navigation is query rewriting.** Every pane, pivot and drill-down in the TUI is a
   statement in the query line. Press `t` on a unit and the line becomes
   `… WHERE TargetID = '…'`. Enter on a cell appends `AND <col> = '<value>'`. You can always
   read, edit, copy, and save what the UI just did.
4. **Completion from a catalog, not a word list.** Attributes, types, valid operators per type,
   joins, label keys with counts, label values, `Values` keys, function signatures, saved
   filters and views, slugs. Context-aware at the cursor.
5. **Pushdown vs local is visible.** `WHERE` is server-side and must fit the grammar. `HAVING`
   is local and takes anything (OR, NOT, function columns). The result grid shows row counts at
   each stage.
6. **Typing is optional.** Most sessions should be keys, not statements. The command area
   always shows the statement the last keystrokes produced, so typing is the power path, not
   the entry path. How SQL-like the typed form is matters less than that it is one language
   for what you type and what the UI writes.

## 4. The query language

*Revised 2026-09-03: the canonical form is a pipeline; SQL SELECT is accepted as an on-ramp.*

A statement is an entity followed by steps. The pipes are optional because every step starts
with a keyword, but the tool always writes them. Keywords are case-insensitive. Identifiers
may contain `-`, `/` and `.` because the language has no arithmetic: `ops-home/by-target`,
`Values.Image/container-image`, `prod-eu` all lex as single identifiers. String literals
are single-quoted, as on the server.

### 4.1 Pipelines

```
<Entity> | <space>/<view> | <space>/<filter>
  [| in <space> | *]
  [| where <expr>] …
  [| columns <expr> [as alias], …]
  [| where <expr>] …
  [| group by <column>, …]
  [| order by <column> [desc], …]
  [| limit n]
```

```
Unit | in * | where Space.Labels.Component = 'checkout' AND HeadRevisionNum > LastReleasedRevisionNum
     | columns Slug, Space.Slug, Target.Slug, Labels.Environment
     | order by UpdatedAt desc

Unit | in * | where Data.kind = 'Deployment'
     | columns Space.Labels.Environment as env, COUNT(*) as units
     | group by env

ops-home/by-target | where Labels.Region = 'us-east'
```

- The entity comes first because it is what you chose first when navigating. Scope is the
  session's `USE` space; `in <space>` overrides it per statement. The default is org-wide
  (`USE *`, matching `--space '*'`), deliberately unlike `cub`, which defaults to the
  context's space: in a lab the space is a join column, and a query that silently returns
  three rows out of a hundred and fifty is the wrong surprise. The status line names the
  scope with every result.
- **Step order is plan order.** The leading run of `where` steps that fit the server grammar
  (a conjunction of attribute terms) goes to the server as one where clause. From the first
  step the server cannot take (`OR`, `NOT`, a function result, a column alias), every later
  step runs locally over the fetched rows. Nothing is rejected at parse time; the chips for
  local steps show in a different color and `EXPLAIN` gives the reason per stage. This
  replaces the WHERE-versus-HAVING lesson with something you can see.
- `where` compiles verbatim after rewrites the compiler can prove: `Space.Slug = 'prod-eu'`
  becomes `SpaceID = '<uuid>'` (catalog lookup), `Target.Slug` likewise.
- `columns` is the projection: attribute paths (`Slug`, `Space.Slug`, `Labels.Environment`,
  `Values.Image/container-image`, `Data.spec.replicas`), function calls (§4.3), or
  `cel('…')`. `Labels.*` (and `Space.Labels.*`) expands to one column per label key found
  in the rows, coarse facets first (fewest distinct values to the left) and constant keys
  hidden, so a row reads like a path through the topology and `f` on any label cell yields
  `Labels.<key> = '<value>'`. The default columns include `Labels.*` for Unit, Space and
  Target because labels are how the topology is represented (2026-09-04).
- `group by` with only attribute columns compiles to View-style banding (server order-by).
  With an aggregate (`COUNT(*)`, `COUNT(DISTINCT x)`, `MIN`, `MAX`) it is a local stage.
- `order by` and `limit` push down where the API supports them, else local.

**The SQL on-ramp.** `SELECT cols FROM Entity [IN scope] [WHERE] [HAVING] [GROUP BY]
[ORDER BY] [LIMIT]` parses to the same AST: `WHERE` must fit the server grammar and errors
with a hint otherwise, `HAVING` is a forced-local step. The tool prints everything, including
statements typed as SQL, in pipeline form, so the pipeline is what you see and learn.

### 4.2 BROWSE: paths through the data

A browse path is an ordered list of axes over a selector. Each axis is a grouping attribute
(`Labels.Environment`, `Space.Labels.Component`) or an entity hop (`Space`, `Unit`,
`Revision`). The UI renders the path as Finder-style columns or as a tree (§5.3); the language
is the same either way.

```
Unit | browse by Space.Labels.Environment, Space.Labels.Region, Space, Unit
Unit | browse by Space.Labels.Component, Space.Labels.Variant, Unit     -- component → variant → unit
Space | browse by Labels.Environment, Space
Unit | where Data.kind = 'Deployment' | browse by Target, Unit
Unit | browse by upstream                                              -- clone tree
Unit | browse by links                                                 -- link graph as a tree
```

Each column is a statement the engine actually runs: a grouping axis is
`SELECT DISTINCT <axis>, COUNT(*) FROM <entity> WHERE <chips so far>`; an entity hop is a
list call with the accumulated WHERE. Choosing a value in a column appends a chip, so walking
a path *builds a selector*, and `Enter` on the last column hands that selector to the results
grid as a `SELECT`. Space-level counts come from `GET /space?summary=true` where the axis is
a space attribute, which is the cheap-rollup-for-the-count, explicit-filter-for-the-rows
pattern from configboard. Browse paths are saved locally as bookmarks (they are not a server
entity); the selector they produce can be saved as a Filter.

### 4.3 Functions as columns

A non-mutating function call in `columns` runs `cub function get <fn> <args>` over the same
selector and joins the result on `UnitID`. Validating functions return `Passed`.

```
Unit | in * | where Space.Labels.Environment = 'prod' AND Data.kind = 'Deployment'
     | columns Slug, get-replicas() as replicas, get-image('@0') as image, vet-placeholders() as clean
     | where replicas < 2 OR NOT clean
```

Compiles to one list call plus one org-wide function invocation per distinct function (the
server fans out per unit), then a local join and the local `where` stage. Mutating functions in
`SELECT` are a compile error in phase 1. Function arguments accept the same `template:` and
`cel:` prefixes as `cub`.

### 4.4 Catalog statements

```
SHOW ENTITIES;                       -- what FROM accepts
SHOW COLUMNS FROM Unit;              -- attributes with type, operators, filterable?, join?
SHOW JOINS FROM Unit;                -- Space, Target, UpstreamUnit, FromLink.*, …
SHOW LABELS [FROM Unit|Space] [IN *];-- label keys with counts
SHOW VALUES OF Labels.Environment;   -- distinct values with counts
SHOW FUNCTIONS [GET|SET|VET] [LIKE 'get-%'];
SHOW FILTERS; SHOW VIEWS; SHOW SPACES; SHOW TARGETS; SHOW COMPONENTS;
DESCRIBE Unit.HeadRevisionNum;       -- cub unit explain HeadRevisionNum
DESCRIBE get-image;                  -- cub function explain get-image
```

### 4.5 EXPLAIN

```
EXPLAIN Unit | where Space.Slug = 'prod-eu' | columns Slug, get-replicas() as r | where r > 3
```
```
plan
  1. list Unit            cub unit list --space '*' --where "SpaceID = 'c1f2…'" --columns Unit.Slug
                          (rewrote Space.Slug = 'prod-eu' → SpaceID, pushed to SQL)
  2. function get-replicas cub function get --space '*' --where "SpaceID = 'c1f2…'" get-replicas --show output -o json
  3. join on UnitID       local
  4. where r > 3          local   (r is a column alias, not a server attribute)
```

### 4.6 Saving

```
Unit | where Space.Labels.Environment = 'prod' AND Data.kind = 'Deployment'
     | save filter ops-home/prod-deploys

ops-home/prod-deploys | columns Slug, Space.Slug, Values.Image/container-image as image, cel('…') as gates
     | order by Space.Slug
     | save view ops-home/prod-images
```

`save` is the last step: `save filter` stores the server-side where steps as a Filter,
`save view` stores the columns and order as a View, bound to the Filter it names or to a
filter slug the console asks for. A local step or an aggregate cannot be saved and the step
says so: that is gap 1 and gap 6 surfacing on purpose. `DROP VIEW …`, `DROP FILTER …`.

### 4.7 Diff

*Shipped 2026-09-04 for selections; the per-unit forms below are still to come.*

The job: see how two parts of the infrastructure differ, where the parts are above the unit
level (dev vs prod, us-east-1 vs us-east-2) and the unit is the atomic thing compared.

```
Unit | in * | where Labels.Component = 'cart'
     | diff Labels.Environment = 'dev' vs Labels.Environment = 'prod' [by Slug, Labels.Region]
```

The steps before `diff` are the common scope; the two sides are where terms and become two
server calls. The selector may sit at any level: a diff written or marked from a Space,
Target or Resource browse is lifted to the units inside it (`Labels.Variant = 'dev'` on
Spaces becomes `Space.Labels.Variant = 'dev'` on Units), since the unit is what is compared. Units are then **paired by like identity**: the unit slug, the Component label
when present, and every label dimension whose set of values is the same on both sides, so
dev/us-east pairs with prod/us-east while Cluster, which differs by construction, is left
out. An n:m pair is refined by the label that matches the most values across its sides
(Region inside dev-vs-prod), recursively; what stays n:m is shown as such with a hint to
narrow. Each pair is `same` or `differ` from DataHash alone, `only-A`/`only-B` when one side
lacks it. Data is fetched only for the pair on screen and diffed locally, unified with
context, so a thousand-pair diff costs two list calls plus two data fetches per pair viewed.

In browse, `m` marks the current selection as side A, `m` again as B, and `d` runs the diff;
terms both marks share move into the common where step. The diff view lists pairs with a
status glyph and a summary line, shows the selected pair's diff on the right, `n`/`p` jump
between differing pairs, `=` hides identical ones, Enter opens the full diff.

Still to come, same statement family:

```
Unit prod-eu/backend | diff revisions 3..5              -- cub unit diff
Unit prod-eu/backend | diff with live                   -- data vs livedata (no cub equivalent today)
ChangeSet ops-home/release-452 | diff                   -- start tag..end tag over its units
```

### 4.8 Meta commands

`\c <context>`, `\use <space|*>`, `\o table|json|yaml|csv`, `\x` (expanded rows), `\e`
(edit in `$EDITOR`), `\cub` (print the cub command for the last statement), `\save view|filter
<space>/<slug>`, `\watch [n]` (re-run the last statement every n seconds), `\h`.

## 5. The screen

One full-screen application. Regions, top to bottom:

```
┌ demo · hub.confighub.com · USE * · Results ─────────────────────────────────── 148 rows ┐
│ Environment=prod ⌫  Component=checkout ⌫                                                  │
│ NAME        SPACE     TARGET     ENV   REPLICAS  IMAGE            ▏ prod-eu/backend       │
│ backend     prod-eu   eks-eu-1   prod  3         checkout:v1.2.4  ▏ Detail Data Revs Muts │
│ backend     prod-us   eks-us-1   prod  3         checkout:v1.2.3  ▏ HeadRevisionNum   17  │
│ …                                                                ▏ LastReleased      15  │
│                                                                  ▏ Upstream  base/…@9    │
├ command ─────────────────────────────────────────────────────────┴─────── ⏎ run · ⇧⏎ nl ┤
│ SELECT Slug, Space.Slug, Target.Slug, Labels.Environment AS env,                          │
│        get-replicas() AS replicas, get-image('@0') AS image                               │
│ FROM Unit WHERE Space.Labels.Environment = 'prod' AND Space.Labels.Component = 'checkout' │
├──────────────────────────────────────────────────────────────────────────────────────────┤
│ ^/ help  ^S save view  ^B browse  ^G grid  ^O open row  ^T catalog  ^R history  ^X explain│
└──────────────────────────────────────────────────────────────────────────────────────────┘
```

- **Top bar**: context, server, scope (`USE`), main-area mode, row counts (server → local).
- **Chips row**: the current WHERE as removable chips. Chips and the command area are two
  renderings of the same clause; edit either.
- **Main area**: one of three modes, switched with ^B/^G/^O or by what you do. *Browse*
  (§5.3), *Results* (a grid), *Detail* (one row, tabbed). Results and Detail can split
  left/right; `Tab` moves focus.
- **Command area**: a multi-line editor, 3 lines by default, grows to a third of the screen.
  `Enter` runs when the statement parses complete, otherwise inserts a newline; `Shift+Enter`
  always inserts a newline; `Ctrl+Enter` always runs. Plain terminals cannot distinguish
  `Shift+Enter` or `Ctrl+Enter` from `Enter`; they work under the kitty keyboard protocol
  (Ghostty, kitty, WezTerm, recent iTerm2), and `Alt+Enter` runs everywhere. Completion popup at the cursor; parse
  errors underline in place with the hint. Whatever the UI did last is written here as a statement, so the area
  doubles as an explanation of what you just navigated to.
- **Key bar**: the global chords, in the Norton Commander position. No F-keys: on a Mac they
  collide with the OS and the touch bar, so every global action is a control chord the
  editor does not already use for readline editing. Single-letter keys act on the focused
  row (§5.4).
- **Side drawers** (toggle, overlay the main area's edge): *History* (^R) and *Catalog* (^T).

### 5.1 History

Every executed statement is appended to a local history with time, scope, row count, and a
hash of the result. `Up`/`Down` at the top or bottom line of the command area walk it, like a
shell. `^R` opens the drawer: a list, newest first, with fuzzy search (from anywhere), `Enter` to load into the command area, `Ctrl+Enter` to
run as-is, `p` to pin, `n` to name. Named entries become bookmarks and show at the top of the
Catalog. History is per user, in `~/.confighub/commander/history.jsonl`, and survives
sessions.

### 5.2 Defining views

The results grid is always a view-shaped thing: columns, an order, a filter. `^S` on a
results grid opens a small form pre-filled from the current statement: view slug, home space
(defaults to the context's space or an `ops-home` if one exists), filter slug if the statement
has a WHERE, and a preview of the two `cub` commands it will run. Saving creates the
Filter and View entities; the top bar then shows `FROM ops-home/<slug>` as the source, and
further edits to columns or chips offer `^S` again as "update view". Local-only features
(HAVING, aggregates, function columns not backed by a `Values` trigger) show as a red mark on
the form with the reason, and the form offers the nearest saveable subset. Views you saved
appear in the Catalog and as browse roots.

### 5.3 Browsing

Browse is for when you do not know what to type. Pick an axis set and move around in it.

**Choosing the path.** `^B` opens browse with a one-line path chooser at the top of the main
area, showing the current `BROWSE … BY …` statement as a row of axis chips. `+` adds an axis
from a picker (entity hops first, then label keys with counts, then any attribute), `-`
removes, drag/`<` `>` reorders. Presets on the first screen:

| Preset | Path |
|---|---|
| By space | `Space, Unit` |
| By component | `Space.Labels.Component, Space.Labels.Variant, Unit` |
| By environment | `Space.Labels.Environment, Space.Labels.Region, Space, Unit` |
| By target | `Target, Unit` |
| Clone tree | `UPSTREAM` |
| Saved views | one entry per View, opening it in Results |

**Two renderings, one path.** `c` toggles between them.

*Columns* (Finder): one vertical pane per axis, left to right. Each entry shows its value and
a count; selecting it fills the next pane to the right. The rightmost pane is the entity list,
and the pane past it is the detail preview. `→` and `←` move between panes, so the whole
thing is driven with four arrow keys.

```
┌ Browse Unit BY Environment, Region, Space, Unit ────────────────────────────────────────┐
│ Environment    ▏ Region        ▏ Space            ▏ Unit          ▏ prod-eu/backend      │
│ prod      (78) ▏ eu       (26) ▏ prod-eu     (14) ▏ backend       ▏ HeadRevisionNum 17   │
│ staging   (36) ▏ us       (26) ▏ prod-eu-dr  (12) ▏ frontend      ▏ Target   eks-eu-1    │
│ dev       (36) ▏ apac     (26) ▏                  ▏ postgres      ▏ Data  ▸ Deployment…  │
```

*Tree*: the same path nested, `→` expands a node, `←` collapses, counts on every node. The
clone tree and link graph render only here.

**Everything is a chip.** Selecting `prod` in the Environment column is exactly
`Space.Labels.Environment = 'prod'`, shown at once as a pending chip. Selections stay UI
state while browsing, so moving through a large org never refetches; `g` (or ^G) commits
them as where steps, rewrites the statement, and shows the grid. `^G` switches to Results with that selector, columns chosen from the path's axes. `^S`
saves it. Browsing is therefore a way of *writing selectors with arrow keys*.

### 5.4 Keys on a row

In Results, Browse and Detail, one-letter keys pivot on the focused row by rewriting the
statement: `s` its space, `t` its target, `u` upstream unit, `d` downstream units, `r`
revisions, `m` mutations, `l` links, `k` component (all variants), `v` live data, `y` yank the
slug, `Y` yank the row as JSON, `Enter` open Detail, `Esc` back (statement history is the
breadcrumb). `^X` shows the plan and the equivalent `cub` command.

### 5.5 The ramp: from navigating to writing

*Added 2026-09-03 after the first sessions with M2: an empty editor is the power path
presented as the entry path.* The screen has to be drivable with arrow keys before it asks
anyone to type, and every level has to show the statement the next level edits. Four
levels; each is a place people can stay.

**Level 0 — Home, not a prompt.** On start the main area shows the org at a glance: entity
counts (spaces, units, targets, links, revisions, from one `GET /space?summary=true`), a
"browse by" list (space, component, environment, target, any label key with its count),
the org's saved Views and Filters, and recent statements. Arrows and Enter. The command area
is collapsed to a one-line *readout* showing the statement behind whatever is on screen;
it is not an editor until you ask (§5.5, level 3).

**Level 1 — Browse.** A preset or a chosen path opens Finder columns with counts (§5.3).
Each selection adds a chip and the readout grows. Enter on a row opens detail; pivot keys
work as in the grid. Nothing is typed.

**Level 2 — Compose without typing.** In browse or the grid, the chips row is focusable.
Enter on a chip opens a small editor: the operators valid for that attribute's type and,
where the catalog has them, the sampled values, so `=` becomes `LIKE` or `IN (…)` with
arrow keys. A column picker (`c`) toggles attributes, label keys and join columns from a
checklist, writing the projection. `o` orders. The result is a complete statement composed
entirely with keys, and the readout has shown every step of it.

**Level 3 — Edit the statement.** `:` (or Enter on the readout) expands the command area
with the current statement pre-filled. Editing something that already works, with
completion, is how HAVING, GROUP BY and function columns get learned. `^S` saves the
result as a View. Esc collapses the editor back to the readout.

**Why the readout's syntax now matters more.** It is the teaching device. Watching
`SELECT * FROM Unit IN * WHERE …` grow as you click is clumsy; the entity that you chose
first sits third. A pipeline form reads like the navigation that produced it:

```
Unit | in * | where Space.Labels.Environment = 'prod'
     | columns Slug, Space.Slug, get-replicas() as r
     | where r < 2 | order by UpdatedAt desc | limit 20
```

Each navigation step appends a step. Step order is plan order: a `where` before `columns`
is the server stage; a `where` after a function column is the local stage, which retires
the WHERE-versus-HAVING lesson, and `EXPLAIN` lines up one-to-one with the steps.
Completion is contextual by construction. The recommendation is to make the pipeline the
canonical form the UI writes and keep `SELECT` accepted for people who arrive with SQL,
both parsing to the same AST. That is two surface syntaxes, argued against in §5.6; the
ramp changes the calculus because one of them is written by the tool, not learned. Open
until decided; it is the one decision that shapes M3.

### 5.6 How SQL-like the typed form is

*Decided 2026-09-03: the pipeline form (§4.1) is canonical and is what the tool writes; SQL
SELECT is accepted and printed back as a pipeline.* The earlier reasoning, kept for the record: The command area must accept the dialect in
§4 because that is what the UI writes. It should *also* accept a `cub` list command verbatim
(`cub unit list --space '*' --where "…" --columns …`, `cub` optional) and translate it into the
same plan, so someone who already knows `cub` has a ramp and can see the translation. Terse
forms (`unit where Labels.env='prod'`) are tempting but a third syntax is a mistake; if the
SQL keywords feel heavy in practice, the fix is to let `SELECT` and `FROM` be optional
(`Unit WHERE …` means `SELECT * FROM Unit WHERE …`), which keeps one grammar.

## 6. Catalog and completion

Sources, in order of authority:

| Source | Gives | How |
|---|---|---|
| OpenAPI spec bundled in the SDK (`openapi.LookupSchema`) | attributes, types, enums, descriptions per entity | offline |
| Hand-maintained join table (from `EntityExpanders`) | joinable prefixes per entity, which are list-valued | checked-in YAML, tested against the server by probing |
| Server error messages | validation of the above | a bad term returns a precise error; the catalog probes once per session to confirm its tables and marks drift |
| Live org sampling | label keys and values with counts, `Values` keys, `ApplyGates` keys, slugs of spaces/targets/units/filters/views | list calls with `select=Labels` etc., cached per session, refreshed on `\refresh` |
| `~/.confighub/functions.json` and `function list` | function names, kinds, parameter signatures, attribute names | cached by cub already |
| Saved Filters and Views | sources for `FROM`, and their columns | list calls |

Completion is context-aware: after `FROM` offer entities and `space/view`; after `SELECT` or
`WHERE` offer attributes of the entity plus joins; after `Labels.` offer keys with counts;
after `Labels.Environment =` offer quoted values; after an attribute offer only operators valid
for its type (a UUID gets `= != IS NULL IS NOT NULL`); after a list-valued join require `*.`;
inside a function call offer its parameters. The status line shows the type and description
of the attribute under the cursor. The same parser validates as you type.

Gap 3 is the constraint: the join and computed-attribute tables are server-only. Phase 1
ships them as a checked-in table with a probe test. The product ask is a
`GET /api/schema/queryable` endpoint; when it exists the table is replaced by a fetch.

## 7. Architecture

Go, `github.com/confighub/cub-commander`, public, MIT. A cub plugin following the
`cub-helm` conventions:
`plugin.HandleHook` writes `cub-plugin.yaml`; auth and server come from `CUB_SERVER`,
`CUB_TOKEN`, `CUB_SPACE`, `CUB_CONTEXT`; install is `cub plugin install ./bin/cub-commander`
via `make plugin`. The manifest declares one command, `commander`, with alias `cmdr`.

```
cmd/                 cobra: commander (the TUI); hidden -e / explain for tests and scripts
internal/catalog/    schema (OpenAPI + joins.yaml), live sampling, cache, completion index
internal/lang/       lexer, parser, AST, semantic check, compiler → plan, explain renderer
internal/plan/       plan nodes: List, Function, Join, Having, Aggregate, Diff, Save
internal/exec/       runs a plan against the API; local stages; row model
internal/format/     table (reuse cub's tablewriter look), json, yaml, csv
internal/history/    jsonl history, bookmarks, fuzzy search
internal/tui/        bubbletea app: command area, results grid, browse (columns + tree), detail tabs, chips, drawers, keymap
internal/cubclient/  goclient-new + cubapi, retrying transport (copied from cub-demo)
```

Dependencies: `charmbracelet/bubbletea`, `bubbles`, `lipgloss` (none exist in the workspace
yet; this is the first TUI), `sergi/go-diff`, `itchyny/gojq`, the SDK's `core/function/api`
for literal and operator rules so the client parser cannot drift from the server's. The
parser is a hand-written recursive descent; the grammar is small.

Testing: golden tests `statement → cub command(s)` in the style of configboard's
`compile.test.ts`; parser fuzz against the server's own `filter_parser_test.go` corpus; a
catalog probe test that runs against a real hub and fails when the join table drifts.

## 8. Phase 1 scope (read)

In: the TUI (command area, history, results, detail, browse in columns and tree form,
chips, pivots, catalog, save-as-view); SELECT and BROWSE over every entity with `--where` in
the CLI (unit, space, target, link, revision, mutation, changeset, tag, trigger, invocation,
filter, view, worker, unit-event, unit-action, user); saved Views and Filters as sources and
as save targets; function columns for get and vet functions; HAVING and local aggregates;
SHOW/DESCRIBE/EXPLAIN; DIFF in all five forms.

Out: anything that mutates ConfigHub state other than `CREATE`/`DROP` of Filters and Views.

See `roadmap.md` for milestones.

## 9. Phase 2 sketch (write)

The read design is shaped so write drops in without a new language:

- `UPDATE Unit SET Labels.reviewed = 'true' WHERE …` → `cub unit update --patch --label …
  --where …`. `DELETE FROM Unit WHERE …`. Every statement runs as a dry run first and shows
  the affected rows in the grid; `\go` executes.
- `CALL set-image('nginx', 'nginx:1.25') WHERE …` → `cub function set --dry-run -o mutations`
  rendered as a per-unit, per-path diff in the right pane, **live as you edit the WHERE**
  (debounced). That is the "iterate on the selector and watch what-if results" loop.
- `BEGIN CHANGESET ops-home/release-453; … ; COMMIT;` maps to changeset create, attach, and
  close; `ROLLBACK` maps to `--restore Before:ChangeSet:…`. A ChangeSet is a transaction.
- `UPGRADE Unit WHERE UpstreamRevisionNum < UpstreamUnit.HeadRevisionNum` → `--upgrade`.
- Merge previews with `--merge-source/--merge-base/--merge-end` as `MERGE … PREVIEW`.

Open write questions: how to show a bulk dry run across 100 units without drowning; whether
`HAVING`-selected rows can be mutated (the server can't select them, so it would be
`--unit a,b,c` under the hood, which `EXPLAIN` should say).

## 10. Name

"commander" fits better than it first looks: Norton Commander is two panes, a command line
at the bottom that is always live, and F-keys for verbs. That is exactly this layout, with
the file system replaced by the query result. Recommendation: keep it. Plugin and repo
`cub-commander`, command `cub commander`, alias `cub cmdr`.

Alternatives considered: `console` (generic), `lab` (says what it is for, not what it is),
`mc` (Midnight Commander homage, too cute), `cubby` (taken by cubbychat).

## 11. Open questions

1. Queryable-attribute endpoint (gap 3): ask for it now, or ship the checked-in table first
   and let the drift test make the case?
2. Should `HAVING`-only queries be saveable as a *commander* artifact (a local `.cql` file)
   even though they can't be a Filter? Probably yes, in `~/.confighub/commander/`.
3. Page size and budgets: list calls have no pagination; configboard used 5k soft / 25k hard
   limits. Same here, with the hint naming the clause to add.
4. Live sampling cost on a large org (cub-demo's ~100-cluster dataset): sample per space
   lazily, or one org-wide `select=Labels` call at startup?
5. Public or private repo. Lab means churn; start private, publish when the language settles.
