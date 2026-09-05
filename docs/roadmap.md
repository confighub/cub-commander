# cub commander — roadmap

Each milestone is demoable on its own and merges independently. Status column is the source
of truth; update it when state changes. Design: `design.md`.

| # | Milestone | Demo | Status |
|---|---|---|---|
| M0 | Design and roadmap docs in repo | this file | done 2026-09-02 |
| M1 | Engine: parser, compiler, executor | hidden `cub commander -e "SELECT Slug, Space.Slug FROM Unit IN * WHERE Labels.Environment = 'prod'"` prints a table; `EXPLAIN` prints the cub command. Golden tests statement → cub command. Needed under everything; not a user surface. | done 2026-09-02 (SELECT/HAVING/GROUP BY/ORDER/LIMIT, EXPLAIN, USE, SHOW ENTITIES/JOINS; all 20 list entities) |
| M2 | TUI walking skeleton | `cub commander` opens the screen: command area (multi-line, Enter/Shift+Enter/Ctrl+Enter), results grid with sort, detail pane, chips row, history drawer with Ctrl+R, Ctrl+X shows the cub command. | done 2026-09-02 (Bubble Tea v2 on charm.land paths; also chips add/remove, order-by toggle, row pivots s/t/u/d/r/l, detail view, help overlay) |
| M3 | The ramp: home, browse, compose | Home screen with counts, browse-by list, saved Views/Filters, recents; collapsed readout line with `:` to expand; `browse by` (^B) with presets and a path chooser; Finder columns and tree renderings; selection appends chips; focusable chips with an operator/value editor; column picker; ^G hands the selector to the grid. Readout syntax decided 2026-09-03: pipeline (design §4.1), implemented the same day with SQL kept as an on-ramp. | in progress: 2026-09-04 shipped the chooser as the start screen (presets from the org's label keys, Unit/Space/Target), `browse by` Finder columns with counts, pending chips, `g`/^G commit to where steps, `Labels.*` column expansion with label-per-column defaults. Left: entity counts on home, saved Views/Filters and recents on home, collapsed readout with `:`, tree rendering, chip editor, column picker. |
| M4 | Catalog and completion | Catalog drawer (^T); completion of entities, attributes, joins, operators-by-type, label keys and values with counts, slugs, functions; `SHOW`/`DESCRIBE`; join table checked in with a probe test. | mostly done 2026-09-02, pulled forward: Tab completion in the command area (entities, attributes from the bundled OpenAPI schema, joins, label keys/values and spaces from a live sample, operators by type, enum values, clause keywords), `SHOW COLUMNS/JOINS/LABELS/VALUES/SPACES`, `DESCRIBE`. Left: catalog drawer (^T), function signatures, join-table probe test. |
| M5 | Views and Filters round-trip | `FROM ops-home/by-target`, ^S save-as-view form, `CREATE FILTER`/`CREATE VIEW … AS SELECT`; the saved entity works in `cub unit list --view`; saved views appear as browse roots. | todo |
| M6 | Function columns, HAVING, aggregates | `SELECT Slug, get-replicas() AS r … HAVING r < 2 OR …`, `GROUP BY … COUNT(*)`; EXPLAIN and the top bar show server vs local row counts. | todo |
| M7 | Topology browse | `BROWSE Unit BY UPSTREAM` clone tree and `BY LINKS` in the tree renderer; component × variant × target as a browse preset. | todo |
| M8 | Diff | selection-vs-selection (shipped 2026-09-04: `diff A vs B [by …]`, auto pairing with refinement, DataHash classification, lazy data diff, marks in browse with m/d); still to come: revisions, unit-with-live, changeset; a Diff tab in Detail. | in progress |
| M9 | Hardening for the fleet dataset | Budgets and hints on the cub-demo ~100-cluster org; lazy sampling; `\watch`; `cub …` line pass-through in the command area. | todo |
| P2a | Edit one unit's data in $EDITOR with If-Match (shipped 2026-09-04) | `e` on the Data tab of a unit's detail; conflicts reload the head and keep the draft. | done |
| P2b | Rollouts | Design in `rollouts.md` (2026-09-05): ChangeOrder list preset with stage/next/blocker columns, rollout mode (stage strip, per-space taken/released/healthy, gates in CLI wording), source diff from the change order's tags, dry-run preview per stage, `P` promote / `L` release / `B` both with a confirm overlay. Milestones R1..R5 there; steel thread is chapter 1 of the change-workflows walkthrough. | R1 shipped 2026-09-05 (read-only mode, live on the Demo org); R2 preview next |
| P2 | Write phase design | `UPDATE`/`DELETE`/`CALL … WHERE` with live dry-run preview, changesets as transactions. Separate design doc. | gated on phase 1 |

Product asks raised by the lab, to file as issues when they bite:

- `GET /api/schema/queryable` (attributes, types, joins, computed) so clients can complete and validate.
- `OR`/`NOT`/parentheses in where.
- Tabular envelope for function invocations across a selector.
- Live-vs-desired and changeset diff endpoints.
