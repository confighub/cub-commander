package tui

import (
	"context"
	"net/url"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/confighub/cub-commander/internal/changeorder"
	"github.com/confighub/cub-commander/internal/exec"
	"github.com/confighub/cub-commander/internal/lang"
	"github.com/confighub/cub-commander/internal/plan"
)

func init() { changeOrderRefreshEvery = time.Millisecond } // never wait on the auto-refresh in tests

// stubChangeOrders answers ChangeOrder statements from the chapter-1 fixture,
// reading change orders exactly as the real runner does.
func stubChangeOrders(t *testing.T, mem *changeorder.MemClient) Runner {
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
		if msg, err := ChangeOrderRunner(ctx, mem, sel, p, rows); err != nil || msg != nil {
			return msg, err
		}
		res, err := exec.Local(p, rows)
		if err != nil {
			return nil, err
		}
		return resultMsg{stmt: sel, plan: p, res: res}, nil
	}
}

func openChangeOrders(t *testing.T) (tea.Model, *changeorder.MemClient) {
	t.Helper()
	mem := changeorder.ChapterOne()
	var m tea.Model = New(planSession(), stubChangeOrders(t, mem), nil, nil)
	mm := m.(Model)
	mm.changeLoader = func(ctx context.Context, ro *changeorder.ChangeOrder, spaceID string) ([]changeorder.UnitChange, error) {
		return changeorder.ChangeIn(ctx, mem, ro, spaceID)
	}
	mm.previewLoader = func(ctx context.Context, ro *changeorder.ChangeOrder, stage int) (*changeorder.Preview, error) {
		return changeorder.PreviewStage(ctx, mem, ro, stage)
	}
	mm.promoter = func(ctx context.Context, ro *changeorder.ChangeOrder, stage int, plan string) ([]changeorder.Outcome, error) {
		out, _, err := changeorder.PromoteStage(ctx, mem, ro, stage, plan)
		return out, err
	}
	m = mm
	m, _ = m.Update(tea.WindowSizeMsg{Width: 160, Height: 45})
	mm = m.(Model)
	for i, it := range mm.chooserItems {
		if it.stmt == ChangeOrdersPreset {
			mm.chooserCursor = i
		}
	}
	m = mm
	m = press(m, "enter")
	return m, mem
}

func TestChangeOrdersPresetLists(t *testing.T) {
	m, _ := openChangeOrders(t)
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

func TestOpenChangeOrderFromRow(t *testing.T) {
	m, mem := openChangeOrders(t)
	// a list opened from the chooser takes arrow keys straight away
	m = press(m, "down")
	if mm := m.(Model); mm.tbl.Cursor() != 1 {
		t.Fatalf("down did not move the cursor (focus %v, table cursor %d)", mm.focus, mm.tbl.Cursor())
	}
	m = press(m, "enter") // second row: cert-manager (the list is newest first)
	mm := m.(Model)
	if mm.mode != modeChangeOrder || mm.order == nil {
		t.Fatalf("mode %v after enter; status %s", mm.mode, mm.status)
	}
	if !strings.Contains(mm.cmd.Value(), "| changeorder") || !strings.Contains(mm.cmd.Value(), "ChangeOrderID = 'co-cm'") {
		t.Errorf("statement: %s", mm.cmd.Value())
	}
	// opens on the next stage (prod) with its gates, as the server evaluated them
	if mm.order.stage != 4 {
		t.Errorf("selected stage %d", mm.order.stage)
	}
	v := stripANSI(m.View().Content)
	if !strings.Contains(v, "promote/release/both") || !strings.Contains(v, "full diff") {
		t.Errorf("key bar is not the change order one:\n%s", v)
	}
	for _, want := range []string{"source", "bases", "dev", "test", "prod", "final", "workflow cert-manager-workflow · component cert-manager", "gates on prod: 5 of 6 satisfied", "✗ Variant 'us-east-test1' is not healthy", "✓ Released", "cert-manager-us-east-prod1", "what this promotes to cert-manager-us-east-prod1", "the server refused the dry run", "promote refused: Variant 'us-east-test1' is not healthy", "cub variant promote --change-order cert-manager-base/cert-manager-1-17-0", "--target-stage prod --dry-run"} {
		if !strings.Contains(v, want) {
			t.Errorf("change order view lacks %q:\n%s", want, v)
		}
	}
	// ← to test: a taken space, the change loads
	var cmd tea.Cmd
	m, cmd = m.Update(tea.KeyPressMsg{Code: tea.KeyLeft})
	m = runCmd(m, cmd, 0)
	v = m.View().Content
	if !strings.Contains(v, "stage: test") || !strings.Contains(v, "live: Degraded (release 3)") {
		t.Errorf("test stage view:\n%s", v)
	}
	// ← ← ← to source: the ordered change, as the server diffs the tags
	for i := 0; i < 3; i++ {
		m, cmd = m.Update(tea.KeyPressMsg{Code: tea.KeyLeft})
		m = runCmd(m, cmd, 0)
	}
	v = stripANSI(m.View().Content)
	for _, want := range []string{"the ordered change", "controller", "rev 2 → 3", "· 1 field", "- quay.io/jetstack/cert-manager-controller:v1.16.0", "+ quay.io/jetstack/cert-manager-controller:v1.17.0", "no change: namespace, webhook"} {
		if !strings.Contains(v, want) {
			t.Errorf("source view lacks %q:\n%s", want, v)
		}
	}
	// Tab focuses the diff pane; ↓ then scrolls it instead of moving the space
	m = press(m, "tab", "down", "down")
	if mm = m.(Model); mm.order.pane != 1 || mm.order.scroll != 2 || mm.focus != focusMain {
		t.Errorf("tab/down: pane %d scroll %d focus %v", mm.order.pane, mm.order.scroll, mm.focus)
	}
	m = press(m, "up", "tab")
	if mm = m.(Model); mm.order.pane != 0 || mm.order.scroll != 1 {
		t.Errorf("up/tab: pane %d scroll %d", mm.order.pane, mm.order.scroll)
	}
	m = press(m, "shift+tab")
	if mm = m.(Model); mm.focus != focusCmd {
		t.Errorf("shift+tab should still move focus to the command area: %v", mm.focus)
	}
	m = press(m, "shift+tab")
	calls := 0
	for _, l := range mem.Log {
		if strings.HasPrefix(l, "/unit_diff?") && strings.Contains(l, "SpaceID+%3D+%27cm-base%27") {
			calls++
		}
	}
	if calls != 1 {
		t.Errorf("the ordered change was read %d times: %v", calls, mem.Log)
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
	if mm = m.(Model); mm.mode != modeChangeOrder {
		t.Errorf("esc from text: mode %v", mm.mode)
	}
	// Esc leaves the change order and restores the list statement
	m = press(m, "esc")
	mm = m.(Model)
	if mm.mode != modeResults || !strings.Contains(mm.cmd.Value(), "state(), stage()") || len(mm.result.Rows) != 2 {
		t.Errorf("esc: mode %v stmt %q", mm.mode, mm.cmd.Value())
	}
}

func TestChangeOrderStageArgumentAndFresh(t *testing.T) {
	m, _ := openChangeOrders(t)
	mm := m.(Model)
	mm.focus = focusCmd
	mm.cmd.SetValue("")
	m = mm
	m = typeText(m, "ChangeOrder | in * | where ChangeOrderID = 'co-ca' | changeorder stage dev")
	m = press(m, "enter")
	mm = m.(Model)
	if mm.mode != modeChangeOrder || mm.order.stage != 2 {
		t.Fatalf("mode %v stage %d status %s", mm.mode, mm.order.stage, mm.status)
	}
	v := m.View().Content
	if !strings.Contains(v, "Ready to Promote") || !strings.Contains(v, "next: bases · no gates on the first stage · promote is open") {
		t.Errorf("fresh change order:\n%s", v)
	}
	// the ambiguous case is refused, not guessed
	mm = m.(Model)
	mm.focus = focusCmd
	mm.cmd.SetValue("")
	m = mm
	m = typeText(m, "ChangeOrder | in * | changeorder")
	m = press(m, "enter")
	if mm = m.(Model); !mm.statusErr || !strings.Contains(mm.status, "matched 3") {
		t.Errorf("status %q", mm.status)
	}
}

func TestPreviewAndPromoteFromTUI(t *testing.T) {
	m, mem := openChangeOrders(t)
	// row 0 is catalog-api (fresh); it opens on bases with the dry run
	m = press(m, "enter")
	mm := m.(Model)
	if mm.mode != modeChangeOrder || mm.order.stage != 1 {
		t.Fatalf("mode %v stage %d", mm.mode, mm.order.stage)
	}
	v := stripANSI(m.View().Content)
	for _, want := range []string{"what this promotes to catalog-api-dev", "api  · 2 fields", "image: catalog-api:5.2.0 → catalog-api:5.3.0", "memory: 512Mi → 1Gi", "no change: config", "P promotes this stage"} {
		if !strings.Contains(v, want) {
			t.Errorf("preview lacks %q:\n%s", want, v)
		}
	}
	// ↓ ↓ to the test class base: the memory stays 2Gi, only the image moves
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	v = stripANSI(m.View().Content)
	if !strings.Contains(v, "what this promotes to catalog-api-test") || !strings.Contains(v, "api  · 1 field  · 1 kept") || strings.Contains(v, "2Gi → 1Gi") {
		t.Errorf("test preview:\n%s", v)
	}
	for _, want := range []string{"⚠ NOT changed", "stays 2Gi", "upstream set 1Gi", "protected: a merge must not overwrite it"} {
		if !strings.Contains(v, want) {
			t.Errorf("kept field not called out (%q):\n%s", want, v)
		}
	}
	// P opens the confirm overlay with the request and the cub command; n cancels
	m, _ = m.Update(tea.KeyPressMsg{Code: 'P', Text: "P"})
	mm = m.(Model)
	if mm.order.confirm == nil {
		t.Fatalf("P did not open the confirm: %s", mm.status)
	}
	v = stripANSI(m.View().Content)
	for _, want := range []string{"Promote catalog-api-5-3-0 into stage bases", "cub variant promote --change-order catalog-api-base/catalog-api-5-3-0 --target-stage bases --expected-plan plan-catalog-api-5-3-0-bases", `POST /api/promote  {"ChangeOrderID":"co-ca","ExpectedPlan":"plan-catalog-api-5-3-0-bases","TargetStage":"bases"}`, "3 unit(s), 4 field(s) change", "1 field(s) NOT changed (kept)", "y promote"} {
		if !strings.Contains(v, want) {
			t.Errorf("confirm lacks %q:\n%s", want, v)
		}
	}
	m, _ = m.Update(tea.KeyPressMsg{Code: 'n', Text: "n"})
	if mm = m.(Model); mm.order.confirm != nil || len(mem.Promotes) != 0 {
		t.Fatalf("cancel: confirm %v promotes %v", mm.order.confirm != nil, mem.Promotes)
	}
	// P then y runs it: one promote of the stage, then a refresh and the report
	m, _ = m.Update(tea.KeyPressMsg{Code: 'P', Text: "P"})
	var cmd tea.Cmd
	m, cmd = m.Update(tea.KeyPressMsg{Code: 'y', Text: "y"})
	m = runCmd(m, cmd, 0)
	mm = m.(Model)
	if len(mem.Promotes) == 0 || !strings.HasPrefix(mem.Promotes[0], "catalog-api-5-3-0 → bases") {
		t.Errorf("promotes: %v", mem.Promotes)
	}
	if mm.mode != modeText || mm.textTitle != "Promote bases" {
		t.Errorf("after promote: mode %v title %q status %q", mm.mode, mm.textTitle, mm.status)
	}
	v = stripANSI(m.View().Content)
	if !strings.Contains(v, "catalog-api-dev") || !strings.Contains(v, "1 unit(s) written, 1 marked") || !strings.Contains(v, "3 space(s) landed, 0 failed") {
		t.Errorf("report:\n%s", v)
	}
	m = press(m, "esc")
	mm = m.(Model)
	if mm.mode != modeChangeOrder || mm.order.stage != 1 {
		t.Errorf("esc from report: mode %v stage %d", mm.mode, mm.order.stage)
	}
	// the refreshed reading has bases taken and dev next
	if v := stripANSI(m.View().Content); !strings.Contains(v, "next: dev") {
		t.Errorf("after the refresh:\n%s", v)
	}
}

func TestPromoteRefusedByGate(t *testing.T) {
	m, _ := openChangeOrders(t)
	mm := m.(Model)
	mm.promoter = func(ctx context.Context, ro *changeorder.ChangeOrder, stage int, plan string) ([]changeorder.Outcome, error) {
		t.Error("promoter called past a failing gate")
		return nil, nil
	}
	mm.tbl.SetCursor(1) // cert-manager: prod is next, test1 unhealthy
	m = mm
	m = press(m, "enter")
	m, _ = m.Update(tea.KeyPressMsg{Code: 'P', Text: "P"})
	mm = m.(Model)
	if mm.order.confirm != nil || !mm.statusErr || !strings.Contains(mm.status, "Variant 'us-east-test1' is not healthy") {
		t.Errorf("confirm %v status %q", mm.order.confirm != nil, mm.status)
	}
	// on a stage that is not the next one, P says which is
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyLeft})
	m, _ = m.Update(tea.KeyPressMsg{Code: 'P', Text: "P"})
	if mm = m.(Model); !strings.Contains(mm.status, "not the next stage: prod is") {
		t.Errorf("status %q", mm.status)
	}
}

func TestReleaseFromTUI(t *testing.T) {
	m, mem := openChangeOrders(t)
	mm := m.(Model)
	mm.releaser = func(ctx context.Context, ro *changeorder.ChangeOrder, stage int, promoted []changeorder.Outcome) ([]changeorder.ReleaseOutcome, error) {
		return changeorder.ReleaseStage(ctx, mem, changeorder.AfterPromote(ro, promoted), stage)
	}
	mm.tbl.SetCursor(1) // cert-manager: opens on prod
	m = mm
	m = press(m, "enter")
	// L on prod: nothing has taken it
	m, _ = m.Update(tea.KeyPressMsg{Code: 'L', Text: "L"})
	if mm = m.(Model); mm.order.confirm != nil || !strings.Contains(mm.status, "has not taken the change yet") {
		t.Fatalf("L on prod: confirm %v status %q", mm.order.confirm != nil, mm.status)
	}
	// ← to test, pretend test2 is unreleased, L opens the overlay with the publish line
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyLeft})
	mm = m.(Model)
	for i := range mm.order.ro.Stages[3].Spaces {
		if mm.order.ro.Stages[3].Spaces[i].Slug == "cert-manager-us-east-test2" {
			mm.order.ro.Stages[3].Spaces[i].Released = false
		}
	}
	m = mm
	m, _ = m.Update(tea.KeyPressMsg{Code: 'L', Text: "L"})
	mm = m.(Model)
	if mm.order.confirm == nil {
		t.Fatalf("L did not open the confirm: %s", mm.status)
	}
	v := stripANSI(m.View().Content)
	for _, want := range []string{"Release cert-manager-1-17-0 from stage test", "cert-manager-us-east-test1", "skipped: already released", `{"ChangeOrderID":"co-cm","TagID":"tag-cm-end"}`, "cub release publish --revision ChangeOrder:cert-manager-base/cert-manager-1-17-0 cert-manager-us-east-test2", "y release"} {
		if !strings.Contains(v, want) {
			t.Errorf("confirm lacks %q:\n%s", want, v)
		}
	}
	var cmd tea.Cmd
	m, cmd = m.Update(tea.KeyPressMsg{Code: 'y', Text: "y"})
	m = runCmd(m, cmd, 0)
	mm = m.(Model)
	published := 0
	for _, p := range mem.Posts {
		if strings.HasPrefix(p, "/space/cm-test2/release ") {
			published++
		}
	}
	if published == 0 {
		t.Errorf("posts: %v", mem.Posts)
	}
	if mm.mode != modeText || mm.textTitle != "Release test" || !strings.Contains(stripANSI(m.View().Content), "published release ") {
		t.Errorf("after release: mode %v title %q", mm.mode, mm.textTitle)
	}
}

func TestPromoteAndReleaseFromTUI(t *testing.T) {
	m, mem := openChangeOrders(t)
	mm := m.(Model)
	mm.releaser = func(ctx context.Context, ro *changeorder.ChangeOrder, stage int, promoted []changeorder.Outcome) ([]changeorder.ReleaseOutcome, error) {
		return changeorder.ReleaseStage(ctx, mem, changeorder.AfterPromote(ro, promoted), stage)
	}
	m = mm
	m = press(m, "enter") // catalog-api, next = bases (no release targets)
	m, _ = m.Update(tea.KeyPressMsg{Code: 'B', Text: "B"})
	if mm = m.(Model); mm.order.confirm != nil || !strings.Contains(mm.status, "no release targets") {
		t.Fatalf("B on bases: %q", mm.status)
	}
	// land bases on the server, refresh: dev is next and has a target
	mm = m.(Model)
	if _, _, err := changeorder.PromoteStage(context.Background(), mem, mm.order.ro, 1, ""); err != nil {
		t.Fatal(err)
	}
	m, cmd := m.Update(tea.KeyPressMsg{Code: 'R', Text: "R"})
	m = runCmd(m, cmd, 0)
	m, cmd = m.Update(tea.KeyPressMsg{Code: tea.KeyRight}) // → dev, which runs its preview
	m = runCmd(m, cmd, 0)
	mm = m.(Model)
	if mm.order.stage != 2 || mm.order.ro.NextName() != "dev" {
		t.Fatalf("stage %d next %q", mm.order.stage, mm.order.ro.NextName())
	}
	// dev1 lacks config: the server clones it, and the preview says so
	if v := stripANSI(m.View().Content); !strings.Contains(v, "would add from upstream: config") {
		t.Errorf("dev preview:\n%s", v)
	}
	m, _ = m.Update(tea.KeyPressMsg{Code: 'B', Text: "B"})
	mm = m.(Model)
	if mm.order.confirm == nil {
		t.Fatalf("B did not open the confirm: %q", mm.status)
	}
	v := stripANSI(m.View().Content)
	for _, want := range []string{"Promote and release dev", "1 added from upstream", "cub release publish --revision ChangeOrder:catalog-api-base/catalog-api-5-3-0 catalog-api-us-east-dev1", "y promote and release"} {
		if !strings.Contains(v, want) {
			t.Errorf("confirm lacks %q:\n%s", want, v)
		}
	}
}

func TestChangeOrderAutoRefresh(t *testing.T) {
	m, mem := openChangeOrders(t)
	mm := m.(Model)
	mm.tbl.SetCursor(1)
	m = mm
	m = press(m, "enter")
	mm = m.(Model)
	gen := mm.order.gen
	before := len(mem.Log)
	// the tick for this reading re-runs the statement quietly and keeps the
	// position, the loaded dry run and the loaded diffs (re-running them redrew
	// the pane every period)
	mm.order.stage = 3
	loaded := &changeorder.Preview{}
	mm.order.previews[3] = loaded
	mm.order.changes["kept"] = nil
	m = mm
	m, cmd := m.Update(changeOrderTickMsg{gen: gen})
	if cmd == nil {
		t.Fatal("tick did not refresh")
	}
	m = runCmd(m, cmd, 0)
	mm = m.(Model)
	if mm.mode != modeChangeOrder || mm.order.stage != 3 || mm.order.gen == gen || len(mem.Log) == before || mm.running {
		t.Errorf("after tick: mode %v stage %d gen %d→%d requests %d→%d running %v", mm.mode, mm.order.stage, gen, mm.order.gen, before, len(mem.Log), mm.running)
	}
	if mm.order.previews[3] != loaded {
		t.Error("tick dropped the loaded dry run")
	}
	if _, ok := mm.order.changes["kept"]; !ok {
		t.Error("tick dropped the loaded diffs")
	}
	// R re-reads everything, the dry run included
	m = press(m, "R")
	mm = m.(Model)
	if mm.order.previews[3] == loaded {
		t.Error("R kept the old dry run")
	}
	// a stale tick (an older reading's) does nothing
	m, cmd = m.Update(changeOrderTickMsg{gen: gen})
	if cmd != nil {
		t.Error("stale tick refreshed")
	}
	// shift+r reads as R
	if keyName(tea.KeyPressMsg{Code: 'r', Mod: tea.ModShift}) != "R" || keyName(tea.KeyPressMsg{Code: 'R', Text: "R"}) != "R" {
		t.Error("keyName")
	}
	// leaving the mode stops the refresh
	m = press(m, "esc")
	mm = m.(Model)
	if mm.mode != modeResults {
		t.Fatalf("mode %v", mm.mode)
	}
}
