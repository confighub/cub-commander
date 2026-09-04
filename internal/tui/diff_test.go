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

// stubDiff answers any statement with two stub sides, and pairs them.
func stubDiff(t *testing.T) Runner {
	lab := func(env, cluster string) map[string]any {
		return map[string]any{"Component": "cart", "Environment": env, "Cluster": cluster}
	}
	mk := func(slug, space, hash string, labels map[string]any) cubclient.Row {
		return cubclient.Row{"Unit": map[string]any{"Slug": slug, "UnitID": space + "/" + slug, "SpaceID": "s", "DataHash": hash, "Labels": labels}, "Space": map[string]any{"Slug": space, "SpaceID": "s", "Labels": map[string]any{"Environment": env(labels)}}}
	}
	rows := []cubclient.Row{
		mk("api", "cart-dev1", "h1", lab("dev", "dev1")),
		mk("cache", "cart-dev1", "h2", lab("dev", "dev1")),
		mk("api", "cart-prod1", "h3", lab("prod", "prod1")),
		mk("cache", "cart-prod1", "h2", lab("prod", "prod1")),
	}
	return func(ctx context.Context, st lang.Stmt, sess plan.Session) (tea.Msg, error) {
		sel := st.(*lang.SelectStmt)
		p, err := plan.Compile(sel, sess)
		if err != nil {
			return nil, err
		}
		if p.Diff == nil {
			res, err := exec.Local(p, rows)
			if err != nil {
				return nil, err
			}
			return resultMsg{stmt: sel, plan: p, res: res}, nil
		}
		side := func(e lang.Expr) []cubclient.Row {
			var out []cubclient.Row
			q := &plan.Plan{Entity: p.Entity, List: &plan.ListStage{}, Columns: []plan.Col{{Header: "x", Expr: lang.Ref{Path: "Slug"}}}, Local: []plan.LocalStage{{Kind: "where", Expr: e}}}
			r, _ := exec.Local(q, rows)
			out = append(out, r.Raw...)
			return out
		}
		res := exec.PairRows("Unit", p.Diff.By, side(p.Diff.AExpr), side(p.Diff.BExpr))
		return diffMsg{stmt: sel, plan: p, res: res}, nil
	}
}

func env(l map[string]any) string { s, _ := l["Environment"].(string); return s }

func TestMarksToDiff(t *testing.T) {
	var m tea.Model = New(planSession(), stubDiff(t), nil, nil)
	m, _ = m.Update(tea.WindowSizeMsg{Width: 160, Height: 40})
	mm := m.(Model)
	mm.chooserOpen, mm.focus = false, focusCmd
	mm.dataFetcher = func(ctx context.Context, row cubclient.Row) (string, error) {
		if strings.Contains(unitID(row), "dev") {
			return "replicas: 1\nimage: v1\n", nil
		}
		return "replicas: 3\nimage: v1\n", nil
	}
	m = typeText(mm, "Unit | in * | browse by Labels.Component, Labels.Environment")
	m = press(m, "enter")
	mm = m.(Model)
	if mm.mode != modeBrowse {
		t.Fatalf("mode %v status %q", mm.mode, mm.status)
	}
	// Select cart → dev, mark A; move to prod, mark B; d.
	m = press(m, "right")
	m, _ = m.Update(tea.KeyPressMsg{Code: 'm', Text: "m"})
	m = press(m, "down")
	m, _ = m.Update(tea.KeyPressMsg{Code: 'm', Text: "m"})
	mm = m.(Model)
	if len(mm.marks) != 2 || !strings.Contains(m.View().Content, "A: Labels.Component = 'cart' AND Labels.Environment = 'dev'") {
		t.Fatalf("marks: %+v\n%s", mm.marks, m.View().Content)
	}
	var cmd tea.Cmd
	m, cmd = mm.Update(tea.KeyPressMsg{Code: 'd', Text: "d"})
	mm = m.(Model)
	want := "Unit | in *\n| where Labels.Component = 'cart'\n| diff Labels.Environment = 'dev' vs Labels.Environment = 'prod'"
	if mm.cmd.Value() != want {
		t.Fatalf("diff statement:\n%q\nwant\n%q", mm.cmd.Value(), want)
	}
	// Run it and the data fetches it schedules (a Batch holds several).
	var run func(c tea.Cmd, depth int)
	run = func(c tea.Cmd, depth int) {
		if c == nil || depth > 6 {
			return
		}
		msg := c()
		if b, ok := msg.(tea.BatchMsg); ok {
			for _, sub := range b {
				run(sub, depth+1)
			}
			return
		}
		if msg == nil {
			return
		}
		var next tea.Cmd
		m, next = m.Update(msg)
		run(next, depth+1)
	}
	run(cmd, 0)
	mm = m.(Model)
	if mm.mode != modeDiff || mm.diff == nil || len(mm.diff.res.Pairs) != 2 {
		t.Fatalf("diff mode: mode=%v status=%q", mm.mode, mm.status)
	}
	v := m.View().Content
	if !strings.Contains(v, "Pairs (2)") || !strings.Contains(v, "api / cart") || !strings.Contains(mm.status, "1 differ, 1 same") {
		t.Errorf("diff view:\n%s\n%s", v, mm.status)
	}
	// The data for the differing pair (api) was fetched and diffed.
	if !strings.Contains(v, "-replicas: 1") || !strings.Contains(v, "+replicas: 3") {
		t.Errorf("unified diff missing:\n%s", v)
	}
	// = hides the identical pair; Esc returns to browse.
	m, _ = mm.Update(tea.KeyPressMsg{Code: '=', Text: "="})
	if !strings.Contains(m.View().Content, "Pairs (1)") {
		t.Errorf("filter")
	}
	m = press(m, "esc")
	if m.(Model).mode != modeBrowse {
		t.Errorf("esc should return to browse")
	}
}

func TestDiffStatementParsesAndPrints(t *testing.T) {
	st, err := lang.ParseOne("Unit | in * | where Labels.Component = 'cart' | diff Labels.Cluster = 'a' vs Labels.Cluster = 'b' by Slug, Labels.Region")
	if err != nil {
		t.Fatal(err)
	}
	s := st.(*lang.SelectStmt)
	if s.Diff == nil || len(s.Diff.By) != 2 {
		t.Fatalf("diff step: %+v", s.Diff)
	}
	if got := lang.StmtString(s); !strings.HasSuffix(got, "| diff Labels.Cluster = 'a' vs Labels.Cluster = 'b' by Slug, Labels.Region") {
		t.Errorf("print: %q", got)
	}
	p, err := plan.Compile(s, plan.Session{})
	if err != nil {
		t.Fatal(err)
	}
	if p.Diff == nil || p.Diff.A.Where != "Labels.Component = 'cart' AND Labels.Cluster = 'a'" || !strings.Contains(p.Explain(""), "side B") {
		t.Errorf("plan: %+v", p.Diff)
	}
}
