package tui

import (
	"testing"

	tea "charm.land/bubbletea/v2"
)

// Regression: a result with fewer columns than the previous one must not
// re-render the old rows against the new columns.
func TestNarrowerResultAfterWider(t *testing.T) {
	var m tea.Model = New(planSession(), stubRunner(t), nil, nil)
	m, _ = m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	mm0 := m.(Model)
	mm0.chooserOpen, mm0.focus = false, focusCmd
	m = mm0
	m = typeText(m, "Unit | columns Slug, Space.Slug, HeadRevisionNum, Labels.Tier, UnitID")
	m = press(m, "enter")
	_ = m.View()
	m = press(m, "ctrl+l")
	m = typeText(m, "Unit | columns Slug")
	m = press(m, "enter")
	_ = m.View()
}
