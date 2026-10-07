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

*Rewritten 2026-10-06 for API 0.8 (SDK core v0.8.7). The first cut (2026-09-05) derived
stages and gates on the client, as the spec's Q12 then required; the server owns them now
(`confighub/docs/design/promote-api.md`), and commander reads what it says.*

**A rollout is a ChangeOrder plus the ChangeWorkflow copy it carries.** The ChangeOrder
records the stage it has reached and the promotions and releases that got it there; the one
thing still read on the client is the picture around that: which spaces each stage holds and
what each has done.

| Fact | Where it comes from |
|---|---|
| The rollouts in flight | `GET /change_order` org-wide; `State` in `New, InProgress, Resolved` is moving, `Released` is done by state, `Aborted/Restored/RestoreReleased` are the undo family. |
| The workflow governing one | `ChangeOrder.ChangeWorkflow`, a `ChangeWorkflowSpec` copied onto the order when it was created (stages with `WhereSpace`, `Prerequisites`, `ReleasePrerequisites`; `Final`; custom and attestation prerequisites). `ChangeWorkflowID` names the entity, read once for its slug. |
| The stage reached | `ChangeOrder.Stage`, set by the server on every promotion and release publish; `Completed` once the last stage satisfies `Final`. |
| A stage's member spaces | `GET /space?where=<stage.WhereSpace> AND SpaceID IN (<InScopeSpaceIDs>)`: the selector intersected with the order's scope, exactly as the server computes membership. Nothing else is implied, the component included. |
| Promoted / released per space | `ResolvedSpaceIDs` / `ReleasedSpaceIDs` on the ChangeOrder (server-derived); `Promotions` and `Releases` say who, when and which release. |
| Healthy per space | the `LiveStatus` of the space's latest published Release (`GET /release?where=Published = true AND SpaceID IN (…)`, highest `ReleaseNum` per space): `Sync=Synced`, `Health=Healthy`, no `Operation` running or failed. A space with no `ReleaseTargetID` is never healthy and never released. |
| The next stage and its gates | a dry run, `POST /promote {ChangeOrderID, DryRun: true}`: the response names the stage it would enter and every `(prerequisite, space)` pair of that stage's entry gates evaluated over the stage before it, with the CLI's wording when one fails. A refusal is a 409 carrying the same body. `Complete` says every stage already has the change. |
| Completed | `Stage == Completed` from the server. Between writes the server does not re-evaluate `Final`, so commander also reads `Released`/`Healthy` over the last stage from the bits above and shows the final tally live; a custom or attestation prerequisite is shown as not evaluated here. |
| The change itself | `GET /unit_diff?where=SpaceID = '<space>'&from=Before:ChangeOrder:<id>&to=ChangeOrder:<id>`: for every unit the order covers there, the revision before it against the one it arrived at, path by path with both values, matched by merge key. A unit with both at the same revision is *untouched*; a unit with no revision on either side is not covered. |
| What a promotion would do | the same dry run with `include=Diff` and `TargetStage`: per space the action (`Promote`, `Unchanged`, `Skipped`, `Blocked`, `Failed`), per unit the action (`Upgrade`, `Clone`, `Mark`, `Empty`, `Revive`, `Invoke`, `Resolve`, `Unchanged`, `Skip`), its diff, and the paths the merge withholds (`Conflicts`); per link what is copied, adopted, skipped or orphaned; and the `Plan` digest. |
| Promote | `POST /promote {ChangeOrderID, TargetStage, ExpectedPlan}`: one request the server applies space by space in upstream order, continuing past a failure; 409 if the gates no longer hold, 412 if the plan changed since the dry run. |
| Release | `POST /space/{space}/release {TagID: EndTagID, ChangeOrderID}`, which is `cub release publish --revision ChangeOrder:<slug>`: pinned to the end tag and recorded on the order, so `Releases` and `Stage` advance in the same transaction. Blocked while any bundled unit carries an ApplyGate, including the transient `awaiting/triggers` a promotion leaves behind, and by the stage's `ReleasePrerequisites`. |

**Gates are checked by the server, for every client.** Commander never decides whether a
promote is allowed; it asks, shows the answer per pair, and sends the dry run's `Plan` back so
that what was shown is what runs.

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
  listed as the server evaluated them: one ✓ line per prerequisite naming the spaces it holds
  for, one ✗ line per failing pair with the server's reason (`Variant 'us-east-test2' is not
  healthy`). On *source* the pane lists the base's units with touched / untouched and the
  skipped units with reasons.
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

The mode re-reads the rollout every 10 s while open (gates are read live, and argobot's report
is the thing people wait for in step 7), keeping the dry runs and diffs already loaded: those
are slow, and re-running them redrew the pane every period (Jesper, 2026-09-09). `R` re-reads
everything, the dry run included; so does the refresh after a write.

## 3. The actions

Three writes, all stage-scoped like the CLI's bulk mode, all through the same guardrail:

1. Show what will run, as `cub` commands, with the per-space list and the current gate
   tally. Gates failing → the action is not offered; the key explains why in the CLI's words
   instead.
2. Confirm (`y`).
3. Run: one promote request the server applies per space in upstream order, reporting the
   per-space outcome; a failure does not stop the spaces after it, and re-running is safe
   because a promotion passes over units already carrying the end tag.
4. Reload the ChangeOrder and redraw. Never patch local state.

| Key | Does | cub equivalent |
|---|---|---|
| `P` promote stage | refuse with the server's reason unless the stage is next and the dry run showed no blocker, then `POST /promote` once for the stage with the dry run's `Plan` as `ExpectedPlan`; the server skips the base, clones what a space lacks, and refuses if anything changed since | `cub variant promote --change-order <space>/<slug> --target-stage <stage> --expected-plan <plan>` |
| `L` release stage | wait for `awaiting/triggers` to clear on the stage's units (bounded), then `POST /space/{s}/release {TagID: EndTagID, ChangeOrderID}` per space that has a release target; spaces without one are listed as *not releasable* and count as released, per the state machine | `cub release publish --revision ChangeOrder:<space>/<slug> <space>` per space |
| `B` promote and release | `P` then `L` on the same stage; the UI's "Promote and release" | the two above |

Not in the steel thread, shown but not driven: abort (`AbortedReason`), demote/restore,
`cub variant approve`. The mode displays an aborted rollout as such and offers nothing.

No `--change-desc` is ever sent: the promoted revisions keep the pipeline's description,
which is the audit trail the walkthrough closes on.

## 4. How it fits the code

- `internal/rollout`: the reading over the server's answers. `Load(ctx, client, cache, row)`
  → `Rollout{Order (with the Workflow copy, Promotions, Releases), Stages[]{Name, Prereqs,
  Spaces[]{Space, Taken, Released, Health}}, Next, Gates[], Completed, Plan}`; `derive` only
  picks the console state from the gates and reads the final tally from the bits; `ChangeIn`
  is the `unit_diff` per space with the kept fields; `PreviewStage` is the dry run with diffs;
  `PromoteStage`, `ReleaseStage` are the writes. All take the `Client` interface (`List`,
  `GetRaw`, `Send`) so the model test runs offline on `MemClient`, whose `promote` stands in
  for the server over the same rows. `live_test.go` runs the read-only half against a real
  order when `COMMANDER_ROLLOUT_LIVE_ORDER` is set.
- `internal/plan`: `rollout [stage <name>]` as a terminal step on a `ChangeOrder` statement;
  `stage()/next()/blocker()` as local computed columns; `CubCommand` prints the promote and
  publish lines for the actions so `^X` is honest. Golden tests as usual.
- `internal/tui/rollout.go`: `rolloutState`, `rolloutKey`, `rolloutView`; registered in the
  key-routing order after the global chords, like the other modes. Esc goes back to the
  ChangeOrder list. Writes go through a confirm overlay checked before the global switch,
  like the popup and the picker.
- No new dependency. Stage membership, release status and the gate dry run are cached per
  statement (`rollout.Cache`); workflow slugs for the process.

## 5. Gaps, and what commander does about each

- **Missing units.** The server clones units a space lacks, at the change order's start, and
  the preview lists them as *would add from upstream*. Nothing to refuse any more.
- **Semantic diffs.** The server's `ConfigDiff` matches resources and merge-keyed array
  elements and carries both values per path, with a unified patch for multi-line strings.
  Commander renders that and nothing else; the raw-text mode (`w`) of the first cut is gone
  with the local YAML diff it toggled.
- **Kept (protected) fields.** A path the merge withholds comes back in the unit's
  `Conflicts` with the server's reason; a path the merge treats as a local override is found
  by comparing the ordered change (the base's `unit_diff`) with the dry run's diff and the
  unit's current data, up the UpgradeUnit lineage to the base unit, with protection read from
  `MutationSources` when the server did not say. Shown loudly as *NOT changed*.
- **The base's own diff (F20).** `unit_diff` on the base from `Before:ChangeOrder` to
  `ChangeOrder`; the honest source.
- **Release pinned to the change order.** `TagID = EndTagID` and `ChangeOrderID` on the
  publish, like the CLI, so a release describes the change and the order records it.
- **Final / completed (F12, F21).** `Stage == Completed` is the server's word; the final
  tally in the strip is commander's live reading of the last stage's bits, since the server
  evaluates `Final` only on a write.
- **Healthy gate on a stale observation (F2).** The observation's time and release number
  sit next to the healthy bit so the reader can see it predates the release; the strip's
  *healthy* count and the ✓ only count a space that has **released** the change (Jesper,
  2026-09-05). Whether the observation is of the released manifest is the server's gate to
  judge, not commander's.
- **Gates on a stage past the next.** Naming a later stage in the dry run is refused by the
  gates of the stage in between; the preview says so rather than guessing what the server
  would plan.
- **Permissions.** The gate dry run needs `Use` on the ChangeOrder; a reader without it sees
  the strip and the bits with *Not reported* and the server's message instead of gates.
- **Naming (Q25).** "Rollout" is the working word here, in the UI text and the `rollout`
  step. Commander is a lab; if the product settles on another word the step is renamed.

## 6. Milestones

| # | Milestone | Demo |
|---|---|---|
| R1 | Read-only rollout mode | **Shipped 2026-09-05.** `Rollouts in flight` preset with state/stage/next/blocker columns; rollout mode with the strip, per-space bits, gates in CLI wording, the source diff from tags and the per-space "what happened" diff; `-e "… \| rollout"` prints the reading as text; offline model test on the chapter-1 fixture (`rollout.ChapterOne`); verified live on the Demo org against `cub changeorder list`. |
| R2 | Preview | **Shipped 2026-09-05.** A stage not yet taken shows the server's dry run per space: fields each unit would change (semantic, layout-insensitive) and the canonical diff against current data; per-unit errors; a space missing units its upstream carries is a blocker naming them. |
| R3 | Promote and release | **Shipped 2026-09-05.** `P`: refused with the reason unless the stage is next, the gates are open and the preview has no blockers; the overlay lists spaces, unit and field counts, the PATCH requests and the cub line; `y` runs per space, the reading refreshes, the report opens in the text view. `L`: publishes each space of the stage that has taken the change and has a release target, pinned to the end tag, after polling the `awaiting/triggers` gate off its units (90 s cap); class bases and already-released spaces are skipped and say so. `B`: both, one confirm, the release reading the promote's outcomes as taken. **Live on the Demo org:** catalog-api-5-4-0 promoted to bases and dev and released from dev through commander; the server's revision trail carries the pipeline's description, the change order and its tags; `cub variant promote --dry-run` agrees on the next step. |
| R4 | Polish | 10 s auto-refresh, home badge, observation time on healthy, abort shown, revision picker `d` inside a space's pane. |
| R5 | API 0.8 | **Shipped 2026-10-06 (unreleased).** Stages, gates and the plan from `POST /promote` dry runs; the change from `unit_diff`; health from `Release.LiveStatus`; cloning by the server; kept fields from `Conflicts`; one promote request with `ExpectedPlan`; releases recorded on the order. Verified read-only against the harbor-financial org (`live_test.go`); a live promote through commander waits for a change order in flight on the demo org. |
| R6 | Next | chapter 2 (refused rollout, abort, `/demote`); attestations as gates (`ReleasePrerequisites`, `cub attestation create` from the rollout); `PromotionFailures` and overrides drawn in the mode; `Validated` gate surfaced per unit. |

## 7. Open with Jesper

1. ~~Confirm shape.~~ Settled 2026-09-05: `P` shows the cub commands and the space list and
   waits for `y`.
2. ~~Three keys or one.~~ Settled 2026-09-05: three (`P`, `L`, `B`).
3. ~~Where a new rollout is noticed.~~ Settled 2026-09-05: do what the UI does (above).
4. **The demo org.** Live work needs `cub auth login` on context `demo`, and a run of chapter
   1 consumes catalog-api until `cub demo reset`. That org is shared with the Web UI work and
   the change-workflows-demo session, so resets are coordinated, never assumed.
