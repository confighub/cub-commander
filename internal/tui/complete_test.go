package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/confighub/cub-commander/internal/catalog"
)

func texts(c []Candidate) string {
	var out []string
	for _, x := range c {
		out = append(out, x.Text)
	}
	return strings.Join(out, " ")
}

func has(c []Candidate, want string) bool {
	for _, x := range c {
		if x.Text == want {
			return true
		}
	}
	return false
}

func TestComplete(t *testing.T) {
	live := catalog.NewLive()
	c := &Completer{Live: live}
	cases := []struct {
		text string
		want []string
		none []string
	}{
		{"", []string{"SELECT", "Unit", "Space"}, nil},
		{"SEL", []string{"SELECT"}, []string{"Unit"}},
		{"SELECT * FROM ", []string{"Unit", "Space", "Revision"}, nil},
		{"SELECT * FROM Un", []string{"Unit", "UnitEvent"}, []string{"Space"}},
		{"SELECT * FROM Unit ", []string{"IN", "WHERE", "HAVING", "ORDER BY", "LIMIT"}, nil},
		{"SELECT * FROM Unit IN * ", []string{"WHERE", "LIMIT"}, []string{"IN"}},
		{"SELECT * FROM Unit IN ", []string{"*"}, nil},
		{"SELECT * FROM Unit WHERE ", []string{"Slug", "Labels.", "Space.", "UpstreamUnit.", "FromLink.*.", "LEN("}, nil},
		{"SELECT * FROM Unit WHERE Head", []string{"HeadRevisionNum", "HeadMutationNum"}, []string{"Slug"}},
		{"SELECT * FROM Unit WHERE Space.", []string{"Slug", "Labels."}, []string{"HeadRevisionNum"}},
		{"SELECT * FROM Unit WHERE Space.Lab", []string{"Labels."}, nil},
		{"SELECT * FROM Unit WHERE HeadRevisionNum ", []string{"=", ">", "IN ("}, []string{"LIKE"}},
		{"SELECT * FROM Unit WHERE Slug ", []string{"=", "LIKE", "~", "IS NULL"}, nil},
		{"SELECT * FROM Unit WHERE TargetID ", []string{"=", "IS NOT NULL"}, []string{"LIKE", ">"}},
		{"SELECT * FROM Unit WHERE Slug = 'x' ", []string{"AND", "HAVING", "ORDER BY"}, []string{"WHERE"}},
		{"SELECT * FROM Unit WHERE Slug = 'x' AND ", []string{"Slug", "Space."}, nil},
		{"SELECT Slug, ", []string{"Slug", "Space."}, nil},
		{"SELECT Slug ", []string{"FROM", "AS"}, nil},
		{"SELECT * FROM Link WHERE UpdateType = '", []string{"UpgradeUnit", "NeedsProvides"}, nil},
		{"SELECT * FROM Unit ORDER BY UpdatedAt ", []string{"DESC", "LIMIT"}, nil},
		{"SHOW ", []string{"ENTITIES", "COLUMNS FROM"}, nil},
		{"Unit ", []string{"| where", "| in", "| columns"}, nil},
		{"Unit | ", []string{"where", "columns", "order by"}, nil},
		{"Unit | in ", []string{"*"}, nil},
		{"Unit | in * ", []string{"| where"}, []string{"| in"}},
		{"Unit | where ", []string{"Slug", "Space.", "Labels."}, nil},
		{"Unit | where Slug ", []string{"=", "LIKE"}, nil},
		{"Unit | where Slug = 'x' ", []string{"AND", "OR", "| columns", "| order by"}, nil},
		{"Unit | columns Slug ", []string{",", "as", "| where"}, []string{"| columns"}},
		{"Unit | columns Slug, Sp", []string{"Space."}, nil},
		{"Unit | order by UpdatedAt ", []string{"desc", "| limit"}, nil},
	}
	for _, tc := range cases {
		_, got := c.Complete(tc.text)
		for _, w := range tc.want {
			if !has(got, w) {
				t.Errorf("%q: want %q in [%s]", tc.text, w, texts(got))
			}
		}
		for _, n := range tc.none {
			if has(got, n) {
				t.Errorf("%q: did not want %q in [%s]", tc.text, n, texts(got))
			}
		}
	}
	// Segment start: completing the last dotted segment only.
	start, _ := c.Complete("SELECT * FROM Unit WHERE Space.Sl")
	if start != len("SELECT * FROM Unit WHERE Space.") {
		t.Errorf("start=%d", start)
	}
	if cp := commonPrefix([]Candidate{{Text: "HeadRevisionNum"}, {Text: "HeadMutationNum"}}); cp != "Head" {
		t.Errorf("commonPrefix=%q", cp)
	}
}

func TestCompleteInModel(t *testing.T) {
	m := New(planSession(), stubRunner(t), nil, nil)
	m.chooserOpen, m.focus = false, focusCmd
	m.width, m.height = 100, 30
	m.layout()
	m.cmd.SetValue("SELECT * FROM Unit WHERE HeadRevisionN")
	m.cmd.MoveToEnd()
	m.complete()
	if m.cmd.Value() != "SELECT * FROM Unit WHERE HeadRevisionNum" || m.popupOpen {
		t.Errorf("single candidate: %q open=%v", m.cmd.Value(), m.popupOpen)
	}
	m.cmd.SetValue("SELECT * FROM Unit WHERE Head")
	m.cmd.MoveToEnd()
	m.complete()
	if !m.popupOpen || len(m.popup) < 2 {
		t.Fatalf("popup: open=%v n=%d", m.popupOpen, len(m.popup))
	}
	m.popupSel = 1
	m.applyCandidate(true)
	if !strings.HasPrefix(m.cmd.Value(), "SELECT * FROM Unit WHERE Head") || !strings.HasSuffix(m.cmd.Value(), "Num") {
		t.Errorf("apply: %q", m.cmd.Value())
	}
	if !strings.Contains(m.View().Content, m.popup[1].Text) {
		t.Errorf("popup not rendered")
	}
	// Esc closes the popup and leaves the text alone.
	m.popupOpen = true
	before := m.cmd.Value()
	var mm tea.Model
	mm, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	m = mm.(Model)
	if m.popupOpen || m.cmd.Value() != before || m.focus != focusCmd {
		t.Errorf("esc: open=%v value=%q focus=%v", m.popupOpen, m.cmd.Value(), m.focus)
	}
}
