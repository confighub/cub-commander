package tui

import (
	"context"
	"net/url"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/confighub/cub-commander/internal/exec"
	"github.com/confighub/cub-commander/internal/lang"
	"github.com/confighub/cub-commander/internal/plan"
	"github.com/confighub/cub-commander/internal/rollout"
)

// stubRollouts answers ChangeOrder statements from the chapter-1 fixture,
// deriving rollouts exactly as the real runner does.
func stubRollouts(t *testing.T, mem *rollout.MemClient) Runner {
	return func(ctx context.Context, st lang.Stmt, sess plan.Session) (tea.Msg, error) {
		sel := st.(*lang.SelectStmt)
		p, err := plan.Compile(sel, sess)
		if err != nil {
			return nil, err
		}
		rows, err := mem.List(ctx, p.Entity.OrgPath, url.Values{"where": {p.List.Where}})
		if err != nil {
			return nil, err
		}
		if msg, err := RolloutRunner(ctx, mem, sel, p, rows); err != nil || msg != nil {
			return msg, err
		}
		res, err := exec.Local(p, rows)
		if err != nil {
			return nil, err
		}
		return resultMsg{stmt: sel, plan: p, res: res}, nil
	}
}

func openRollouts(t *testing.T) (tea.Model, *rollout.MemClient) {
	t.Helper()
	mem := rollout.ChapterOne()
	var m tea.Model = New(planSession(), stubRollouts(t, mem), nil, nil)
	mm := m.(Model)
	mm.changeLoader = func(ctx context.Context, o rollout.Order, spaceID string) ([]rollout.UnitChange, error) {
		return rollout.Change(ctx, mem, o, spaceID)
	}
	mm.previewLoader = func(ctx context.Context, ro *rollout.Rollout, stage int) (*rollout.Preview, error) {
		return rollout.PreviewStage(ctx, mem, ro, stage)
	}
	mm.promoter = func(ctx context.Context, ro *rollout.Rollout, stage int) ([]rollout.Outcome, error) {
		return rollout.PromoteStage(ctx, mem, ro, stage)
	}
	m = mm
	m, _ = m.Update(tea.WindowSizeMsg{Width: 160, Height: 45})
	mm = m.(Model)
	for i, it := range mm.chooserItems {
		if it.stmt == RolloutsPreset {
			mm.chooserCursor = i
		}
	}
	m = mm
	m = press(m, "enter")
	return m, mem
}

func TestRolloutsPresetLists(t *testing.T) {
	m, _ := openRollouts(t)
	v := m.View().Content
	for _, want := range []string{"cert-manager-1-17-0", "Degraded", "Variant 'us-east-test1' is not healthy", "catalog-api-5-3-0", "Ready to Promote", "No blocker."} {
		if !strings.Contains(v, want) {
			t.Errorf("list lacks %q:\n%s", want, v)
		}
	}
	if strings.Contains(v, "traefik-3-0-0") {
		t.Errorf("Released order not hidden:\n%s", v)
	}
	mm := m.(Model)
	if mm.mode != modeResults || len(mm.result.Rows) != 2 {
		t.Fatalf("mode %v rows %d", mm.mode, len(mm.result.Rows))
	}
	// newest first: catalog-api (2026-09-05) before cert-manager (2026-09-03)
	if got := exec.Format(mm.result.Rows[0][0]); got != "catalog-api-5-3-0" {
		t.Errorf("first row %s", got)
	}
}

func TestOpenRolloutFromRow(t *testing.T) {
	m, mem := openRollouts(t)
	// a list opened from the chooser takes arrow keys straight away
	m = press(m, "down")
	if mm := m.(Model); mm.tbl.Cursor() != 1 {
		t.Fatalf("down did not move the cursor (focus %v, table cursor %d)", mm.focus, mm.tbl.Cursor())
	}
	m = press(m, "enter") // second row: cert-manager (the list is newest first)
	mm := m.(Model)
	if mm.mode != modeRollout || mm.roll == nil {
		t.Fatalf("mode %v after enter; status %s", mm.mode, mm.status)
	}
	if !strings.Contains(mm.cmd.Value(), "| rollout") || !strings.Contains(mm.cmd.Value(), "ChangeOrderID = 'co-cm'") {
		t.Errorf("statement: %s", mm.cmd.Value())
	}
	// opens on the next stage (prod) with its gates
	if mm.roll.stage != 4 {
		t.Errorf("selected stage %d", mm.roll.stage)
	}
	v := m.View().Content
	if !strings.Contains(v, "promote/release/both") || !strings.Contains(v, "full diff") {
		t.Errorf("key bar is not the rollout one:\n%s", v)
	}
	for _, want := range []string{"source", "bases", "dev", "test", "prod", "final", "gates on prod: 2 of 3 satisfied", "Variant 'us-east-test1' is not healthy", "cert-manager-us-east-prod1", "what this promotes to cert-manager-us-east-prod1", "promote refused: Variant 'us-east-test1' is not healthy", "cub variant promote --change-order cert-manager-base/cert-manager-1-17-0", "--target-stage prod --dry-run"} {
		if !strings.Contains(v, want) {
			t.Errorf("rollout view lacks %q:\n%s", want, v)
		}
	}
	// ← to test: a taken space, the change loads
	var cmd tea.Cmd
	m, cmd = m.Update(tea.KeyPressMsg{Code: tea.KeyLeft})
	m = runCmd(m, cmd, 0)
	v = m.View().Content
	if !strings.Contains(v, "stage: test") || !strings.Contains(v, "live: Degraded") {
		t.Errorf("test stage view:\n%s", v)
	}
	// ← ← ← to source: the ordered change from the tags
	for i := 0; i < 3; i++ {
		m, cmd = m.Update(tea.KeyPressMsg{Code: tea.KeyLeft})
		m = runCmd(m, cmd, 0)
	}
	v = m.View().Content
	for _, want := range []string{"the ordered change", "controller", "rev 2 → 3", "· 1 field", "-image: quay.io/jetstack/cert-manager-controller:v1.16.0", "+image: quay.io/jetstack/cert-manager-controller:v1.17.0", "no change: namespace, webhook"} {
		if !strings.Contains(v, want) {
			t.Errorf("source view lacks %q:\n%s", want, v)
		}
	}
	// Tab focuses the diff pane; ↓ then scrolls it instead of moving the space
	m = press(m, "tab", "down", "down")
	if mm = m.(Model); mm.roll.pane != 1 || mm.roll.scroll != 2 || mm.focus != focusMain {
		t.Errorf("tab/down: pane %d scroll %d focus %v", mm.roll.pane, mm.roll.scroll, mm.focus)
	}
	m = press(m, "up", "tab")
	if mm = m.(Model); mm.roll.pane != 0 || mm.roll.scroll != 1 {
		t.Errorf("up/tab: pane %d scroll %d", mm.roll.pane, mm.roll.scroll)
	}
	m = press(m, "shift+tab")
	if mm = m.(Model); mm.focus != focusCmd {
		t.Errorf("shift+tab should still move focus to the command area: %v", mm.focus)
	}
	m = press(m, "shift+tab")
	calls := 0
	for _, l := range mem.Log {
		if strings.Contains(l, "RevisionNum") && strings.Contains(l, "SpaceID+%3D+%27cm-base%27+AND+Tags+%3F+%27tag-cm-start%27") {
			calls++
		}
	}
	if calls != 1 {
		t.Errorf("start tag queried %d times: %v", calls, mem.Log)
	}
	// Enter opens the full diff as text, where the field line fits on one line; Esc returns
	m = press(m, "enter")
	if mm = m.(Model); mm.mode != modeText || !strings.Contains(mm.textTitle, "ordered change") {
		t.Errorf("enter: mode %v title %q", mm.mode, mm.textTitle)
	}
	if v := stripANSI(m.View().Content); !strings.Contains(v, "image: quay.io/jetstack/cert-manager-controller:v1.16.0 → quay.io/jetstack/cert-manager-controller:v1.17.0") {
		t.Errorf("full diff lacks the field line:\n%s", v)
	}
	m = press(m, "esc")
	if mm = m.(Model); mm.mode != modeRollout {
		t.Errorf("esc from text: mode %v", mm.mode)
	}
	// Esc leaves the rollout and restores the list statement
	m = press(m, "esc")
	mm = m.(Model)
	if mm.mode != modeResults || !strings.Contains(mm.cmd.Value(), "state(), stage()") || len(mm.result.Rows) != 2 {
		t.Errorf("esc: mode %v stmt %q", mm.mode, mm.cmd.Value())
	}
}

func TestRolloutStageArgumentAndFresh(t *testing.T) {
	m, _ := openRollouts(t)
	mm := m.(Model)
	mm.focus = focusCmd
	mm.cmd.SetValue("")
	m = mm
	m = typeText(m, "ChangeOrder | in * | where ChangeOrderID = 'co-ca' | rollout stage dev")
	m = press(m, "enter")
	mm = m.(Model)
	if mm.mode != modeRollout || mm.roll.stage != 2 {
		t.Fatalf("mode %v stage %d status %s", mm.mode, mm.roll.stage, mm.status)
	}
	v := m.View().Content
	if !strings.Contains(v, "Ready to Promote") || !strings.Contains(v, "next: bases · gates 1 of 1 satisfied · promote is open") {
		t.Errorf("fresh rollout:\n%s", v)
	}
	// the ambiguous case is refused, not guessed
	mm = m.(Model)
	mm.focus = focusCmd
	mm.cmd.SetValue("")
	m = mm
	m = typeText(m, "ChangeOrder | in * | rollout")
	m = press(m, "enter")
	if mm = m.(Model); !mm.statusErr || !strings.Contains(mm.status, "matched 3") {
		t.Errorf("status %q", mm.status)
	}
}

func TestPreviewAndPromoteFromTUI(t *testing.T) {
	m, mem := openRollouts(t)
	// row 0 is catalog-api (fresh); it opens on bases with the dry run
	m = press(m, "enter")
	mm := m.(Model)
	if mm.mode != modeRollout || mm.roll.stage != 1 {
		t.Fatalf("mode %v stage %d", mm.mode, mm.roll.stage)
	}
	v := stripANSI(m.View().Content)
	for _, want := range []string{"what this promotes to catalog-api-dev", "api  · 2 fields", "image: catalog-api:5.2.0 → catalog-api:5.3.0", "memory: 512Mi → 1Gi", "no change: config", "P promotes this stage"} {
		if !strings.Contains(v, want) {
			t.Errorf("preview lacks %q:\n%s", want, v)
		}
	}
	// ↓ to the test class base: the memory stays 2Gi, only the image moves
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	v = stripANSI(m.View().Content)
	if !strings.Contains(v, "what this promotes to catalog-api-test") || !strings.Contains(v, "api  · 1 field") || strings.Contains(v, "2Gi → 1Gi") {
		t.Errorf("test preview:\n%s", v)
	}
	// P opens the confirm overlay with the cub command; n cancels
	m, _ = m.Update(tea.KeyPressMsg{Code: 'P', Text: "P"})
	mm = m.(Model)
	if mm.roll.confirm == nil {
		t.Fatalf("P did not open the confirm: %s", mm.status)
	}
	v = stripANSI(m.View().Content)
	for _, want := range []string{"Promote catalog-api-5-3-0 into stage bases", "cub variant promote --change-order catalog-api-base/catalog-api-5-3-0 --target-stage bases", "3 unit(s), 4 field(s) change", "y promote"} {
		if !strings.Contains(v, want) {
			t.Errorf("confirm lacks %q:\n%s", want, v)
		}
	}
	realPatches := func() int {
		n := 0
		for _, q := range mem.Patches {
			if q.Get("dry_run") == "" && q.Get("upgrade") == "true" && q.Get("change_order") == "co-ca" {
				n++
			}
		}
		return n
	}
	m, _ = m.Update(tea.KeyPressMsg{Code: 'n', Text: "n"})
	if mm = m.(Model); mm.roll.confirm != nil || realPatches() != 0 {
		t.Fatalf("cancel: confirm %v real patches %d", mm.roll.confirm != nil, realPatches())
	}
	// P then y runs it: three real PATCHes, then a refresh and the report
	m, _ = m.Update(tea.KeyPressMsg{Code: 'P', Text: "P"})
	var cmd tea.Cmd
	m, cmd = m.Update(tea.KeyPressMsg{Code: 'y', Text: "y"})
	m = runCmd(m, cmd, 0)
	mm = m.(Model)
	// three spaces; the test harness may run a command twice, so a multiple of three
	if real := realPatches(); real < 3 || real%3 != 0 {
		t.Errorf("real patches: %d of %d", real, len(mem.Patches))
	}
	if mm.mode != modeText || mm.textTitle != "Promote bases" {
		t.Errorf("after promote: mode %v title %q status %q", mm.mode, mm.textTitle, mm.status)
	}
	v = stripANSI(m.View().Content)
	if !strings.Contains(v, "catalog-api-dev") || !strings.Contains(v, "2 unit(s) processed") || !strings.Contains(v, "3 space(s) landed, 0 failed") {
		t.Errorf("report:\n%s", v)
	}
	m = press(m, "esc")
	if mm = m.(Model); mm.mode != modeRollout || mm.roll.stage != 1 {
		t.Errorf("esc from report: mode %v stage %d", mm.mode, mm.roll.stage)
	}
}

func TestPromoteRefusedByGate(t *testing.T) {
	m, _ := openRollouts(t)
	mm := m.(Model)
	mm.promoter = func(ctx context.Context, ro *rollout.Rollout, stage int) ([]rollout.Outcome, error) {
		t.Error("promoter called past a failing gate")
		return nil, nil
	}
	mm.tbl.SetCursor(1) // cert-manager: prod is next, test1 unhealthy
	m = mm
	m = press(m, "enter")
	m, _ = m.Update(tea.KeyPressMsg{Code: 'P', Text: "P"})
	mm = m.(Model)
	if mm.roll.confirm != nil || !mm.statusErr || !strings.Contains(mm.status, "Variant 'us-east-test1' is not healthy") {
		t.Errorf("confirm %v status %q", mm.roll.confirm != nil, mm.status)
	}
	// on a stage that is not the next one, P says which is
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyLeft})
	m, _ = m.Update(tea.KeyPressMsg{Code: 'P', Text: "P"})
	if mm = m.(Model); !strings.Contains(mm.status, "not the next stage: prod is") {
		t.Errorf("status %q", mm.status)
	}
}

func TestReleaseFromTUI(t *testing.T) {
	m, mem := openRollouts(t)
	mm := m.(Model)
	mm.releaser = func(ctx context.Context, ro *rollout.Rollout, stage int, promoted []rollout.Outcome) ([]rollout.ReleaseOutcome, error) {
		return rollout.ReleaseStage(ctx, mem, rollout.AfterPromote(ro, promoted), stage)
	}
	mm.tbl.SetCursor(1) // cert-manager: opens on prod
	m = mm
	m = press(m, "enter")
	// L on prod: nothing has taken it
	m, _ = m.Update(tea.KeyPressMsg{Code: 'L', Text: "L"})
	if mm = m.(Model); mm.roll.confirm != nil || !strings.Contains(mm.status, "has not taken the change yet") {
		t.Fatalf("L on prod: confirm %v status %q", mm.roll.confirm != nil, mm.status)
	}
	// ← to test, pretend test2 is unreleased, L opens the overlay with the publish line
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyLeft})
	mm = m.(Model)
	for i := range mm.roll.ro.Stages[3].Spaces {
		if mm.roll.ro.Stages[3].Spaces[i].Slug == "cert-manager-us-east-test2" {
			mm.roll.ro.Stages[3].Spaces[i].Released = false
		}
	}
	m = mm
	m, _ = m.Update(tea.KeyPressMsg{Code: 'L', Text: "L"})
	mm = m.(Model)
	if mm.roll.confirm == nil {
		t.Fatalf("L did not open the confirm: %s", mm.status)
	}
	v := stripANSI(m.View().Content)
	for _, want := range []string{"Release cert-manager-1-17-0 from stage test", "cert-manager-us-east-test1", "skipped: already released", "cub release publish --revision ChangeOrder:cert-manager-base/cert-manager-1-17-0 cert-manager-us-east-test2", "y release"} {
		if !strings.Contains(v, want) {
			t.Errorf("confirm lacks %q:\n%s", want, v)
		}
	}
	var cmd tea.Cmd
	m, cmd = m.Update(tea.KeyPressMsg{Code: 'y', Text: "y"})
	m = runCmd(m, cmd, 0)
	mm = m.(Model)
	if len(mem.Posts) == 0 || !strings.HasPrefix(mem.Posts[0], "/space/cm-test2/release ") {
		t.Errorf("posts: %v", mem.Posts)
	}
	if mm.mode != modeText || mm.textTitle != "Release test" || !strings.Contains(stripANSI(m.View().Content), "published release 1") {
		t.Errorf("after release: mode %v title %q", mm.mode, mm.textTitle)
	}
}

func TestPromoteAndReleaseFromTUI(t *testing.T) {
	m, mem := openRollouts(t)
	mm := m.(Model)
	mm.releaser = func(ctx context.Context, ro *rollout.Rollout, stage int, promoted []rollout.Outcome) ([]rollout.ReleaseOutcome, error) {
		return rollout.ReleaseStage(ctx, mem, rollout.AfterPromote(ro, promoted), stage)
	}
	m = mm
	m = press(m, "enter") // catalog-api, next = bases (no release targets)
	m, _ = m.Update(tea.KeyPressMsg{Code: 'B', Text: "B"})
	if mm = m.(Model); mm.roll.confirm != nil || !strings.Contains(mm.status, "no release targets") {
		t.Fatalf("B on bases: %q", mm.status)
	}
	// pretend bases landed: dev is next and has a target
	mm = m.(Model)
	for i := range mm.roll.ro.Stages[1].Spaces {
		mm.roll.ro.Stages[1].Spaces[i].Taken = true
	}
	mm.roll.ro.Next = 2
	mm.roll.ro.Gates = []rollout.Gate{{Name: rollout.PrereqTaken, OK: true}}
	mm.roll.stage = 1
	m = mm
	m, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyRight}) // → dev, which runs its preview
	m = runCmd(m, cmd, 0)
	mm = m.(Model)
	if mm.roll.stage != 2 {
		t.Fatalf("stage %d", mm.roll.stage)
	}
	// dev1 lacks config in the fixture, so B is refused like P is
	m, _ = m.Update(tea.KeyPressMsg{Code: 'B', Text: "B"})
	if mm = m.(Model); mm.roll.confirm != nil || !strings.Contains(mm.status, "lacks config") {
		t.Fatalf("B past a missing unit: confirm %v status %q", mm.roll.confirm != nil, mm.status)
	}
}
