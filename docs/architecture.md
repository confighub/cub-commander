# cub commander — architecture notes

*Handoff notes, 2026-09-05. The design is in `design.md`, the milestones in `roadmap.md`;
this is the page that explains how the code is put together and the conventions that are
not written anywhere else. Read this before adding a mode.*

## Packages

| Package | Role |
|---|---|
| `internal/lang` | Lexer, AST, parser (pipeline form + SQL on-ramp), `StmtString` printer, where serializer. Identifiers may contain `-`, `/`, `.`; a pipe glued after a dot stays inside a data path. |
| `internal/catalog` | Static entity table (REST paths, joins, include IDs, default columns), attributes from the SDK's embedded OpenAPI (`schema.go`), the generated filterable table (`hack/gen-filterable.sh`), and the live org sample (`live.go`: label keys/values, spaces). |
| `internal/plan` | `Compile(stmt, session)` → `Plan`: one list stage (server) + ordered local stages, `Pushed[]` per where step, `Browse` axes, `Diff` (two list stages). `liftToUnit` rewrites a Space/Target/Resource diff to units. `Explain`/`CubCommand`/`APIPath` are the honesty contract, pinned by golden tests. |
| `internal/exec` | Runs plans: generic REST rows (`cubclient.Row` = `map[string]any` keyed by entity name, joins as sibling keys), local where/group/order/limit, `Labels.*` expansion (`expandColumns`), diff pairing (`PairRows`, `refine`), unified diff, unit data read/write (`UnitDataWithHash`, `SaveUnitData`), YAML stream splitting (`docs.go`). |
| `internal/cubclient` | Thin HTTP client: `List`, `GetRaw`, `GetRawETag`, `PutRaw` (If-Match, 409 → `ConflictError`), `SpaceID` cache, retrying transport. |
| `internal/history` | jsonl statement history under `~/.confighub/commander/`. |
| `internal/tui` | The Bubble Tea v2 app. See below. |
| `cmd` | cobra root: `commander` opens the TUI; hidden `-e` runs statements for scripts and tests. |

Modules are `charm.land/bubbletea/v2`, `charm.land/bubbles/v2`, `charm.land/lipgloss/v2` (the
`github.com/charmbracelet/*` v2 paths are stale mirrors and conflict).

## The TUI

One `Model` (`app.go`) with:

- **Modes**: `modeResults` (grid), `modeDetail` (tabbed row), `modeText` (viewport: help,
  EXPLAIN, revision diffs), `modeBrowse` (Finder panes), `modeDiff` (pairs + unified diff).
  The chooser (`chooserOpen`) and help (`helpOpen`) are overlays, not modes.
- **Focus**: `focusCmd` (the textarea), `focusMain`, `focusDrawer` (history).
- **Per-mode state**: `browse *browseState`, `det *detailState` (with `picker *revPicker`),
  `diff *diffState`. Each mode file owns its state, its `…Key` handler and its `…View`:
  `browse.go`, `detail.go` + `revisions.go`, `diff.go`, `actions.go` (grid, chips, pivots).
- **Key routing order** (`key()` in `app.go`), which is where most "key does nothing" bugs
  have lived: completion popup → open revision picker → global chords (`^Q ^/ ^G ^O ^R ^X ^B`,
  Esc, ⇧Tab, Tab) → help swallows → drawer → main by mode → command area. A new overlay that
  owns Esc must be checked *before* the global switch, like the popup and picker are.
- **Esc goes back one level**: popup → picker → overlay → detail's `from` mode → text's
  `textFrom` → browse → chooser. Keep that invariant.
- **Navigation is statement rewriting**: chips, pivots, browse commits and marks all build a
  `lang.SelectStmt` and call `rewrite()`, which prints it into the command area and runs it.
  Never mutate results in place.
- **Async work is injected**: `Runner` (statements), `Sampler` (live catalog), `Fetcher`
  (resource panes), `DataFetcher`/`DataLoader`/`DataSaver` (unit data and the one write),
  `RevLoader`/`RevDataLoader`. `run.go` wires them to the real client; tests pass stubs. A
  loader returns a `tea.Cmd` producing a typed msg (`resultMsg`, `resourcesMsg`,
  `unitDataMsg`, `savedMsg`, …) handled in `Update`. Match messages on the unit key, never on
  position, because the user may have moved on.
- **Statement execution** is batched with `tick()` so the loading panel counts elapsed time;
  `m.running` replaces the main area until the result lands.
- **Marks** are a two-slot set toggled with `m` (browse selections for diffs, revisions in the
  picker). `d` runs the diff. Keep `m`/`d` meaning that everywhere.
- **Writes**: exactly one, `SaveUnitData`, always with `If-Match`; a resource edit is the
  unit rewritten with one document replaced (`exec.Stream.Replace`). An empty hash refuses to
  write. Anything new that writes should go through the same shape: read with ETag, edit,
  conditional PUT, 409 → reload and keep the draft.

## Conventions worth keeping

- Labels are the topology. Anything that assumes a label lives on the unit is wrong for real
  orgs; use `catalog.Live.Coverage` to pick `Labels.k` vs `Space.Labels.k` (see `presets()`).
- Default scope is org-wide; the status line names the scope with every result.
- Every fetch selects joined fields (`Space.Slug`, `Unit.Labels`, …) so the server trims the
  joined objects; never drop that or the org-wide payloads triple.
- No F-keys. Global actions are control chords the readline editor does not use.
- New docs are lowercase kebab-case.

## Testing

- `go test ./...` is offline. `stubRunner`, `stubResources`, `stubSpaces`, `stubDiff`,
  `fakeResources`, `liveWith` (in `internal/tui/*_test.go`) stand in for the server. `press`,
  `typeText`, `runCmd` drive the model; `runCmd` flattens `tea.BatchMsg` and skips ticks.
- Golden tests in `internal/plan/plan_test.go` pin statement → cub command → API request.
  When selects change on purpose, regenerate the expectations from the test's `got` output.
- Live smoke test under a pty when the model test cannot prove something:
  `script -q /dev/null zsh -c 'stty cols 160 rows 40; cub commander'` fed by a subshell that
  `printf`s keys with sleeps, then strip ANSI and grep. Escape sequences: `\x1b[Z` ⇧Tab,
  `\x1b[A-D` arrows, `\x11` ^Q, `\x18` ^X, `\x12` ^R, `\x1f` ^/. Set `EDITOR` to a script to
  drive `e`. The plugin binary takes `CUB_SERVER`/`CUB_TOKEN`/`CUB_CONTEXT` from the
  environment, so it can be pointed at another context without switching `cub context`.
- Never write to the production org for a test; the demo context is for that.

## Loop

`make plugin` builds the working tree and installs it (`cub commander version` shows
`-dirty`/`-N-g…`). A `v*` tag runs the release workflow (darwin/linux × amd64/arm64) and
`cub plugin install confighub/cub-commander` fetches it. CI runs vet/test/build but not
gofmt; run `gofmt -l .` before tagging.
