package tui

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"charm.land/bubbles/v2/table"
	tea "charm.land/bubbletea/v2"

	"github.com/confighub/cub-commander/internal/cubclient"
	"github.com/confighub/cub-commander/internal/exec"
	"github.com/confighub/cub-commander/internal/lang"
	"github.com/confighub/cub-commander/internal/plan"
)

// fillTable loads the result into the grid with widths fitted to the pane.
func (m *Model) fillTable() {
	if m.result == nil {
		return
	}
	cols := make([]table.Column, len(m.result.Headers))
	widths := make([]int, len(m.result.Headers))
	for i, h := range m.result.Headers {
		widths[i] = len(h)
	}
	rows := make([]table.Row, len(m.result.Rows))
	for ri, r := range m.result.Rows {
		row := make(table.Row, len(r))
		for i, v := range r {
			s := exec.Format(v)
			row[i] = s
			if len(s) > widths[i] {
				widths[i] = len(s)
			}
		}
		rows[ri] = row
	}
	avail := m.mainWidth() - 2
	for i := range widths {
		if widths[i] > 40 {
			widths[i] = 40
		}
	}
	total := func() int {
		t := 0
		for _, w := range widths {
			t += w + 2
		}
		return t
	}
	for total() > avail {
		// shrink the widest column
		wi := 0
		for i, w := range widths {
			if w > widths[wi] {
				wi = i
			}
		}
		if widths[wi] <= 6 {
			break
		}
		widths[wi]--
	}
	for i, h := range m.result.Headers {
		title := h
		if i == m.colCursor {
			title = "▸" + h
		}
		cols[i] = table.Column{Title: title, Width: widths[i]}
	}
	cur := m.tbl.Cursor()
	// SetColumns re-renders whatever rows the table holds; clear them first
	// so a narrower result never renders the previous result's wider rows.
	m.tbl.SetRows(nil)
	m.tbl.SetColumns(cols)
	m.tbl.SetRows(rows)
	if cur < len(rows) {
		m.tbl.SetCursor(cur)
	}
}

func (m *Model) cell(col int) string {
	if m.result == nil {
		return ""
	}
	i := m.tbl.Cursor()
	if i < 0 || i >= len(m.result.Rows) || col >= len(m.result.Rows[i]) {
		return ""
	}
	return exec.Format(m.result.Rows[i][col])
}

// columns are the effective output columns (after Labels.* expansion).
func (m *Model) columns() []plan.Col {
	if m.result != nil && m.result.Columns != nil {
		return m.result.Columns
	}
	if m.plan != nil {
		return m.plan.Columns
	}
	return nil
}

// columnRef resolves the expression behind an output column, following aliases.
func (m *Model) columnRef(col int) (lang.Ref, bool) {
	cols := m.columns()
	if col >= len(cols) {
		return lang.Ref{}, false
	}
	r, ok := cols[col].Expr.(lang.Ref)
	return r, ok && !r.Len
}

// addChip appends `AND <column> = '<value>'` for the focused cell.
func (m Model) addChip() (tea.Model, tea.Cmd) {
	if m.stmt == nil || m.result == nil {
		return m, nil
	}
	ref, ok := m.columnRef(m.colCursor)
	if !ok {
		m.setStatus("this column is not a filterable attribute", true)
		return m, nil
	}
	i := m.tbl.Cursor()
	if i < 0 || i >= len(m.result.Rows) {
		return m, nil
	}
	v := m.result.Rows[i][m.colCursor]
	var right lang.Expr
	switch x := v.(type) {
	case float64:
		right = lang.Lit{Kind: lang.LitNumber, N: int64(x)}
	case bool:
		right = lang.Lit{Kind: lang.LitBool, B: x}
	case nil:
		st := addTerm(*m.stmt, lang.Cmp{Left: ref, Op: "IS NULL"})
		return m.rewrite(&st)
	default:
		s := exec.Format(v)
		if strings.ContainsAny(s, "'\"\\") {
			m.setStatus("value contains quotes; cannot be a where literal", true)
			return m, nil
		}
		right = lang.Lit{Kind: lang.LitString, S: s}
	}
	st := addTerm(*m.stmt, lang.Cmp{Left: ref, Op: "=", Right: right})
	return m.rewrite(&st)
}

// addTerm ANDs a server-pushable term onto the first where step, creating a
// leading step when there is none.
func addTerm(st lang.SelectStmt, t lang.Cmp) lang.SelectStmt {
	filters := append([]lang.Filter(nil), st.Filters...)
	if len(filters) > 0 && !filters[0].Local {
		filters[0].Expr = lang.Conjoin(append(lang.Conjuncts(filters[0].Expr), t))
	} else {
		filters = append([]lang.Filter{{Expr: t}}, filters...)
		st.ColumnsPos++
	}
	st.Filters = filters
	return st
}

// removeLastChip drops the last term of the last where step.
func (m Model) removeLastChip() (tea.Model, tea.Cmd) {
	if m.stmt == nil || len(m.stmt.Filters) == 0 {
		return m, nil
	}
	st := *m.stmt
	filters := append([]lang.Filter(nil), st.Filters...)
	last := len(filters) - 1
	terms := lang.Conjuncts(filters[last].Expr)
	if len(terms) <= 1 {
		filters = filters[:last]
		if last < st.ColumnsPos {
			st.ColumnsPos--
		}
	} else {
		filters[last].Expr = lang.Conjoin(terms[:len(terms)-1])
	}
	st.Filters = filters
	return m.rewrite(&st)
}

func (m Model) toggleOrder() (tea.Model, tea.Cmd) {
	if m.stmt == nil {
		return m, nil
	}
	ref, ok := m.columnRef(m.colCursor)
	if !ok {
		cols := m.columns()
		if m.colCursor < len(cols) && cols[m.colCursor].Alias != "" {
			ref = lang.Ref{Path: cols[m.colCursor].Alias}
		} else {
			return m, nil
		}
	}
	st := *m.stmt
	desc := false
	if len(st.OrderBy) > 0 && st.OrderBy[0].Ref.Path == ref.Path {
		desc = !st.OrderBy[0].Desc
	}
	st.OrderBy = []lang.OrderItem{{Ref: ref, Desc: desc}}
	return m.rewrite(&st)
}

// pivot rewrites the statement around the selected grid row.
func (m Model) pivot(k string) (tea.Model, tea.Cmd) {
	if m.result == nil || m.result.Raw == nil {
		m.setStatus("pivots need ungrouped rows", true)
		return m, nil
	}
	i := m.tbl.Cursor()
	if i < 0 || i >= len(m.result.Raw) {
		return m, nil
	}
	return m.pivotRow(k, m.result.Raw[i], m.plan.Entity.Name)
}

// pivotRow rewrites the statement around a row of the given entity:
// s its space's units, t its target's units, u upstream / a space's units,
// d downstreams, r revisions, l links.
func (m Model) pivotRow(k string, raw cubclient.Row, ent string) (tea.Model, tea.Cmd) {
	own, _ := raw[ent].(map[string]any)
	get := func(obj map[string]any, f string) string {
		if obj == nil {
			return ""
		}
		s, _ := obj[f].(string)
		return s
	}
	space, _ := raw["Space"].(map[string]any)
	spaceSlug := get(space, "Slug")
	if ent == "Space" {
		spaceSlug = get(own, "Slug")
	}
	sel := func(entity string, where lang.Expr, order *lang.OrderItem) (tea.Model, tea.Cmd) {
		st := &lang.SelectStmt{Star: true, From: lang.Source{Entity: entity}, Scope: &lang.Scope{Org: true}}
		if where != nil {
			st.Filters = []lang.Filter{{Expr: where}}
			st.ColumnsPos = 1
		}
		if order != nil {
			st.OrderBy = []lang.OrderItem{*order}
		}
		return m.rewrite(st)
	}
	eq := func(attr, val string) lang.Expr {
		return lang.Cmp{Left: lang.Ref{Path: attr}, Op: "=", Right: lang.Lit{Kind: lang.LitString, S: val}}
	}
	switch k {
	case "s":
		if spaceSlug == "" {
			break
		}
		return m.rewrite(&lang.SelectStmt{Star: true, From: lang.Source{Entity: "Unit"}, Scope: &lang.Scope{Space: spaceSlug}})
	case "t":
		if ent == "Space" {
			return m.rewrite(&lang.SelectStmt{Star: true, From: lang.Source{Entity: "Target"}, Scope: &lang.Scope{Space: spaceSlug}})
		}
		if id := get(own, "TargetID"); id != "" {
			return sel("Unit", eq("TargetID", id), nil)
		}
		if ent == "Target" {
			return sel("Unit", eq("TargetID", get(own, "TargetID")), nil)
		}
	case "u":
		if ent == "Space" && spaceSlug != "" {
			return m.rewrite(&lang.SelectStmt{Star: true, From: lang.Source{Entity: "Unit"}, Scope: &lang.Scope{Space: spaceSlug}})
		}
		if id := get(own, "UpstreamUnitID"); id != "" {
			return sel("Unit", eq("UnitID", id), nil)
		}
	case "d":
		if id := get(own, "UnitID"); id != "" {
			return sel("Unit", eq("UpstreamUnitID", id), nil)
		}
	case "r":
		if id := get(own, "UnitID"); id != "" && ent == "Unit" {
			return sel("Revision", eq("UnitID", id), &lang.OrderItem{Ref: lang.Ref{Path: "RevisionNum"}, Desc: true})
		}
		if id := get(own, "SpaceID"); id != "" {
			return sel("Revision", eq("SpaceID", id), &lang.OrderItem{Ref: lang.Ref{Path: "CreatedAt"}, Desc: true})
		}
	case "l":
		if id := get(own, "UnitID"); id != "" && ent == "Unit" {
			m.setStatus("outgoing links only: the server has no OR, so incoming links are a second query (ToUnitID)", false)
			return sel("Link", eq("FromUnitID", id), nil)
		}
		if ent == "Space" {
			return m.rewrite(&lang.SelectStmt{Star: true, From: lang.Source{Entity: "Link"}, Scope: &lang.Scope{Space: spaceSlug}})
		}
	case "m":
		if ent == "Unit" {
			m.setStatus("mutations arrive with the detail tabs in M5", false)
			return m, nil
		}
	}
	m.setStatus(fmt.Sprintf("no %q pivot for this row", k), true)
	return m, nil
}

// renderRow prints an extended row as indented key: value lines, joins first.
func renderRow(row map[string]any) string {
	var b strings.Builder
	keys := make([]string, 0, len(row))
	for k := range row {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Fprintf(&b, "%s\n", k)
		writeValue(&b, row[k], "  ")
	}
	return b.String()
}

func writeValue(b *strings.Builder, v any, indent string) {
	switch x := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			switch y := x[k].(type) {
			case map[string]any, []any:
				fmt.Fprintf(b, "%s%s:\n", indent, k)
				writeValue(b, y, indent+"  ")
			default:
				fmt.Fprintf(b, "%s%s: %s\n", indent, k, exec.Format(y))
			}
		}
	case []any:
		for _, it := range x {
			switch y := it.(type) {
			case map[string]any, []any:
				fmt.Fprintf(b, "%s-\n", indent)
				writeValue(b, y, indent+"  ")
			default:
				fmt.Fprintf(b, "%s- %s\n", indent, exec.Format(y))
			}
		}
	default:
		j, _ := json.Marshal(v)
		fmt.Fprintf(b, "%s%s\n", indent, j)
	}
}
