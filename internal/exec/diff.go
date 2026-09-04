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

// Pair is one comparison: the units on each side that share a pairing key.
type Pair struct {
	Key    string
	Parts  []string // key values, in By order
	A, B   []cubclient.Row
	Status string // same, differ, only-a, only-b, multi
}

type DiffResult struct {
	By     []lang.Ref // the pairing keys actually used
	Pairs  []Pair
	Counts map[string]int
	ARows  int
	BRows  int
	ALabel string
	BLabel string
}

// RunDiff fetches both sides and pairs them.
func RunDiff(ctx context.Context, c *cubclient.Client, p *plan.Plan) (*DiffResult, error) {
	fetch := func(st plan.ListStage) ([]cubclient.Row, error) {
		q := url.Values{}
		if st.Where != "" {
			q.Set("where", st.Where)
		}
		if len(st.Select) > 0 {
			q.Set("select", strings.Join(st.Select, ","))
		}
		if len(st.Include) > 0 {
			q.Set("include", strings.Join(st.Include, ","))
		}
		path := p.Entity.OrgPath
		if st.Space != "" {
			id, err := c.SpaceID(ctx, st.Space)
			if err != nil {
				return nil, err
			}
			path = "/space/" + id + "/" + p.Entity.SpacePath
		}
		return c.List(ctx, path, q)
	}
	a, err := fetch(p.Diff.A)
	if err != nil {
		return nil, fmt.Errorf("side A: %w", err)
	}
	b, err := fetch(p.Diff.B)
	if err != nil {
		return nil, fmt.Errorf("side B: %w", err)
	}
	res := PairRows(p.Entity.Name, p.Diff.By, a, b)
	res.ALabel, res.BLabel = lang.ExprString(p.Diff.AExpr), lang.ExprString(p.Diff.BExpr)
	return res, nil
}

// PairRows pairs the two sides. With no explicit keys it uses the unit's
// identity (Slug, plus Labels.Component when present) and every label
// dimension whose set of values is the same on both sides: dev/us-east then
// pairs with prod/us-east, while Cluster, which differs by construction, is
// left out.
func PairRows(entity string, by []lang.Ref, a, b []cubclient.Row) *DiffResult {
	if len(by) == 0 {
		by = autoKeys(entity, a, b)
	}
	keyOf := func(r cubclient.Row) (string, []string) {
		parts := make([]string, len(by))
		for i, k := range by {
			parts[i] = str(Value(entity, k, r))
		}
		return strings.Join(parts, "\x00"), parts
	}
	pairs := map[string]*Pair{}
	var order []string
	add := func(rows []cubclient.Row, side int) {
		for _, r := range rows {
			k, parts := keyOf(r)
			p, ok := pairs[k]
			if !ok {
				p = &Pair{Key: strings.Join(parts, " / "), Parts: parts}
				pairs[k] = p
				order = append(order, k)
			}
			if side == 0 {
				p.A = append(p.A, r)
			} else {
				p.B = append(p.B, r)
			}
		}
	}
	add(a, 0)
	add(b, 1)
	res := &DiffResult{By: by, Counts: map[string]int{}, ARows: len(a), BRows: len(b)}
	sort.Strings(order)
	used := map[string]bool{}
	for _, k := range by {
		used[k.Path] = true
	}
	for _, k := range order {
		for _, p := range refine(entity, *pairs[k], used) {
			res.Counts[p.Status]++
			res.Pairs = append(res.Pairs, p)
		}
	}
	return res
}

// classify sets a pair's status from its sides and hashes.
func classify(entity string, p *Pair) {
	switch {
	case len(p.A) == 0:
		p.Status = "only-b"
	case len(p.B) == 0:
		p.Status = "only-a"
	case len(p.A) == 1 && len(p.B) == 1:
		if hash(entity, p.A[0]) == hash(entity, p.B[0]) {
			p.Status = "same"
		} else {
			p.Status = "differ"
		}
	default:
		p.Status = "multi"
		if allSame(entity, append(append([]cubclient.Row(nil), p.A...), p.B...)) {
			p.Status = "same"
		}
	}
}

// refine splits an n:m pair by the label key that matches the most values
// across its two sides (Region inside a dev-vs-prod pair, say), recursively,
// until pairs are 1:1 or no key helps. Unmatched values become one-sided pairs.
func refine(entity string, p Pair, used map[string]bool) []Pair {
	classify(entity, &p)
	if p.Status != "multi" {
		return []Pair{p}
	}
	label := func(r cubclient.Row, key string) string {
		return str(Value(entity, lang.Ref{Path: key}, r))
	}
	keys := map[string]bool{}
	for _, r := range append(append([]cubclient.Row(nil), p.A...), p.B...) {
		for _, lm := range labelMaps {
			m, _ := Value(entity, lang.Ref{Path: lm}, r).(map[string]any)
			for k := range m {
				if !used[lm+"."+k] {
					keys[lm+"."+k] = true
				}
			}
		}
	}
	bestKey, bestMatches := "", 0
	for k := range keys {
		va, vb := map[string]bool{}, map[string]bool{}
		for _, r := range p.A {
			va[label(r, k)] = true
		}
		for _, r := range p.B {
			vb[label(r, k)] = true
		}
		matches := 0
		for v := range va {
			if v != "" && vb[v] {
				matches++
			}
		}
		// A key only helps when it splits at least one side; a constant
		// label (the same DemoName on every row) matches without separating.
		if len(va) < 2 && len(vb) < 2 {
			continue
		}
		if matches > bestMatches || (matches == bestMatches && matches > 0 && k < bestKey) {
			bestKey, bestMatches = k, matches
		}
	}
	if bestMatches == 0 {
		return []Pair{p}
	}
	groups := map[string]*Pair{}
	var order []string
	for side, rows := range [][]cubclient.Row{p.A, p.B} {
		for _, r := range rows {
			v := label(r, bestKey)
			if v == "" {
				v = "(none)"
			}
			g, ok := groups[v]
			if !ok {
				g = &Pair{Key: p.Key + " / " + v, Parts: append(append([]string(nil), p.Parts...), v)}
				groups[v] = g
				order = append(order, v)
			}
			if side == 0 {
				g.A = append(g.A, r)
			} else {
				g.B = append(g.B, r)
			}
		}
	}
	sort.Strings(order)
	sub := map[string]bool{}
	for k := range used {
		sub[k] = true
	}
	sub[bestKey] = true
	var out []Pair
	for _, v := range order {
		out = append(out, refine(entity, *groups[v], sub)...)
	}
	return out
}

func hash(entity string, r cubclient.Row) string {
	return str(Value(entity, lang.Ref{Path: "DataHash"}, r))
}

func allSame(entity string, rows []cubclient.Row) bool {
	if len(rows) == 0 {
		return true
	}
	h := hash(entity, rows[0])
	for _, r := range rows[1:] {
		if hash(entity, r) != h {
			return false
		}
	}
	return true
}

// labelMaps are the label maps a unit row can be keyed by: its own and its
// space's. Component and Variant usually live on the space.
var labelMaps = []string{"Labels", "Space.Labels"}

// autoKeys: Slug, the Component label wherever it lives, then every label
// dimension (own or space) whose set of values is the same on both sides.
func autoKeys(entity string, a, b []cubclient.Row) []lang.Ref {
	keys := []lang.Ref{{Path: "Slug"}}
	values := func(rows []cubclient.Row) map[string]map[string]bool {
		out := map[string]map[string]bool{}
		for _, r := range rows {
			for _, lm := range labelMaps {
				m, _ := Value(entity, lang.Ref{Path: lm}, r).(map[string]any)
				for k, v := range m {
					p := lm + "." + k
					if out[p] == nil {
						out[p] = map[string]bool{}
					}
					out[p][str(v)] = true
				}
			}
		}
		return out
	}
	va, vb := values(a), values(b)
	for _, lm := range labelMaps {
		p := lm + ".Component"
		if len(va[p]) > 0 || len(vb[p]) > 0 {
			keys = append(keys, lang.Ref{Path: p})
			break
		}
	}
	var shared []string
	for p, setA := range va {
		setB := vb[p]
		if strings.HasSuffix(p, ".Component") || len(setA) < 2 || len(setA) != len(setB) {
			continue
		}
		same := true
		for v := range setA {
			if !setB[v] {
				same = false
				break
			}
		}
		if same {
			shared = append(shared, p)
		}
	}
	sort.Strings(shared)
	for _, p := range shared {
		keys = append(keys, lang.Ref{Path: p})
	}
	return keys
}

// UnitData fetches a unit's data as text.
func UnitData(ctx context.Context, c *cubclient.Client, row cubclient.Row) (string, error) {
	own, _ := row["Unit"].(map[string]any)
	uid, _ := own["UnitID"].(string)
	sid, _ := own["SpaceID"].(string)
	if sid == "" {
		if sp, ok := row["Space"].(map[string]any); ok {
			sid, _ = sp["SpaceID"].(string)
		}
	}
	if uid == "" || sid == "" {
		return "", fmt.Errorf("row has no UnitID/SpaceID")
	}
	return c.GetRaw(ctx, "/space/"+sid+"/unit/"+uid+"/data")
}

// unitPath is the API path of a unit row.
func unitPath(row cubclient.Row) (string, error) {
	own, _ := row["Unit"].(map[string]any)
	uid, _ := own["UnitID"].(string)
	sid, _ := own["SpaceID"].(string)
	if sid == "" {
		if sp, ok := row["Space"].(map[string]any); ok {
			sid, _ = sp["SpaceID"].(string)
		}
	}
	if uid == "" || sid == "" {
		return "", fmt.Errorf("row has no UnitID/SpaceID")
	}
	return "/space/" + sid + "/unit/" + uid, nil
}

// UnitDataWithHash fetches a unit's data and the DataHash the server served
// with it, which a later SaveUnitData sends as If-Match.
func UnitDataWithHash(ctx context.Context, c *cubclient.Client, row cubclient.Row) (string, string, error) {
	p, err := unitPath(row)
	if err != nil {
		return "", "", err
	}
	return c.GetRawETag(ctx, p+"/data")
}

// SaveUnitData writes new data as a revision, conditional on the hash the
// data was read at. Returns the new head revision number when the server
// reports it.
func SaveUnitData(ctx context.Context, c *cubclient.Client, row cubclient.Row, text, ifMatch, description string) (int, error) {
	p, err := unitPath(row)
	if err != nil {
		return 0, err
	}
	q := url.Values{}
	if description != "" {
		q.Set("last_change_description", description)
	}
	res, err := c.PutRaw(ctx, p+"/data", text, ifMatch, q)
	if err != nil {
		return 0, err
	}
	if u, ok := res["Unit"].(map[string]any); ok {
		if n, ok := u["HeadRevisionNum"].(float64); ok {
			return int(n), nil
		}
	}
	return 0, nil
}

// RowName is space/slug for a unit row.
func RowName(r cubclient.Row) string {
	own, _ := r["Unit"].(map[string]any)
	slug, _ := own["Slug"].(string)
	if sp, ok := r["Space"].(map[string]any); ok {
		if ss, _ := sp["Slug"].(string); ss != "" {
			return ss + "/" + slug
		}
	}
	return slug
}
