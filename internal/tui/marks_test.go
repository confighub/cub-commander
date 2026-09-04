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

func stubSpaces(t *testing.T) Runner {
	mk := func(slug, comp, variant string) cubclient.Row {
		return cubclient.Row{"Space": map[string]any{"Slug": slug, "SpaceID": "s-" + slug, "Labels": map[string]any{"Component": comp, "Variant": variant}}}
	}
	rows := []cubclient.Row{mk("argobot-base", "argobot", "base"), mk("argobot-nonprod", "argobot", "nonprod"), mk("argobot-prod", "argobot", "prod"), mk("docs-prod", "docs", "prod")}
	return func(ctx context.Context, st lang.Stmt, sess plan.Session) (tea.Msg, error) {
		sel := st.(*lang.SelectStmt)
		p, err := plan.Compile(sel, sess)
		if err != nil {
			return nil, err
		}
		if p.Diff != nil {
			return diffMsg{stmt: sel, plan: p, res: &exec.DiffResult{Counts: map[string]int{}}}, nil
		}
		res, err := exec.Local(p, rows)
		if err != nil {
			return nil, err
		}
		return resultMsg{stmt: sel, plan: p, res: res}, nil
	}
}

func TestMarkTwoVariantsThenDiff(t *testing.T) {
	var m tea.Model = New(planSession(), stubSpaces(t), nil, nil)
	m, _ = m.Update(tea.WindowSizeMsg{Width: 160, Height: 40})
	mm := m.(Model)
	mm.chooserOpen, mm.focus = false, focusCmd
	m = typeText(mm, "Space | in * | browse by Labels.Component, Labels.Variant")
	m = press(m, "enter")
	mm = m.(Model)
	if mm.mode != modeBrowse {
		t.Fatalf("mode %v %q", mm.mode, mm.status)
	}
	// Component pane: argobot is first. Move into Variant: base, nonprod, prod.
	m = press(m, "right", "down") // nonprod
	m, _ = m.Update(tea.KeyPressMsg{Code: 'm', Text: "m"})
	m = press(m, "down") // prod
	m, _ = m.Update(tea.KeyPressMsg{Code: 'm', Text: "m"})
	mm = m.(Model)
	if len(mm.marks) != 2 || mm.marks[0].label == mm.marks[1].label {
		t.Fatalf("marks: %+v", mm.marks)
	}
	m, _ = mm.Update(tea.KeyPressMsg{Code: 'd', Text: "d"})
	mm = m.(Model)
	want := "Space | in *\n| where Labels.Component = 'argobot'\n| diff Labels.Variant = 'nonprod' vs Labels.Variant = 'prod'"
	if mm.cmd.Value() != want {
		t.Fatalf("diff statement:\n%q\nstatus %q", mm.cmd.Value(), mm.status)
	}
	if strings.Contains(mm.status, "same selection") {
		t.Fatalf("false 'same selection': %q", mm.status)
	}
}

func TestMarkSetSemantics(t *testing.T) {
	var m tea.Model = New(planSession(), stubSpaces(t), nil, nil)
	m, _ = m.Update(tea.WindowSizeMsg{Width: 160, Height: 40})
	mm := m.(Model)
	mm.chooserOpen, mm.focus = false, focusCmd
	m = typeText(mm, "Space | in * | browse by Labels.Component, Labels.Variant")
	m = press(m, "enter", "right", "down")
	key := func(k string) { m, _ = m.Update(tea.KeyPressMsg{Code: rune(k[0]), Text: k}) }
	key("m") // A = nonprod
	key("m") // unmark
	if len(m.(Model).marks) != 0 {
		t.Fatalf("toggle off failed: %+v", m.(Model).marks)
	}
	key("m") // A = nonprod again
	key("d") // still on it: an explanation, not a diff
	if st := m.(Model).status; !strings.Contains(st, "still on it") {
		t.Errorf("d on the mark: %q", st)
	}
	m = press(m, "down") // prod
	key("m")             // B = prod
	m = press(m, "up")   // base? no: up from prod is nonprod… go to base
	m = press(m, "up")
	key("m") // third mark replaces B with base
	mm = m.(Model)
	if len(mm.marks) != 2 || !strings.Contains(mm.marks[1].label, "'base'") || !strings.Contains(mm.status, "B replaced") {
		t.Errorf("replace: %+v %q", mm.marks, mm.status)
	}
	key("d")
	if v := m.(Model).cmd.Value(); !strings.Contains(v, "diff Labels.Variant = 'nonprod' vs Labels.Variant = 'base'") {
		t.Errorf("diff after replace: %q", v)
	}
}
