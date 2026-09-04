package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

// A path that ends in Resource shows the union pane from the start.
func TestHopFromStatement(t *testing.T) {
	var m tea.Model = New(planSession(), stubRunner(t), nil, nil)
	m, _ = m.Update(tea.WindowSizeMsg{Width: 160, Height: 40})
	mm := m.(Model)
	mm.chooserOpen, mm.focus = false, focusCmd
	mm.fetcher = fakeResources(t)
	m = typeText(mm, "Unit | in * | browse by Space.Slug, Resource")
	// run the statement and then the fetch it schedules
	var cmd tea.Cmd
	m, cmd = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	for i := 0; i < 3 && cmd != nil; i++ {
		msg := cmd()
		if msg == nil {
			break
		}
		m, cmd = m.Update(msg)
	}
	mm = m.(Model)
	if mm.browse == nil || !mm.browse.hop || mm.browse.pane != 0 {
		t.Fatalf("browse: %+v", mm.browse)
	}
	if v := m.View().Content; !strings.Contains(v, "Resources (2)") {
		t.Errorf("union at start:\n%s", v)
	}
}
