# cub commander — rollouts

*Design, 2026-09-05. The steel thread is chapter 1 of the change-workflows walkthrough
(cub-demo, branch `docs/change-workflows-demo`, `docs/change-workflows-walkthrough.md`): CI
opens a change order; someone sees a rollout in flight; reviews the change; previews and
promotes stage by stage, releasing where the next gate asks for it, until the workflow's
final check holds. Commander covers everything after the change order exists.*

Vocabulary and semantics come from `confighub/docs/design/change-workflows.md` (v4, final)
and the CLI. Commander does not invent rollout semantics: where the spec leaves something
open, this page says so and the roadmap marks it gated.

## 1. What the platform gives us

**A rollout is a ChangeOrder plus the ChangeWorkflow it was created under.** Nothing stores
a stage. Everything commander shows is derived on read, exactly as `cub changeorder get`
and the Web UI derive it:

| Fact | Where it comes from |
|---|---|
| The rollouts in flight | `GET /change_order` org-wide; `State` in `New, InProgress, Resolved` is moving, `Released` is done by state, `Aborted/Restored/RestoreReleased` are the undo family. |
| The workflow governing one | annotations `confighub.com/change-workflow-unit-id` and `…-revision-num` on the ChangeOrder; the pinned revision's data (`/space/{s}/unit/{u}/revision/{r}/data`) parses as `changeworkflow.ChangeWorkflow` (SDK `core/changeworkflow`). |
| A stage's member spaces | `GET /space?where=<stage.whereSpace> AND Labels.Component = '<component>'`, then filtered to the ChangeOrder's `InScopeSpaceIDs`. The component is the base space's `Component` label. |
| Promoted / released per space | `ResolvedSpaceIDs` / `ReleasedSpaceIDs` on the ChangeOrder (server-derived). |
| Healthy per space | the space's `confighub.com/live-status` annotation: `syncStatus=Synced`, `operationPhase=Succeeded`, `healthStatus=Healthy`. A space with no `ReleaseTargetID` is never healthy and never released. |
| The next stage | the first stage whose members are not all in `ResolvedSpaceIDs` (the base's own stage passes trivially, since the base is resolved from birth). |
| The gates on it | the next stage's `prerequisites`, evaluated over every member of the previous stage: taken (always), `released`, `healthy`. |
| Completed | the last stage's members satisfy `final.prerequisites`. Different reading from `State`; both are shown. |
| The change itself | Revisions carrying the ChangeOrder's start and end Tags: `GET /revision?where=SpaceID = '<space>' AND Tags ? '<StartTagID>'` and the same for `EndTagID`; bodies in one call from `GET /revision_data?where=RevisionID IN (…)`. A unit whose start and end land on the same revision is *untouched*. |
| What a promotion would do | `PATCH /unit?where=SpaceID IN (…) AND UpstreamUnitID IS NOT NULL&upgrade=true&change_order=<id>&dry_run=true&include=ConfigData`, body `null`. Per-unit multi-status; `ConfigData` is the would-be result, diffed locally against the unit's current data. |
| Promote | the same call without `dry_run`. |
| Release | `POST /space/{space}/release` with `TagID = EndTagID`, which is `cub release publish --revision ChangeOrder:<slug>`. Blocked while any bundled unit carries an ApplyGate, including the transient `awaiting/triggers` a promotion leaves behind. |

**Gates are checked by the client, not the server** (spec, "The promote operation"; Q12).
The dry run above does not know about stages; commander has to evaluate prerequisites itself,
in the CLI's order and wording, before it offers a promote. That is the one piece of logic
worth getting byte-for-byte right, and it is pinned by tests against the CLI's messages.

Two things the CLI does that the first cut does not: clone units the target lacks (with
their links) before upgrading, and `--squash`. See §5.

## 2. The surface

Rollouts are one more thing to browse, so they start where everything else does.

**Home / chooser** gains a preset, *Rollouts in flight*, which is the statement

```
ChangeOrder | in * | where State IN ('New', 'InProgress', 'Resolved')
            | columns Slug, Space.Slug, state(), stage(), next(), blocker(), CreatedAt
            | order by CreatedAt desc
```

The `state()` column uses the Web UI's vocabulary so the two surfaces agree: *Ready to
Promote*, *Degraded* (a healthy gate failing), *Unreleased changes* (a released gate failing),
*Progressing* (a stage partly taken), *Complete*, *Aborted*, *No ChangeWorkflow*. Complete
and Aborted are hidden by the preset's where step, as the Rollouts page hides them by default;
the count of each state sits above the grid the way the page's exception strip does, and a
component row elsewhere in commander shows an *outstanding rollout* hint the way the component
overview flags it.

`state()`, `stage()`, `next()` and `blocker()` are local computed columns (yellow chips, per-stage
reasons in EXPLAIN, like function columns): resolved once per distinct workflow and once per
(workflow, component) for stage membership, cached for the session and refreshed with the
statement. A ChangeOrder with no workflow shows them blank, which is what the CLI does. The
home screen shows the in-flight count next to the preset, so a new rollout is visible on the
first screen; the status line repeats it after every refresh.

**Enter on a ChangeOrder row opens rollout mode** (`modeRollout`), a new mode alongside
results, detail, browse and diff. Its readout is

```
ChangeOrder catalog-api-base/catalog-api-5-3-0 | rollout [stage test]
```

so history, EXPLAIN and the statement editor keep working; Esc returns to the list.
Metadata stays reachable as a tab, as in detail.

The screen:

```
catalog-api-5-3-0  catalog-api 5.3.0                    InProgress · not completed
workflow catalog-api-workflow @rev 3 · component catalog-api · 6 of 7 spaces to go

 source ─ bases ─ dev ─ test ─ prod ─ final
   ●       ○       ○      ○      ○      ·
 base    0/3     0/1    0/2    0/3
                 ▲ next: gates 1/1 · promote is open

┌ stage: bases ──────────────────┐ ┌ what this promotes here ───────────────────┐
│ catalog-api-dev    not taken   │ │ api  (catalog-api-dev)                      │
│ catalog-api-test   not taken   │ │ -        image: catalog-api:5.2.0           │
│ catalog-api-prod   not taken   │ │ +        image: catalog-api:5.3.0           │
│                                │ │ -          memory: 512Mi                    │
│ gates on bases: 1/1 satisfied  │ │ +          memory: 1Gi                      │
│  ✓ base has the change         │ │ namespace, config, catalog-db: no change    │
└────────────────────────────────┘ └────────────────────────────────────────────┘
 ←/→ stage   ↑/↓ space   ⏎ diff   P promote   L release   B promote+release   ^X cub
```

- **Stage strip** across the top: source, each stage, and *final*. Under each, promoted/
  released/healthy counts as `taken 2/2 · released 2/2 · healthy 0/2`, collapsed to one
  glyph when narrow. The next stage is marked and its gate tally shown in the CLI's words.
- **Left pane**: the selected stage's spaces with their three bits; the gates on this stage
  listed with ✓/✗ and the CLI's refusal text on the failing one (`live-status not found for
  Variant 'us-east-test2'`). On *source* the pane lists the base's units with touched /
  untouched and the skipped units with reasons.
- **Right pane**: the diff for the selection.
  - On *source*: the ordered change, per unit, start-tag revision → end-tag revision.
  - On a stage whose selected space has **not** taken the change: the dry-run preview,
    per unit, current → would-be. A unit the dry run reports an error for shows the error.
  - On a space that **has** taken it: what happened there, start-tag revision → end-tag
    revision in that space (same query, different SpaceID).
  - `⏎` opens the full unified diff in the diff viewer (`n`/`p`/`=` as elsewhere).
- Keys stay consistent with the rest of commander: `m`/`d` still mark and diff, `s` pivots
  to the space, `u` to its units, `Esc` back one level, `^X` shows the plan and the exact
  `cub` commands the actions would run.

The mode refreshes itself every 10 s while open (gates are read live, and argobot's report
is the thing people wait for in step 7); `R` refreshes now.

## 3. The actions

Three writes, all stage-scoped like the CLI's bulk mode, all through the same guardrail:

1. Show what will run, as `cub` commands, with the per-space list and the current gate
   tally. Gates failing → the action is not offered; the key explains why in the CLI's words
   instead.
2. Confirm (`y`).
3. Run per space, in order, reporting per-space outcome; a failure does not stop the
   ones after it, the way the CLI lands a stage partway; re-running is safe because a
   promotion passes over units already carrying the end tag.
4. Reload the ChangeOrder and redraw. Never patch local state.

| Key | Does | cub equivalent |
|---|---|---|
| `P` promote stage | dry run the stage first and refuse if anything comes back that the upgrade cannot do (see §5), then `PATCH /unit … upgrade=true&change_order=…` per space, skipping the base | `cub variant promote --change-order <space>/<slug> --target-stage <stage>` |
| `L` release stage | wait for `awaiting/triggers` to clear on the stage's units (bounded), then `POST /space/{s}/release {TagID: EndTagID}` per space that has a release target; spaces without one are listed as *not releasable* and count as released, per the state machine | `cub release publish --revision ChangeOrder:<space>/<slug> <space>` per space |
| `B` promote and release | `P` then `L` on the same stage; the UI's "Promote and release" | the two above |

Not in the steel thread, shown but not driven: abort (`AbortedReason`), demote/restore,
`cub variant approve`. The mode displays an aborted rollout as such and offers nothing.

No `--change-desc` is ever sent: the promoted revisions keep the pipeline's description,
which is the audit trail the walkthrough closes on.

## 4. How it fits the code

- `internal/rollout` (new): pure derivation over fetched rows. `Load(ctx, client, changeOrder)`
  → `Rollout{Order, Workflow, Component, Stages[]{Name, Prereqs, Spaces[]{Space, Taken,
  Released, Healthy, Releasable}}, Next, Gates[], Completed}`; `Gates()` mirrors
  `validateStageEntryGates` and `checkVariantPrerequisites` including messages; `Change()`
  builds the tag-pair diff for a space; `Preview()` runs the dry run and pairs `ConfigData`
  with current data; `Promote()`, `Release()` are the writes. All take injected fetchers
  so the model test can run offline with stubs, as every other loader does.
- `internal/plan`: `rollout [stage <name>]` as a terminal step on a `ChangeOrder` statement;
  `stage()/next()/blocker()` as local computed columns; `CubCommand` prints the promote and
  publish lines for the actions so `^X` is honest. Golden tests as usual.
- `internal/tui/rollout.go`: `rolloutState`, `rolloutKey`, `rolloutView`; registered in the
  key-routing order after the global chords, like the other modes. Esc goes back to the
  ChangeOrder list. Writes go through a confirm overlay checked before the global switch,
  like the popup and the picker.
- No new dependency. Caches for workflows and stage membership live on `catalog.Live`
  next to the label sample and are invalidated by the sampler.

## 5. Gaps, and what commander does about each

- **Missing units.** The CLI clones units the target lacks (at the start tag, with links)
  before upgrading. The first cut does not. Before a promote, commander compares the units of
  the space's *upstream* (its `UpstreamSpaceID` annotation, the class base for a deployment)
  that carry the start tag against the space's `UpstreamUnitID`s; if any are missing it
  refuses with the `cub variant promote` line to run instead. Comparing against the root base
  was the first attempt and flagged every deployment; the tree is hop by hop. The walkthrough
  never hits this. Cloning is a later milestone, not a shortcut.
- **Semantic diffs.** A function that rewrites a unit re-serializes the YAML, so a text diff
  of one image bump showed 54 changed lines. Both the tag diff and the preview compare parsed
  documents (paired by apiVersion/kind/name, list items by `name`), list the changed fields,
  and diff a canonical re-encoding; `w` shows the raw text.
- **Kept (protected) fields.** The dry-run diff shows what changes, so a protected value
  the merge kept is simply absent, as in the CLI's `-o mutations`; only the Web UI draws it
  as kept. Step 6 of the walkthrough is told from the base's diff (memory 512Mi → 1Gi) next
  to test's (image only), which is enough to make the point. `include=MutationSources` may
  let commander annotate kept paths later.
- **The base's own diff (F20).** The UI does not draw it; commander does, from the tags,
  which is the honest source (the start tag is where the variants last took from).
- **Release pinned to the change order.** The UI publishes the head; commander pins
  `TagID = EndTagID` like the CLI, so a release describes the change.
- **Final / completed (F12, F21).** Commander draws *final* as a node and reads completed
  from `final.prerequisites`, matching `cub changeorder get`.
- **Healthy gate on a stale observation (F2).** Commander shows the observation's time next
  to the healthy bit so the reader can see it predates the release; it does not second-guess
  the gate.
- **No OR in where.** Start- and end-tag revisions are two list calls, not one.
- **One row per unit by default.** `/revision` and `/revision_data` keep the newest row per
  unit unless `distinct_on=Off` (which needs a `limit`). The tag queries want exactly that; the
  body fetch of a before/after pair does not, and passes Off. Found live 2026-09-05.
- **Naming (Q25).** "Rollout" is the working word here, in the UI text and the `rollout`
  step. Commander is a lab; if the product settles on another word the step is renamed.
- **Version skew.** Client-side derivation means commander must agree with the server's
  workflow format (v4, no `Labels.Component` in `whereSpace`). A definition that names the
  component is reported as the CLI reports it, not silently conjoined.

## 6. Milestones

| # | Milestone | Demo |
|---|---|---|
| R1 | Read-only rollout mode | **Shipped 2026-09-05.** `Rollouts in flight` preset with state/stage/next/blocker columns; rollout mode with the strip, per-space bits, gates in CLI wording, the source diff from tags and the per-space "what happened" diff; `-e "… \| rollout"` prints the reading as text; offline model test on the chapter-1 fixture (`rollout.ChapterOne`); verified live on the Demo org against `cub changeorder list`. |
| R2 | Preview | **Shipped 2026-09-05.** A stage not yet taken shows the server's dry run per space: fields each unit would change (semantic, layout-insensitive) and the canonical diff against current data; per-unit errors; a space missing units its upstream carries is a blocker naming them. |
| R3 | Promote and release | **Shipped 2026-09-05.** `P`: refused with the reason unless the stage is next, the gates are open and the preview has no blockers; the overlay lists spaces, unit and field counts, the PATCH requests and the cub line; `y` runs per space, the reading refreshes, the report opens in the text view. `L`: publishes each space of the stage that has taken the change and has a release target, pinned to the end tag, after polling the `awaiting/triggers` gate off its units (90 s cap); class bases and already-released spaces are skipped and say so. `B`: both, one confirm, the release reading the promote's outcomes as taken. **Live on the Demo org:** catalog-api-5-4-0 promoted to bases and dev and released from dev through commander; the server's revision trail carries the pipeline's description, the change order and its tags; `cub variant promote --dry-run` agrees on the next step. |
| R4 | Polish | 10 s auto-refresh, home badge, observation time on healthy, abort shown, revision picker `d` inside a space's pane. |
| R5 | Gated | cloning missing units; kept-field annotation from MutationSources; chapter 2 (refused rollout, abort, demote); anything the spec's Q12 moves server-side. |

## 7. Open with Jesper

1. ~~Confirm shape.~~ Settled 2026-09-05: `P` shows the cub commands and the space list and
   waits for `y`.
2. ~~Three keys or one.~~ Settled 2026-09-05: three (`P`, `L`, `B`).
3. ~~Where a new rollout is noticed.~~ Settled 2026-09-05: do what the UI does (above).
4. **The demo org.** Live work needs `cub auth login` on context `demo`, and a run of chapter
   1 consumes catalog-api until `cub demo reset`. That org is shared with the Web UI work and
   the change-workflows-demo session, so resets are coordinated, never assumed.
