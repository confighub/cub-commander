// Package exec runs a plan: the list stage against the server, then the local
// stages over the rows, producing a result table.
package exec

import (
	"context"
	"fmt"
	"net/url"
	"sort"
	"strings"

	"github.com/confighub/cub-commander/internal/cubclient"
	"github.com/confighub/cub-commander/internal/lang"
	"github.com/confighub/cub-commander/internal/plan"
)

type Result struct {
	Headers    []string
	Columns    []plan.Col // effective columns after Labels.* expansion
	Rows       [][]any
	Raw        []cubclient.Row // the extended row behind each output row; nil when grouped
	ServerRows int
	Hidden     []string // label keys hidden from an expansion because they were constant
}

func Run(ctx context.Context, c *cubclient.Client, p *plan.Plan) (*Result, error) {
	rows, err := List(ctx, c, p)
	if err != nil {
		return nil, err
	}
	return Local(p, rows)
}

// List runs the plan's server stage and returns the extended rows.
func List(ctx context.Context, c *cubclient.Client, p *plan.Plan) ([]cubclient.Row, error) {
	q := url.Values{}
	if p.List.Where != "" {
		q.Set("where", p.List.Where)
	}
	if len(p.List.Select) > 0 {
		q.Set("select", strings.Join(p.List.Select, ","))
	}
	if len(p.List.Include) > 0 {
		q.Set("include", strings.Join(p.List.Include, ","))
	}
	path := p.Entity.OrgPath
	if p.List.Space != "" && p.Entity.SpaceScoped {
		id, err := c.SpaceID(ctx, p.List.Space)
		if err != nil {
			return nil, err
		}
		path = "/space/" + id + "/" + p.Entity.SpacePath
	}
	return c.List(ctx, path, q)
}

// Local runs the local stages over extended rows.
func Local(p *plan.Plan, rows []cubclient.Row) (*Result, error) {
	res := &Result{ServerRows: len(rows)}
	ev := &evaluator{entity: p.Entity.Name, aliases: map[string]lang.Expr{}}
	cols, hidden := expandColumns(ev, p.Columns, rows)
	res.Columns, res.Hidden = cols, hidden
	for _, c := range cols {
		res.Headers = append(res.Headers, c.Header)
	}
	for _, c := range cols {
		if c.Alias != "" {
			ev.aliases[c.Alias] = c.Expr
		}
	}
	grouped := false
	for _, st := range p.Local {
		switch st.Kind {
		case "having", "where":
			var kept []cubclient.Row
			for _, r := range rows {
				ok, err := ev.truth(st.Expr, r)
				if err != nil {
					return nil, fmt.Errorf("local where: %w", err)
				}
				if ok {
					kept = append(kept, r)
				}
			}
			rows = kept
		case "group":
			grouped = true
			rows = ev.group(st.Refs, cols, rows)
		case "order":
			ref, desc := st.Refs[0], st.Desc
			sort.SliceStable(rows, func(i, j int) bool {
				a, b := ev.value(ref, rows[i]), ev.value(ref, rows[j])
				if desc {
					return less(b, a)
				}
				return less(a, b)
			})
		case "limit":
			if len(rows) > st.Limit {
				rows = rows[:st.Limit]
			}
		}
	}
	for _, r := range rows {
		out := make([]any, len(cols))
		for i, c := range cols {
			if grouped {
				out[i] = r["__col"+fmt.Sprint(i)]
				continue
			}
			out[i] = ev.eval(c.Expr, r)
		}
		res.Rows = append(res.Rows, out)
		if !grouped {
			res.Raw = append(res.Raw, r)
		}
	}
	return res, nil
}

// expandColumns replaces each `<map>.*` column with one column per key found
// in the rows, most frequent first, capped, and skipping keys whose value is
// the same on every row (they are not a facet; DemoName is the usual case).
func expandColumns(ev *evaluator, in []plan.Col, rows []cubclient.Row) ([]plan.Col, []string) {
	const maxKeys = 6
	var out []plan.Col
	var hidden []string
	for _, c := range in {
		if c.Expand == "" {
			out = append(out, c)
			continue
		}
		counts := map[string]int{}
		distinct := map[string]map[string]bool{}
		for _, r := range rows {
			m, _ := ev.value(lang.Ref{Path: c.Expand}, r).(map[string]any)
			for k, v := range m {
				counts[k]++
				if distinct[k] == nil {
					distinct[k] = map[string]bool{}
				}
				distinct[k][str(v)] = true
			}
		}
		keys := make([]string, 0, len(counts))
		for k := range counts {
			if len(rows) > 1 && len(distinct[k]) == 1 && counts[k]*2 >= len(rows) {
				hidden = append(hidden, k)
				continue
			}
			keys = append(keys, k)
		}
		// Keys carried by most rows are the topology; a key on a handful of
		// rows (PR, Status) is noise as a column. Keep those covering at
		// least a quarter of the rows, or the best-covered few if none do.
		sort.Slice(keys, func(i, j int) bool { return counts[keys[i]] > counts[keys[j]] })
		var covered []string
		for _, k := range keys {
			if counts[k]*4 >= len(rows) {
				covered = append(covered, k)
			}
		}
		if len(covered) == 0 && len(keys) > 0 {
			covered = keys[:min(3, len(keys))]
		}
		keys = covered
		// Then coarse facets first: fewer distinct values to the left (Layer,
		// Environment … Component … Cluster), so a row reads like a path.
		sort.Slice(keys, func(i, j int) bool {
			if len(distinct[keys[i]]) != len(distinct[keys[j]]) {
				return len(distinct[keys[i]]) < len(distinct[keys[j]])
			}
			return keys[i] < keys[j]
		})
		if len(keys) > maxKeys {
			keys = keys[:maxKeys]
		}
		for _, k := range keys {
			path := c.Expand + "." + k
			header := k
			if i := strings.Index(c.Expand, "."); i > 0 {
				header = c.Expand[:i] + "·" + k
			}
			out = append(out, plan.Col{Header: header, Expr: lang.Ref{Path: path}})
		}
	}
	sort.Strings(hidden)
	return out, hidden
}

// Value evaluates one attribute reference against an extended row of the
// given entity; the browse panes use it per row and axis.
func Value(entity string, r lang.Ref, row cubclient.Row) any {
	ev := &evaluator{entity: entity, aliases: map[string]lang.Expr{}}
	return ev.value(r, row)
}

type evaluator struct {
	entity  string
	aliases map[string]lang.Expr
}

// value resolves an attribute path against an extended row. The first
// segment may be a join key present in the row (Space, Target, …), the entity
// name itself, or an attribute of the entity.
func (ev *evaluator) value(r lang.Ref, row cubclient.Row) any {
	if a, ok := ev.aliases[r.Path]; ok {
		return ev.eval(a, row)
	}
	segs := strings.Split(r.Path, ".")
	var cur any
	if v, ok := row[segs[0]]; ok && (segs[0] == ev.entity || isMap(v)) && len(segs) > 1 {
		cur = v
		segs = segs[1:]
	} else if ent, ok := row[ev.entity]; ok {
		cur = ent
	} else {
		cur = row
	}
	for _, s := range segs {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur, ok = m[s]
		if !ok {
			cur = nil
			break
		}
	}
	if r.Len {
		switch x := cur.(type) {
		case []any:
			return float64(len(x))
		case map[string]any:
			return float64(len(x))
		case string:
			return float64(len(x))
		case nil:
			return float64(0)
		}
	}
	return cur
}

func isMap(v any) bool { _, ok := v.(map[string]any); return ok }

func (ev *evaluator) eval(e lang.Expr, row cubclient.Row) any {
	switch x := e.(type) {
	case lang.Ref:
		return ev.value(x, row)
	case lang.Lit:
		switch x.Kind {
		case lang.LitString:
			return x.S
		case lang.LitNumber:
			return float64(x.N)
		default:
			return x.B
		}
	case lang.Cmp, lang.And, lang.Or, lang.Not:
		ok, err := ev.truth(x, row)
		if err != nil {
			return nil
		}
		return ok
	case lang.Call:
		// A computed column the runner derived onto the row (rollout columns).
		if m, ok := row["Rollout"].(map[string]any); ok {
			return m[strings.ToLower(x.Name)]
		}
	}
	return nil
}

func (ev *evaluator) truth(e lang.Expr, row cubclient.Row) (bool, error) {
	switch x := e.(type) {
	case lang.And:
		l, err := ev.truth(x.L, row)
		if err != nil || !l {
			return false, err
		}
		return ev.truth(x.R, row)
	case lang.Or:
		l, err := ev.truth(x.L, row)
		if err != nil {
			return false, err
		}
		if l {
			return true, nil
		}
		return ev.truth(x.R, row)
	case lang.Not:
		v, err := ev.truth(x.X, row)
		return !v, err
	case lang.Ref:
		v := ev.value(x, row)
		b, _ := v.(bool)
		return b, nil
	case lang.Cmp:
		return ev.compare(x, row)
	}
	return false, fmt.Errorf("cannot evaluate %s locally", lang.ExprString(e))
}

func (ev *evaluator) compare(c lang.Cmp, row cubclient.Row) (bool, error) {
	l := ev.eval(c.Left, row)
	var res bool
	switch c.Op {
	case "IS NULL":
		res = l == nil
	case "IS NOT NULL":
		res = l != nil
	case "IN", "NOT IN":
		list, _ := c.Right.(lang.ListLit)
		found := false
		for _, it := range list.Items {
			if equal(l, ev.eval(it, row)) {
				found = true
				break
			}
		}
		res = found == (c.Op == "IN")
	default:
		r := ev.eval(c.Right, row)
		var err error
		res, err = compareOp(c.Op, l, r)
		if err != nil {
			return false, err
		}
	}
	switch c.Truth {
	case "IS FALSE", "IS NOT TRUE":
		res = !res
	}
	return res, nil
}

func compareOp(op string, l, r any) (bool, error) {
	switch op {
	case "=":
		return equal(l, r), nil
	case "!=":
		return !equal(l, r), nil
	case "<":
		return less(l, r), nil
	case ">":
		return less(r, l), nil
	case "<=":
		return !less(r, l), nil
	case ">=":
		return !less(l, r), nil
	case "LIKE", "~~", "NOT LIKE", "!~~", "ILIKE":
		ls, rs := str(l), str(r)
		m := matchLike(ls, rs, op == "ILIKE")
		if op == "NOT LIKE" || op == "!~~" {
			return !m, nil
		}
		return m, nil
	case "~", "~*", "!~", "!~*":
		m, err := matchRegex(str(l), str(r), strings.HasSuffix(op, "*"))
		if err != nil {
			return false, err
		}
		if strings.HasPrefix(op, "!") {
			return !m, nil
		}
		return m, nil
	case "?":
		switch x := l.(type) {
		case []any:
			for _, it := range x {
				if equal(it, r) {
					return true, nil
				}
			}
		case map[string]any:
			_, ok := x[str(r)]
			return ok, nil
		}
		return false, nil
	}
	return false, fmt.Errorf("unsupported operator %s", op)
}
