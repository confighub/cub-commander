package lang

import (
	"fmt"
	"strings"
)

// StmtString prints a statement in the canonical pipeline form, one step per
// line. The TUI rewrites statements through the AST and prints them with this,
// so what navigation writes into the command area is always re-parsable.
func StmtString(s *SelectStmt) string {
	var b strings.Builder
	if s.From.Saved != "" {
		b.WriteString(s.From.Saved)
	} else {
		b.WriteString(s.From.Entity)
	}
	if s.Scope != nil {
		if s.Scope.Org {
			b.WriteString(" | in *")
		} else {
			b.WriteString(" | in " + s.Scope.Space)
		}
	}
	columns := func() {
		if s.Star {
			return
		}
		parts := make([]string, len(s.Columns))
		for i, c := range s.Columns {
			parts[i] = ExprString(c.Expr)
			if c.Alias != "" {
				parts[i] += " as " + c.Alias
			}
		}
		b.WriteString("\n| columns " + strings.Join(parts, ", "))
	}
	pos := s.ColumnsPos
	if pos > len(s.Filters) {
		pos = len(s.Filters)
	}
	for i, f := range s.Filters {
		if i == pos {
			columns()
		}
		b.WriteString("\n| where " + ExprString(f.Expr))
	}
	if pos >= len(s.Filters) {
		columns()
	}
	if len(s.Browse) > 0 {
		parts := make([]string, len(s.Browse))
		for i, g := range s.Browse {
			parts[i] = ExprString(g)
		}
		b.WriteString("\n| browse by " + strings.Join(parts, ", "))
	}
	if s.Diff != nil {
		b.WriteString("\n| diff " + ExprString(s.Diff.A) + " vs " + ExprString(s.Diff.B))
		if len(s.Diff.By) > 0 {
			parts := make([]string, len(s.Diff.By))
			for i, r := range s.Diff.By {
				parts[i] = ExprString(r)
			}
			b.WriteString(" by " + strings.Join(parts, ", "))
		}
	}
	if s.Rollout != nil {
		b.WriteString("\n| rollout")
		if s.Rollout.Stage != "" {
			b.WriteString(" stage " + s.Rollout.Stage)
		}
	}
	if len(s.GroupBy) > 0 {
		parts := make([]string, len(s.GroupBy))
		for i, g := range s.GroupBy {
			parts[i] = ExprString(g)
		}
		b.WriteString("\n| group by " + strings.Join(parts, ", "))
	}
	if len(s.OrderBy) > 0 {
		parts := make([]string, len(s.OrderBy))
		for i, o := range s.OrderBy {
			parts[i] = ExprString(o.Ref)
			if o.Desc {
				parts[i] += " desc"
			}
		}
		b.WriteString("\n| order by " + strings.Join(parts, ", "))
	}
	if s.Limit != nil {
		b.WriteString(fmt.Sprintf("\n| limit %d", *s.Limit))
	}
	return b.String()
}

// Conjuncts splits a server WHERE into its terms.
func Conjuncts(e Expr) []Cmp {
	var out []Cmp
	var rec func(Expr)
	rec = func(x Expr) {
		switch y := x.(type) {
		case And:
			rec(y.L)
			rec(y.R)
		case Cmp:
			out = append(out, y)
		}
	}
	rec(e)
	return out
}

// Conjoin builds a WHERE from terms; nil when empty.
func Conjoin(terms []Cmp) Expr {
	var e Expr
	for _, t := range terms {
		if e == nil {
			e = t
		} else {
			e = And{e, t}
		}
	}
	return e
}
