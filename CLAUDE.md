# cub-commander

Public repo (github.com/confighub/cub-commander, MIT): a `cub` plugin, the terminal lab for
the ConfigHub data model.

Read before changing anything: `docs/architecture.md` (how the code and the TUI fit
together, key-routing order, test harness), then `docs/design.md` (the language and the
screens) and `docs/roadmap.md` (milestones and status). If the work touches change orders —
the most active part of the codebase — `docs/change-orders.md` is the one that matters: the
server model the reading is built on, what is a gate, and what is still unproven.

Loop: `make plugin` installs the working tree into cub; `go test ./...` is offline; a `v*`
tag makes a release. Never write to the production org for a test; the demo context is for
that. Nothing here should assume labels live on units (see architecture notes).
