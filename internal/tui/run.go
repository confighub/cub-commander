package tui

import (
	"context"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/confighub/cub-commander/internal/catalog"
	"github.com/confighub/cub-commander/internal/cubclient"
	"github.com/confighub/cub-commander/internal/exec"
	"github.com/confighub/cub-commander/internal/history"
	"github.com/confighub/cub-commander/internal/lang"
	"github.com/confighub/cub-commander/internal/plan"
)

// Run opens the application against the ConfigHub server cub pointed us at.
func Run(sess plan.Session) error {
	client, err := cubclient.New()
	if err != nil {
		return err
	}
	hist, err := history.Open("")
	if err != nil {
		return fmt.Errorf("history: %w", err)
	}
	live := catalog.NewLive()
	m := New(sess, DefaultRunner(client, live), func(ctx context.Context, l *catalog.Live) error { return l.Sample(ctx, client) }, hist)
	m.fetcher = client.List
	m.dataFetcher = func(ctx context.Context, row cubclient.Row) (string, error) { return exec.UnitData(ctx, client, row) }
	_ = live
	_, err = tea.NewProgram(m).Run()
	return err
}

// DefaultRunner executes statements against a client.
func DefaultRunner(c *cubclient.Client, live *catalog.Live) Runner {
	return func(ctx context.Context, st lang.Stmt, sess plan.Session) (tea.Msg, error) {
		switch x := st.(type) {
		case *lang.SelectStmt:
			p, err := plan.Compile(x, sess)
			if err != nil {
				return nil, err
			}
			if p.Diff != nil {
				res, err := exec.RunDiff(ctx, c, p)
				if err != nil {
					return nil, err
				}
				return diffMsg{stmt: x, plan: p, res: res}, nil
			}
			res, err := exec.Run(ctx, c, p)
			if err != nil {
				return nil, err
			}
			return resultMsg{stmt: x, plan: p, res: res}, nil
		case *lang.ExplainStmt:
			sel, ok := x.Inner.(*lang.SelectStmt)
			if !ok {
				return nil, fmt.Errorf("EXPLAIN only explains SELECT")
			}
			p, err := plan.Compile(sel, sess)
			if err != nil {
				return nil, err
			}
			id := ""
			if p.List.Space != "" {
				id, _ = c.SpaceID(ctx, p.List.Space)
			}
			return textMsg{title: "EXPLAIN", body: p.Explain(id)}, nil
		case *lang.ShowStmt:
			return Show(x, live)
		case *lang.DescribeStmt:
			body, err := Describe(x.Name)
			if err != nil {
				return nil, err
			}
			return textMsg{title: "DESCRIBE " + x.Name, body: body}, nil
		}
		return nil, fmt.Errorf("unsupported statement")
	}
}

// Show renders a SHOW statement as a result.
func Show(s *lang.ShowStmt, live *catalog.Live) (tea.Msg, error) {
	res, err := ShowResult(s, live)
	if err != nil {
		return nil, err
	}
	st := &lang.SelectStmt{Star: true, From: lang.Source{Entity: "Show"}}
	res.Columns = cols(res.Headers)
	return resultMsg{stmt: st, plan: &plan.Plan{Columns: res.Columns}, res: res}, nil
}

// ShowResult builds the table behind a SHOW statement. Catalog-only forms
// need no server; LABELS, VALUES and SPACES read the live sample.
func ShowResult(s *lang.ShowStmt, live *catalog.Live) (*exec.Result, error) {
	res := &exec.Result{}
	switch s.What {
	case "COLUMNS":
		ent := s.Arg
		if ent == "" {
			ent = "Unit"
		}
		e, ok := catalog.Lookup(ent)
		if !ok {
			return nil, fmt.Errorf("unknown entity %q", ent)
		}
		res.Headers = []string{"COLUMN", "TYPE", "FILTERABLE", "OPERATORS", "DESCRIPTION"}
		for _, a := range catalog.Attributes(e.Name) {
			ops := ""
			if a.Filterable {
				ops = strings.Join(catalog.Operators(a.Type), " ")
			}
			t := a.Type
			if len(a.Enum) > 0 {
				t = "enum(" + strings.Join(a.Enum, "|") + ")"
			}
			res.Rows = append(res.Rows, []any{a.Name, t, a.Filterable, ops, a.Desc})
		}
	case "LABELS":
		ent := s.Arg
		if ent == "" {
			ent = "Unit"
		}
		e, ok := catalog.Lookup(ent)
		if !ok {
			return nil, fmt.Errorf("unknown entity %q", ent)
		}
		res.Headers = []string{"LABEL", "COUNT", "VALUES"}
		for _, k := range live.LabelKeys(e.Name) {
			vals := live.LabelValues(e.Name, k.Name)
			names := make([]string, 0, len(vals))
			for i, v := range vals {
				if i == 8 {
					names = append(names, "…")
					break
				}
				names = append(names, v.Name)
			}
			res.Rows = append(res.Rows, []any{k.Name, float64(k.N), strings.Join(names, ", ")})
		}
	case "VALUES":
		path := strings.TrimPrefix(s.Arg, "Labels.")
		ent := "Unit"
		if strings.HasPrefix(s.Arg, "Space.") {
			ent, path = "Space", strings.TrimPrefix(strings.TrimPrefix(s.Arg, "Space."), "Labels.")
		}
		res.Headers = []string{"VALUE", "COUNT"}
		for _, v := range live.LabelValues(ent, path) {
			res.Rows = append(res.Rows, []any{v.Name, float64(v.N)})
		}
	case "SPACES":
		res.Headers = []string{"SPACE"}
		for _, sp := range live.Spaces() {
			res.Rows = append(res.Rows, []any{sp})
		}
	case "ENTITIES":
		res.Headers = []string{"ENTITY", "CUB", "SPACE-SCOPED", "JOINS"}
		for _, e := range catalog.All() {
			res.Rows = append(res.Rows, []any{e.Name, e.CLI, e.SpaceScoped, strings.Join(e.Joins, ", ")})
		}
	case "JOINS":
		e, ok := catalog.Lookup(s.Arg)
		if !ok {
			return nil, fmt.Errorf("unknown entity %q", s.Arg)
		}
		res.Headers = []string{"JOIN", "LIST-VALUED", "EXAMPLE"}
		for _, j := range e.Joins {
			name := strings.TrimSuffix(j, "*")
			ex := name + ".Slug = '…'"
			if strings.HasSuffix(j, "*") {
				ex = name + ".*.Slug = '…'"
			}
			res.Rows = append(res.Rows, []any{name, strings.HasSuffix(j, "*"), ex})
		}
	default:
		return nil, fmt.Errorf("SHOW %s: unknown; try ENTITIES, COLUMNS FROM <entity>, JOINS FROM <entity>, LABELS FROM <entity>, VALUES OF Labels.<key>, SPACES", s.What)
	}
	res.ServerRows = len(res.Rows)
	return res, nil
}

// Describe explains an entity or an Entity.Attribute from the bundled schema.
func Describe(name string) (string, error) {
	ent, attr := name, ""
	if i := strings.Index(name, "."); i > 0 {
		ent, attr = name[:i], name[i+1:]
	}
	e, ok := catalog.Lookup(ent)
	if !ok {
		return "", fmt.Errorf("unknown entity %q", ent)
	}
	var b strings.Builder
	if attr == "" {
		fmt.Fprintf(&b, "%s  (cub %s list; %s)\n\njoins: %s\n\n", e.Name, e.CLI, e.OrgPath, strings.Join(e.Joins, ", "))
		for _, a := range catalog.Attributes(e.Name) {
			f := " "
			if a.Filterable {
				f = "*"
			}
			fmt.Fprintf(&b, "%s %-28s %-8s %s\n", f, a.Name, a.Type, a.Desc)
		}
		b.WriteString("\n* = usable in WHERE\n")
		return b.String(), nil
	}
	a, ok := catalog.Attribute(e.Name, attr)
	if !ok {
		return "", fmt.Errorf("%s has no attribute %q", e.Name, attr)
	}
	fmt.Fprintf(&b, "%s.%s\n\ntype        %s\nfilterable  %v\n", e.Name, a.Name, a.Type, a.Filterable)
	if a.Filterable {
		fmt.Fprintf(&b, "operators   %s\n", strings.Join(catalog.Operators(a.Type), " "))
	}
	if len(a.Enum) > 0 {
		fmt.Fprintf(&b, "values      %s\n", strings.Join(a.Enum, ", "))
	}
	if a.Desc != "" {
		fmt.Fprintf(&b, "\n%s\n", a.Desc)
	}
	return b.String(), nil
}

func cols(h []string) []plan.Col {
	out := make([]plan.Col, len(h))
	for i, s := range h {
		out[i] = plan.Col{Header: s}
	}
	return out
}
