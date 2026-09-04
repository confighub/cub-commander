package exec

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/confighub/cub-commander/internal/cubclient"
	"github.com/confighub/cub-commander/internal/lang"
	"github.com/confighub/cub-commander/internal/plan"
)

func str(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case float64:
		if x == float64(int64(x)) {
			return fmt.Sprint(int64(x))
		}
		return fmt.Sprint(x)
	case bool:
		return fmt.Sprint(x)
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		parts := make([]string, 0, len(keys))
		for _, k := range keys {
			parts = append(parts, k+"="+str(x[k]))
		}
		return strings.Join(parts, ",")
	case []any:
		return fmt.Sprintf("%d items", len(x))
	}
	return fmt.Sprint(v)
}

// Format renders a cell for display.
func Format(v any) string { return str(v) }

func equal(l, r any) bool {
	lf, lok := l.(float64)
	rf, rok := r.(float64)
	if lok && rok {
		return lf == rf
	}
	if lok || rok {
		return str(l) == str(r)
	}
	return str(l) == str(r)
}

func less(l, r any) bool {
	lf, lok := l.(float64)
	rf, rok := r.(float64)
	if lok && rok {
		return lf < rf
	}
	if l == nil && r != nil {
		return true
	}
	return str(l) < str(r)
}

func matchLike(s, pattern string, fold bool) bool {
	var b strings.Builder
	b.WriteString("^")
	for _, c := range pattern {
		switch c {
		case '%':
			b.WriteString(".*")
		case '_':
			b.WriteString(".")
		default:
			b.WriteString(regexp.QuoteMeta(string(c)))
		}
	}
	b.WriteString("$")
	m, _ := matchRegex(s, b.String(), fold)
	return m
}

func matchRegex(s, pattern string, fold bool) (bool, error) {
	if fold {
		pattern = "(?i)" + pattern
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return false, fmt.Errorf("bad regex %q: %w", pattern, err)
	}
	return re.MatchString(s), nil
}

// group implements GROUP BY with COUNT/MIN/MAX columns. Output rows carry the
// computed column values under __col<i>.
func (ev *evaluator) group(keys []lang.Ref, cols []plan.Col, rows []cubclient.Row) []cubclient.Row {
	type bucket struct {
		key  string
		rows []cubclient.Row
	}
	var order []string
	buckets := map[string]*bucket{}
	for _, r := range rows {
		parts := make([]string, len(keys))
		for i, k := range keys {
			parts[i] = str(ev.value(k, r))
		}
		k := strings.Join(parts, "\x00")
		b, ok := buckets[k]
		if !ok {
			b = &bucket{key: k}
			buckets[k] = b
			order = append(order, k)
		}
		b.rows = append(b.rows, r)
	}
	var out []cubclient.Row
	for _, k := range order {
		b := buckets[k]
		first := b.rows[0]
		row := cubclient.Row{}
		for k, v := range first {
			row[k] = v
		}
		for i, c := range cols {
			switch x := c.Expr.(type) {
			case lang.Call:
				row["__col"+fmt.Sprint(i)] = ev.aggregate(x, b.rows)
			default:
				row["__col"+fmt.Sprint(i)] = ev.eval(x, first)
			}
		}
		out = append(out, row)
	}
	return out
}

func (ev *evaluator) aggregate(c lang.Call, rows []cubclient.Row) any {
	name := strings.ToUpper(c.Name)
	distinct := false
	var arg lang.Expr = lang.Star{}
	for _, a := range c.Args {
		if r, ok := a.(lang.Ref); ok && r.Path == "DISTINCT" {
			distinct = true
			continue
		}
		arg = a
	}
	switch name {
	case "COUNT":
		if _, star := arg.(lang.Star); star {
			return float64(len(rows))
		}
		seen := map[string]bool{}
		n := 0
		for _, r := range rows {
			v := ev.eval(arg, r)
			if v == nil {
				continue
			}
			if distinct {
				if seen[str(v)] {
					continue
				}
				seen[str(v)] = true
			}
			n++
		}
		return float64(n)
	case "MIN", "MAX":
		var best any
		for _, r := range rows {
			v := ev.eval(arg, r)
			if v == nil {
				continue
			}
			if best == nil || (name == "MIN" && less(v, best)) || (name == "MAX" && less(best, v)) {
				best = v
			}
		}
		return best
	}
	return nil
}
