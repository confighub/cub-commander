package tui

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/confighub/cub-commander/internal/cubclient"
)

// Enter on a unit in the browse Units pane, then 2 / → / 1 must switch tabs.
func TestDetailFromBrowseSwitchesTabs(t *testing.T) {
	var m tea.Model = New(planSession(), stubRunner(t), nil, nil)
	m, _ = m.Update(tea.WindowSizeMsg{Width: 140, Height: 40})
	mm := m.(Model)
	mm.chooserOpen, mm.focus = false, focusCmd
	mm.dataLoader = func(ctx context.Context, row cubclient.Row) (string, string, error) { return "a: 1\n", "h", nil }
	m = typeText(mm, "Unit | in * | browse by Space.Slug")
	m = press(m, "enter")
	mm = m.(Model)
	mm.browse.pane = 1 // Units pane
	m = mm
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	mm = m.(Model)
	if mm.mode != modeDetail || mm.focus != focusMain {
		t.Fatalf("mode=%v focus=%v", mm.mode, mm.focus)
	}
	v := m.View().Content
	if !strings.Contains(v, "2 Data") {
		t.Fatalf("no Data tab:\n%s", v)
	}
	for _, k := range []tea.KeyPressMsg{{Code: '2', Text: "2"}, {Code: tea.KeyRight}} {
		mm = m.(Model)
		mm.det.tab = 0
		var cmd tea.Cmd
		m, cmd = mm.Update(k)
		if m.(Model).det.tab != 1 {
			t.Errorf("key %s did not switch to Data", k.String())
		}
		if cmd != nil {
			m, _ = m.Update(cmd())
		}
	}
	if !strings.Contains(m.View().Content, "a: 1") {
		t.Errorf("data not shown:\n%s", m.View().Content)
	}
	m, _ = m.Update(tea.KeyPressMsg{Code: '1', Text: "1"})
	if m.(Model).det.tab != 0 {
		t.Errorf("1 did not switch back")
	}
}

// Enter on a space opens metadata only; u pivots to the units in it.
func TestSpaceDetailPivotsToUnits(t *testing.T) {
	var m tea.Model = New(planSession(), stubRunner(t), nil, nil)
	m, _ = m.Update(tea.WindowSizeMsg{Width: 140, Height: 40})
	mm := m.(Model)
	mm.chooserOpen, mm.focus = false, focusCmd
	m = typeText(mm, "Space | in *")
	m = press(m, "enter", "shift+tab", "enter")
	mm = m.(Model)
	if mm.mode != modeDetail || mm.det.entity != "Space" {
		t.Fatalf("mode=%v entity=%q", mm.mode, mm.det.entity)
	}
	if v := m.View().Content; !strings.Contains(v, "metadata only") || strings.Contains(v, "2 Data") {
		t.Errorf("space header:\n%s", v)
	}
	m, _ = m.Update(tea.KeyPressMsg{Code: '2', Text: "2"})
	if !strings.Contains(m.(Model).status, "only Metadata") {
		t.Errorf("status: %q", m.(Model).status)
	}
	mm = m.(Model)
	mm.det.row = cubclient.Row{"Space": map[string]any{"Slug": "prod-eu", "SpaceID": "s1"}}
	m, cmd := mm.Update(tea.KeyPressMsg{Code: 'u', Text: "u"})
	if !strings.HasPrefix(m.(Model).cmd.Value(), "Unit | in prod-eu") {
		t.Errorf("pivot: %q", m.(Model).cmd.Value())
	}
	_ = cmd
}

func TestRevisionPickerDiff(t *testing.T) {
	var m tea.Model = New(planSession(), stubRunner(t), nil, nil)
	m, _ = m.Update(tea.WindowSizeMsg{Width: 140, Height: 40})
	mm := m.(Model)
	mm.chooserOpen, mm.focus = false, focusCmd
	mm.dataLoader = func(ctx context.Context, row cubclient.Row) (string, string, error) { return "replicas: 3\n", "h3", nil }
	mm.revLoader = func(ctx context.Context, row cubclient.Row) ([]cubclient.Row, error) {
		mk := func(n float64, id, desc string) cubclient.Row {
			return cubclient.Row{"Revision": map[string]any{"RevisionNum": n, "RevisionID": id, "CreatedAt": "2026-09-01T10:00:00Z", "Description": desc, "Source": "UpdateUnit"}, "User": map[string]any{"Username": "me"}}
		}
		return []cubclient.Row{mk(1, "r1", "first"), mk(3, "r3", "head"), mk(2, "r2", "second")}, nil
	}
	mm.revDataLoader = func(ctx context.Context, row cubclient.Row, id string) (string, error) {
		return map[string]string{"r1": "replicas: 1\n", "r2": "replicas: 2\n", "r3": "replicas: 3\n"}[id], nil
	}
	m = typeText(mm, "Unit | in *")
	m = press(m, "enter", "shift+tab", "enter")
	var cmd tea.Cmd
	m, cmd = m.Update(tea.KeyPressMsg{Code: '2', Text: "2"})
	m, _ = m.Update(cmd())
	// d opens the picker, sorted newest first, cursor on the revision before head.
	m, cmd = m.Update(tea.KeyPressMsg{Code: 'd', Text: "d"})
	m, _ = m.Update(cmd())
	mm = m.(Model)
	p := mm.det.picker
	if p == nil || len(p.rows) != 3 || revNum(p.rows[0]) != 3 || p.cur != 1 {
		t.Fatalf("picker: %+v", p)
	}
	if v := m.View().Content; !strings.Contains(v, "Revisions") || !strings.Contains(v, "second") {
		t.Errorf("picker view:\n%s", v)
	}
	// Enter diffs revision 2 against the current data.
	m, cmd = mm.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m, _ = m.Update(cmd())
	mm = m.(Model)
	if mm.mode != modeText || !strings.Contains(mm.textTitle, "revision 2 → current") {
		t.Fatalf("diff: mode=%v title=%q status=%q", mm.mode, mm.textTitle, mm.status)
	}
	if v := m.View().Content; !strings.Contains(v, "-replicas: 2") || !strings.Contains(v, "+replicas: 3") {
		t.Errorf("diff view:\n%s", v)
	}
	// Esc returns to the picker (still open); m marks rev 1, Enter on rev 3 diffs 1 → 3.
	m = press(m, "esc")
	if mm = m.(Model); mm.mode != modeDetail || mm.det.picker == nil {
		t.Fatalf("esc from diff should return to the picker: mode=%v picker=%v", mm.mode, mm.det.picker != nil)
	}
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyDown}) // rev 1
	m, _ = m.Update(tea.KeyPressMsg{Code: 'm', Text: "m"})
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyUp}) // rev 3
	m, cmd = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m, _ = m.Update(cmd())
	mm = m.(Model)
	if !strings.Contains(mm.textTitle, "revision 1 → 3") || !strings.Contains(m.View().Content, "-replicas: 1") {
		t.Errorf("two-revision diff: %q\n%s", mm.textTitle, m.View().Content)
	}
}

// d from the Metadata tab, Esc, leave and re-enter: the picker must not linger.
func TestPickerFromMetadataAndEsc(t *testing.T) {
	var m tea.Model = New(planSession(), stubRunner(t), nil, nil)
	m, _ = m.Update(tea.WindowSizeMsg{Width: 140, Height: 40})
	mm := m.(Model)
	mm.chooserOpen, mm.focus = false, focusCmd
	mm.dataLoader = func(ctx context.Context, row cubclient.Row) (string, string, error) { return "replicas: 3\n", "h3", nil }
	mm.revLoader = func(ctx context.Context, row cubclient.Row) ([]cubclient.Row, error) {
		return []cubclient.Row{{"Revision": map[string]any{"RevisionNum": 2.0, "RevisionID": "r2"}}, {"Revision": map[string]any{"RevisionNum": 1.0, "RevisionID": "r1"}}}, nil
	}
	mm.revDataLoader = func(ctx context.Context, row cubclient.Row, id string) (string, error) { return "replicas: 1\n", nil }
	m = typeText(mm, "Unit | in *")
	m = press(m, "enter", "shift+tab", "enter")
	// d on Metadata: moves to Data, loads it, opens the picker.
	var cmd tea.Cmd
	m, cmd = m.Update(tea.KeyPressMsg{Code: 'd', Text: "d"})
	if b, ok := cmd().(tea.BatchMsg); ok {
		for _, c := range b {
			if c != nil {
				m, _ = m.Update(c())
			}
		}
	}
	mm = m.(Model)
	if mm.det.tab != 1 || mm.det.picker == nil || len(mm.det.picker.rows) != 2 {
		t.Fatalf("picker from metadata: tab=%d picker=%+v", mm.det.tab, mm.det.picker)
	}
	// Enter diffs even if the head had not been loaded before d.
	mm.det.loaded = false
	m, cmd = mm.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m, _ = m.Update(cmd())
	if mm = m.(Model); mm.mode != modeText || !strings.Contains(m.View().Content, "+replicas: 3") {
		t.Errorf("diff without preloaded head: mode=%v status=%q", mm.mode, mm.status)
	}
	m = press(m, "esc") // back to the picker
	// Esc closes the picker rather than the view; a second Esc leaves the view.
	m = press(m, "esc")
	if mm = m.(Model); mm.mode != modeDetail || mm.det.picker != nil {
		t.Fatalf("esc should close the picker: mode=%v picker=%v", mm.mode, mm.det.picker != nil)
	}
	m = press(m, "esc")
	if m.(Model).mode != modeResults {
		t.Fatalf("second esc should leave detail")
	}
	// Re-enter the same unit: tabs switch normally.
	m = press(m, "enter")
	m, _ = m.Update(tea.KeyPressMsg{Code: '1', Text: "1"})
	m, _ = m.Update(tea.KeyPressMsg{Code: '2', Text: "2"})
	if mm = m.(Model); mm.det.tab != 1 || mm.det.picker != nil {
		t.Errorf("after re-enter: tab=%d picker=%v", mm.det.tab, mm.det.picker != nil)
	}
}
