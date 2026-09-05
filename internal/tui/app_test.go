package tui

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/confighub/cub-commander/internal/cubclient"
	"github.com/confighub/cub-commander/internal/exec"
	"github.com/confighub/cub-commander/internal/lang"
	"github.com/confighub/cub-commander/internal/plan"
)

func stubRunner(t *testing.T) Runner {
	return func(ctx context.Context, st lang.Stmt, sess plan.Session) (tea.Msg, error) {
		sel := st.(*lang.SelectStmt)
		p, err := plan.Compile(sel, sess)
		if err != nil {
			return nil, err
		}
		rows := []cubclient.Row{
			{"Unit": map[string]any{"Slug": "backend", "UnitID": "u1", "HeadRevisionNum": 17.0, "Labels": map[string]any{"Tier": "Backend"}}, "Space": map[string]any{"Slug": "prod-eu", "SpaceID": "s1", "Labels": map[string]any{"Environment": "prod"}}},
			{"Unit": map[string]any{"Slug": "frontend", "UnitID": "u2", "HeadRevisionNum": 3.0, "Labels": map[string]any{"Tier": "Frontend"}}, "Space": map[string]any{"Slug": "prod-eu", "SpaceID": "s1", "Labels": map[string]any{"Environment": "dev"}}},
		}
		res, err := exec.Local(p, rows)
		if err != nil {
			return nil, err
		}
		return resultMsg{stmt: sel, plan: p, res: res}, nil
	}
}

// runKeys are the keys whose returned command is executed in tests (they run
// statements); a substring match here once ran the textarea's 500ms blink
// command for every typed letter.
var runKeys = map[string]bool{"enter": true, "f": true, "-": true, "o": true, "r": true, "g": true}

// runCmd executes a command and feeds its message back, flattening batches
// (a statement run is batched with the loading tick).
func runCmd(m tea.Model, cmd tea.Cmd, depth int) tea.Model {
	if cmd == nil || depth > 4 {
		return m
	}
	out := cmd()
	if b, ok := out.(tea.BatchMsg); ok {
		for _, c := range b {
			m = runCmd(m, c, depth+1)
		}
		return m
	}
	if out == nil {
		return m
	}
	if _, isTick := out.(tickMsg); isTick {
		return m // the tick only reschedules itself while running
	}
	if _, isTick := out.(rolloutTickMsg); isTick {
		return m // the rollout auto-refresh is driven explicitly in tests
	}
	var next tea.Cmd
	m, next = m.Update(out)
	if next != nil && depth < 2 {
		// follow one level (e.g. a browse result scheduling a fetch), but not ticks
		if _, isTickCmd := next().(tickMsg); !isTickCmd {
			// re-run since we consumed it: cheap for stubs
			m = runCmd(m, next, depth+1)
		}
	}
	return m
}

func press(m tea.Model, keys ...string) tea.Model {
	for _, k := range keys {
		var msg tea.KeyPressMsg
		switch k {
		case "enter":
			msg = tea.KeyPressMsg{Code: tea.KeyEnter}
		case "tab":
			msg = tea.KeyPressMsg{Code: tea.KeyTab}
		case "shift+tab":
			msg = tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift}
		case "ctrl+r":
			msg = tea.KeyPressMsg{Code: 'r', Mod: tea.ModCtrl}
		case "ctrl+l":
			msg = tea.KeyPressMsg{Code: 'l', Mod: tea.ModCtrl}
		case "esc":
			msg = tea.KeyPressMsg{Code: tea.KeyEscape}
		case "up":
			msg = tea.KeyPressMsg{Code: tea.KeyUp}
		case "down":
			msg = tea.KeyPressMsg{Code: tea.KeyDown}
		case "right":
			msg = tea.KeyPressMsg{Code: tea.KeyRight}
		default:
			msg = tea.KeyPressMsg{Code: rune(k[0]), Text: k}
		}
		var cmd tea.Cmd
		m, cmd = m.Update(msg)
		// Only run commands for keys that execute statements; the textarea's
		// cursor-blink tick would otherwise sleep on every keystroke.
		if cmd != nil && runKeys[k] {
			m = runCmd(m, cmd, 0)
		}
	}
	return m
}

// typeText types characters without running any returned command (typing
// never executes a statement; only the keys in runKeys do).
func typeText(m tea.Model, s string) tea.Model {
	for _, r := range s {
		m, _ = m.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	return m
}

func TestQueryFilterHistory(t *testing.T) {
	var m tea.Model = New(plan.Session{}, stubRunner(t), nil, nil)
	m, _ = m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	mm0 := m.(Model)
	mm0.chooserOpen, mm0.focus = false, focusCmd
	m = mm0
	m = typeText(m, "SELECT Slug, Space.Slug, HeadRevisionNum FROM Unit IN *")
	m = press(m, "enter")
	v := m.View().Content
	if !strings.Contains(v, "NAME") || !strings.Contains(v, "backend") || !strings.Contains(v, "2 rows") {
		t.Fatalf("results not rendered:\n%s", v)
	}
	// Focus results, add a chip on the NAME column, expect the statement rewritten and re-run.
	m = press(m, "shift+tab", "f")
	mm := m.(Model)
	if !strings.Contains(mm.cmd.Value(), "| where Slug = 'backend'") {
		t.Errorf("chip rewrite: %q", mm.cmd.Value())
	}
	if !strings.Contains(m.View().Content, "[Slug = 'backend']") {
		t.Errorf("chip not shown")
	}
	// Order by the focused column, then remove the chip.
	m = press(m, "o", "-")
	mm = m.(Model)
	if strings.Contains(mm.cmd.Value(), "where") || !strings.Contains(mm.cmd.Value(), "| order by Slug") {
		t.Errorf("after order+unchip: %q", mm.cmd.Value())
	}
	// Detail, then back.
	m = press(m, "enter")
	if !strings.Contains(m.View().Content, "HeadRevisionNum: 17") && !strings.Contains(m.View().Content, "HeadRevisionNum: 3") {
		t.Errorf("detail not shown:\n%s", m.View().Content)
	}
	m = press(m, "esc", "esc")
	// Pivot: revisions of the selected unit.
	m = press(m, "shift+tab", "r")
	mm = m.(Model)
	if !strings.HasPrefix(mm.cmd.Value(), "Revision | in *") || !strings.Contains(mm.cmd.Value(), "UnitID = 'u") {
		t.Errorf("pivot: %q", mm.cmd.Value())
	}
	// Incomplete statement: Enter inserts a newline instead of erroring.
	m = press(m, "esc")
	mm = m.(Model)
	mm.cmd.Reset()
	m = typeText(mm, "SELECT Slug FROM Unit WHERE")
	m = press(m, "enter")
	mm = m.(Model)
	if !strings.Contains(mm.cmd.Value(), "\n") || mm.statusErr {
		t.Errorf("incomplete handling: %q err=%v %s", mm.cmd.Value(), mm.statusErr, mm.status)
	}
	// OR in a SQL WHERE is a parse error with the hint.
	mm.cmd.Reset()
	m = typeText(mm, "SELECT * FROM Unit WHERE Slug = 'a' OR Slug = 'b'")
	m = press(m, "enter")
	mm = m.(Model)
	if !mm.statusErr || !strings.Contains(mm.status, "HAVING") {
		t.Errorf("OR hint: %q", mm.status)
	}
	// In a pipeline the same step simply runs locally and its chip says so.
	mm.cmd.Reset()
	mm.statusErr = false
	m = typeText(mm, "Unit | where Slug = 'backend' OR Slug = 'frontend'")
	m = press(m, "enter")
	mm = m.(Model)
	if mm.statusErr || mm.plan == nil || len(mm.plan.Pushed) != 1 || mm.plan.Pushed[0] {
		t.Errorf("pipeline OR: err=%v status=%q plan=%+v", mm.statusErr, mm.status, mm.plan)
	}
	if !strings.Contains(m.View().Content, "evaluated locally") {
		t.Errorf("local chip hint missing")
	}
	// History drawer opens and renders.
	m = press(m, "ctrl+r")
	if !strings.Contains(m.View().Content, "History") {
		t.Errorf("drawer missing")
	}
}

func planSession() plan.Session { return plan.Session{} }
