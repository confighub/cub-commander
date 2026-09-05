// Package plan compiles a parsed statement into stages the executor runs, and
// renders the plan as the cub command and API call each stage corresponds to.
package plan

import (
	"fmt"
	"net/url"
	"sort"
	"strings"

	"github.com/confighub/cub-commander/internal/catalog"
	"github.com/confighub/cub-commander/internal/lang"
)

// Session is the state a statement is compiled against.
type Session struct {
	Space string // "" or "*" means org-wide
}

func (s Session) Org() bool { return s.Space == "" || s.Space == "*" }

type Plan struct {
	Entity  catalog.Entity
	List    *ListStage
	Local   []LocalStage
	Columns []Col
	Pushed  []bool       // per statement filter: true when it went to the server
	Browse  []lang.Ref   // browse axes, when the statement has a browse by step
	Diff    *DiffPlan    // when the statement has a diff step
	Rollout *RolloutPlan // when the statement has a rollout step
	// RolloutCols is set when the columns include state(), stage(), next()
	// or blocker(): the runner derives them per ChangeOrder row from its
	// ChangeWorkflow before the local stages run.
	RolloutCols bool
}

// RolloutPlan is the rollout step: the list must yield one ChangeOrder.
type RolloutPlan struct {
	Stage string
}

// RolloutColumns are the computed columns a ChangeOrder statement may name.
var RolloutColumns = map[string]bool{"state": true, "stage": true, "next": true, "blocker": true}

// rolloutFields are the ChangeOrder attributes the derivation reads.
var rolloutFields = []string{"Slug", "SpaceID", "Space.Slug", "Description", "State", "AbortedReason", "CreatedAt",
	"StartTagID", "EndTagID", "InScopeSpaceIDs", "ResolvedSpaceIDs", "ReleasedSpaceIDs", "Annotations", "SkippedUnits"}

// DiffPlan is two list stages sharing the common where, one per side.
type DiffPlan struct {
	A, B   ListStage
	AExpr  lang.Expr
	BExpr  lang.Expr
	By     []lang.Ref // explicit pairing keys; empty means auto
	Common string
}

// Col is one output column. Expand marks a `Labels.*`-style column that the
// executor replaces with one column per label key found in the rows.
type Col struct {
	Header string
	Expr   lang.Expr
	Alias  string
	Expand string // "" or the map path to expand, e.g. "Labels", "Space.Labels"
}

// ListStage is one list API call.
type ListStage struct {
	Space   string // "" = org-wide
	Where   string
	Select  []string
	Include []string
}

// LocalStage is a stage evaluated in the client.
type LocalStage struct {
	Kind   string // having, group, order, limit
	Detail string
	Expr   lang.Expr
	Refs   []lang.Ref
	Desc   bool
	Limit  int
	Reason string
}

func Compile(st *lang.SelectStmt, s Session) (*Plan, error) {
	if st.Diff != nil && st.From.Entity != "" && !strings.EqualFold(st.From.Entity, "Unit") {
		lifted, err := liftToUnit(st)
		if err != nil {
			return nil, err
		}
		st = lifted
	}
	if st.From.Saved != "" {
		return nil, fmt.Errorf("FROM %s: saved views and filters as sources arrive in M5", st.From.Saved)
	}
	ent, ok := catalog.Lookup(st.From.Entity)
	if !ok {
		return nil, fmt.Errorf("unknown entity %q; SHOW ENTITIES lists them", st.From.Entity)
	}
	p := &Plan{Entity: ent}

	space := s.Space
	if st.Scope != nil {
		if st.Scope.Org {
			space = "*"
		} else {
			space = st.Scope.Space
		}
	}
	if space == "*" {
		space = ""
	}
	if space != "" && !ent.SpaceScoped {
		space = "" // org-level entity: the scope is meaningless, list org-wide
	}

	// Columns.
	p.Browse = st.Browse
	if st.Star {
		for _, c := range ent.DefaultCols {
			p.Columns = append(p.Columns, Col{Header: header(ent, c), Expr: lang.Ref{Path: c}, Expand: expandPath(c)})
		}
	} else {
		for _, c := range st.Columns {
			if call, ok := c.Expr.(lang.Call); ok {
				switch {
				case RolloutColumns[strings.ToLower(call.Name)] && len(call.Args) == 0:
					if ent.Name != "ChangeOrder" {
						return nil, fmt.Errorf("%s() is a rollout column; it needs a ChangeOrder statement", call.Name)
					}
					p.RolloutCols = true
				case !isAggregate(call.Name):
					return nil, fmt.Errorf("function column %s(): function columns arrive in M6", call.Name)
				case len(st.GroupBy) == 0:
					return nil, fmt.Errorf("%s() needs a GROUP BY", call.Name)
				}
			}
			col := Col{Expr: c.Expr, Alias: c.Alias}
			if r, ok := c.Expr.(lang.Ref); ok {
				col.Expand = expandPath(r.Path)
			}
			if c.Alias != "" {
				col.Header = c.Alias
			} else if r, ok := c.Expr.(lang.Ref); ok {
				col.Header = header(ent, lang.ExprString(r))
			} else {
				col.Header = lang.ExprString(c.Expr)
			}
			p.Columns = append(p.Columns, col)
		}
	}

	aliases := map[string]bool{}
	for _, c := range p.Columns {
		if c.Alias != "" {
			aliases[c.Alias] = true
		}
	}

	// Filters: the leading run of pushable where steps goes to the server as
	// one conjunction; from the first step that cannot be pushed, everything
	// runs locally, in step order. That is what "step order is plan order" means.
	var serverTerms []lang.Cmp
	var localFilters []lang.Expr
	var localReasons []string
	pushing := true
	for _, f := range st.Filters {
		reason := ""
		if f.Local {
			reason = "HAVING is always local"
		} else if err := lang.CheckServerWhere(f.Expr); err != nil {
			reason = err.(*lang.ParseError).Msg
		} else if a := aliasRef(f.Expr, aliases); a != "" {
			reason = a + " is a column alias, not a server attribute"
		} else if !pushing {
			reason = "follows a local step"
		}
		if reason == "" {
			serverTerms = append(serverTerms, lang.Conjuncts(f.Expr)...)
			p.Pushed = append(p.Pushed, true)
			continue
		}
		pushing = false
		p.Pushed = append(p.Pushed, false)
		localFilters = append(localFilters, f.Expr)
		localReasons = append(localReasons, reason)
	}

	// Which attributes must come back from the server: every ref in columns,
	// local filters, group and order, projected onto the entity's own fields
	// plus the include IDs of the joins referenced.
	refs := []lang.Ref{}
	for _, c := range p.Columns {
		refs = append(refs, lang.Refs(c.Expr)...)
	}
	for _, f := range localFilters {
		refs = append(refs, lang.Refs(f)...)
	}
	refs = append(refs, st.GroupBy...)
	if st.Diff != nil {
		refs = append(refs, lang.Ref{Path: "DataHash"}, lang.Ref{Path: "Labels"}, lang.Ref{Path: "SpaceID"}, lang.Ref{Path: "Space.Slug"}, lang.Ref{Path: "Space.Labels"})
		refs = append(refs, st.Diff.By...)
	}
	if st.Rollout != nil {
		if ent.Name != "ChangeOrder" {
			return nil, fmt.Errorf("rollout opens a ChangeOrder; start from ChangeOrder")
		}
		p.Rollout = &RolloutPlan{Stage: st.Rollout.Stage}
	}
	if p.Rollout != nil || p.RolloutCols {
		for _, f := range rolloutFields {
			refs = append(refs, lang.Ref{Path: f})
		}
	}
	for _, b := range st.Browse {
		if _, isEntity := catalog.Lookup(b.Path); isEntity && !strings.Contains(b.Path, ".") {
			continue // an entity hop (…, Resource): a pane loaded on demand, not a field
		}
		refs = append(refs, b)
	}
	for _, o := range st.OrderBy {
		refs = append(refs, o.Ref)
	}
	sel := map[string]bool{}
	if ent.IDField != "" {
		sel[ent.IDField] = true
	}
	inc := map[string]bool{}
	for _, id := range ent.IncludeIDs {
		inc[id] = true
	}
	for _, r := range refs {
		if aliases[r.Path] || r.Path == "DISTINCT" {
			continue
		}
		segs := strings.Split(r.Path, ".")
		if strings.EqualFold(segs[0], ent.Name) && len(segs) > 1 {
			segs = segs[1:]
		}
		// A bare name is the entity's own attribute even when it is also a
		// join prefix (ApprovedBy is a UUID list; ApprovedBy.*.Username is a join).
		if len(segs) > 1 && ent.IsJoin(segs[0]) {
			inc[segs[0]+"ID"] = true
			// Name the joined field in select: the server then trims the joined
			// object to it (a whole Unit per row is most of a 30 MB response).
			field := segs[1]
			if field == "*" && len(segs) > 2 {
				field = segs[2]
			}
			sel[segs[0]+"."+field] = true
			continue
		}
		if segs[0] == "Data" {
			continue // config data is fetched separately (M6)
		}
		sel[segs[0]] = true
	}
	// Every included join gets at least its Slug selected, so an unreferenced
	// join (included for pivots and row labels) comes back trimmed too. The
	// entity's own ID field for the join (UnitID on a Resource) is selected as
	// well: the detail view walks from a resource to its unit through it.
	for id := range inc {
		if _, own := catalog.Attribute(ent.Name, id); own {
			sel[id] = true
		}
		join := strings.TrimSuffix(id, "ID")
		if je := catalog.JoinEntity(join); je != "" {
			if _, ok := catalog.Attribute(je, "Slug"); ok {
				sel[join+".Slug"] = true
			}
		}
	}
	p.List = &ListStage{Space: space, Where: lang.ServerWhere(lang.Conjoin(serverTerms)), Select: sortedKeys(sel), Include: sortedKeys(inc)}

	if st.Diff != nil {
		if ent.Name != "Unit" {
			return nil, fmt.Errorf("diff compares units; start from Unit, Space, Target or Resource")
		}
		for _, side := range []lang.Expr{st.Diff.A, st.Diff.B} {
			if err := lang.CheckServerWhere(side); err != nil {
				return nil, fmt.Errorf("diff side %s: %s", lang.ExprString(side), err.(*lang.ParseError).Msg)
			}
		}
		if len(localFilters) > 0 {
			return nil, fmt.Errorf("diff needs every where step before it to be server-side")
		}
		mk := func(side lang.Expr) ListStage {
			terms := append(append([]lang.Cmp(nil), serverTerms...), lang.Conjuncts(side)...)
			return ListStage{Space: space, Where: lang.ServerWhere(lang.Conjoin(terms)), Select: p.List.Select, Include: p.List.Include}
		}
		p.Diff = &DiffPlan{A: mk(st.Diff.A), B: mk(st.Diff.B), AExpr: st.Diff.A, BExpr: st.Diff.B, By: st.Diff.By, Common: p.List.Where}
	}

	if p.RolloutCols {
		p.Local = append(p.Local, LocalStage{Kind: "rollout", Detail: "state(), stage(), next(), blocker()",
			Reason: "the server stores no stage; each is read from the ChangeOrder's ChangeWorkflow revision, its stage selectors and the spaces' live status, as cub changeorder list does"})
	}
	for i, f := range localFilters {
		p.Local = append(p.Local, LocalStage{Kind: "where", Expr: f, Detail: lang.ExprString(f), Reason: localReasons[i]})
	}
	if len(st.GroupBy) > 0 {
		names := make([]string, len(st.GroupBy))
		for i, g := range st.GroupBy {
			names[i] = g.Path
		}
		p.Local = append(p.Local, LocalStage{Kind: "group", Refs: st.GroupBy, Detail: strings.Join(names, ", "), Reason: "views can band by a column but not aggregate"})
	}
	for _, o := range st.OrderBy {
		d := o.Ref.Path
		if o.Desc {
			d += " DESC"
		}
		p.Local = append(p.Local, LocalStage{Kind: "order", Refs: []lang.Ref{o.Ref}, Desc: o.Desc, Detail: d, Reason: "the list API has no order_by; a saved View can carry OrderBy"})
	}
	if st.Limit != nil {
		p.Local = append(p.Local, LocalStage{Kind: "limit", Limit: *st.Limit, Detail: fmt.Sprint(*st.Limit), Reason: "the list API has no pagination"})
	}
	return p, nil
}

// liftToUnit rewrites a diff over Spaces, Targets or Resources as a diff over
// the Units inside them: the unit is what gets compared, the selector may sit
// at any level. Space terms become Space.<attr>, Target terms Target.<attr>,
// Resource terms drop their Unit. prefix; a Resource's own attributes have no
// unit-level equivalent and are rejected.
func liftToUnit(st *lang.SelectStmt) (*lang.SelectStmt, error) {
	ent, ok := catalog.Lookup(st.From.Entity)
	if !ok {
		return nil, fmt.Errorf("unknown entity %q", st.From.Entity)
	}
	var lift func(path string) (string, error)
	switch ent.Name {
	case "Space", "Target":
		lift = func(path string) (string, error) {
			first := strings.Split(path, ".")[0]
			if ent.IsJoin(first) {
				return "", fmt.Errorf("%s: a %s join cannot be expressed from Unit; select units directly", path, ent.Name)
			}
			return ent.Name + "." + strings.TrimPrefix(path, ent.Name+"."), nil
		}
	case "Resource":
		lift = func(path string) (string, error) {
			segs := strings.Split(path, ".")
			switch {
			case segs[0] == "Unit" && len(segs) > 1:
				return strings.Join(segs[1:], "."), nil
			case (segs[0] == "Space" || segs[0] == "Target") && len(segs) > 1:
				return path, nil
			}
			return "", fmt.Errorf("%s is a Resource attribute; diff compares units, so select by Unit., Space. or Target. attributes", path)
		}
	default:
		return nil, fmt.Errorf("diff compares units; start from Unit, Space, Target or Resource")
	}
	var liftExpr func(e lang.Expr) (lang.Expr, error)
	liftExpr = func(e lang.Expr) (lang.Expr, error) {
		switch x := e.(type) {
		case lang.Ref:
			np, err := lift(x.Path)
			if err != nil {
				return nil, err
			}
			return lang.Ref{Path: np, Len: x.Len}, nil
		case lang.Cmp:
			l, err := liftExpr(x.Left)
			if err != nil {
				return nil, err
			}
			r := x.Right
			if rr, ok := r.(lang.Ref); ok {
				if r, err = liftExpr(rr); err != nil {
					return nil, err
				}
			}
			return lang.Cmp{Left: l, Op: x.Op, Right: r, Truth: x.Truth}, nil
		case lang.And:
			l, err := liftExpr(x.L)
			if err != nil {
				return nil, err
			}
			r, err := liftExpr(x.R)
			if err != nil {
				return nil, err
			}
			return lang.And{L: l, R: r}, nil
		case lang.Or:
			l, err := liftExpr(x.L)
			if err != nil {
				return nil, err
			}
			r, err := liftExpr(x.R)
			if err != nil {
				return nil, err
			}
			return lang.Or{L: l, R: r}, nil
		case lang.Not:
			in, err := liftExpr(x.X)
			if err != nil {
				return nil, err
			}
			return lang.Not{X: in}, nil
		}
		return e, nil
	}
	out := *st
	out.From = lang.Source{Entity: "Unit"}
	out.Star, out.Columns = true, nil
	out.Filters = nil
	for _, f := range st.Filters {
		e, err := liftExpr(f.Expr)
		if err != nil {
			return nil, err
		}
		out.Filters = append(out.Filters, lang.Filter{Expr: e, Local: f.Local})
	}
	a, err := liftExpr(st.Diff.A)
	if err != nil {
		return nil, err
	}
	b, err := liftExpr(st.Diff.B)
	if err != nil {
		return nil, err
	}
	d := &lang.DiffStep{A: a, B: b}
	for _, r := range st.Diff.By {
		np, err := lift(r.Path)
		if err != nil {
			return nil, err
		}
		d.By = append(d.By, lang.Ref{Path: np})
	}
	out.Diff = d
	out.Browse = nil
	return &out, nil
}

func isAggregate(name string) bool {
	switch strings.ToUpper(name) {
	case "COUNT", "MIN", "MAX":
		return true
	}
	return false
}

// aliasRef returns the first reference to a column alias in an expression.
func aliasRef(e lang.Expr, aliases map[string]bool) string {
	for _, r := range lang.Refs(e) {
		if aliases[r.Path] {
			return r.Path
		}
	}
	return ""
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// expandPath returns the map path of a `<map>.*` column, else "".
func expandPath(path string) string {
	if !strings.HasSuffix(path, ".*") {
		return ""
	}
	base := strings.TrimSuffix(path, ".*")
	segs := strings.Split(base, ".")
	if catalog.IsMapField(segs[len(segs)-1]) {
		return base
	}
	return ""
}

// header mimics cub's column headers: entity prefix stripped, Slug → NAME,
// X.Slug → X, Labels.k → LABEL:k, else the last segment upper-cased.
func header(ent catalog.Entity, path string) string {
	segs := strings.Split(path, ".")
	if strings.EqualFold(segs[0], ent.Name) && len(segs) > 1 {
		segs = segs[1:]
	}
	switch {
	case len(segs) == 1 && segs[0] == "Slug":
		return "NAME"
	case len(segs) == 2 && segs[1] == "Slug":
		return strings.ToUpper(segs[0])
	case len(segs) == 2 && segs[0] == "Labels":
		return segs[1] // a label key is its own column; the key is the header
	case len(segs) == 3 && segs[1] == "Labels":
		return segs[0] + "·" + segs[2]
	case len(segs) >= 2 && catalog.IsMapField(segs[0]):
		return strings.ToUpper(strings.TrimSuffix(segs[0], "s")) + ":" + strings.Join(segs[1:], ".")
	}
	return strings.ToUpper(segs[len(segs)-1])
}

// CubCommand renders the list stage as the cub command a user would type.
func (p *Plan) CubCommand() string {
	var b strings.Builder
	b.WriteString("cub " + p.Entity.CLI + " list")
	if p.Entity.SpaceScoped {
		if p.List.Space == "" {
			b.WriteString(" --space '*'")
		} else {
			b.WriteString(" --space " + p.List.Space)
		}
	}
	if p.List.Where != "" {
		b.WriteString(fmt.Sprintf(" --where %q", p.List.Where))
	}
	var cols []string
	for _, c := range p.Columns {
		if c.Expand != "" {
			cols = append(cols, p.Entity.Name+"."+strings.TrimPrefix(c.Expand, p.Entity.Name+".")+" (one column per key here)")
			continue
		}
		if r, ok := c.Expr.(lang.Ref); ok && !r.Len {
			path := r.Path
			if !strings.Contains(path, ".") || !p.Entity.IsJoin(strings.Split(path, ".")[0]) {
				if !strings.HasPrefix(path, p.Entity.Name+".") {
					path = p.Entity.Name + "." + path
				}
			}
			cols = append(cols, path)
		}
	}
	if len(cols) > 0 {
		b.WriteString(" --columns " + strings.Join(cols, ","))
	}
	return b.String()
}

// APIPath renders the list stage as the HTTP request.
func (p *Plan) APIPath(spaceID string) string {
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
		id := spaceID
		if id == "" {
			id = "{" + p.List.Space + "}"
		}
		path = "/space/" + id + "/" + p.Entity.SpacePath
	}
	if len(q) == 0 {
		return "GET /api" + path
	}
	return "GET /api" + path + "?" + q.Encode()
}

// Explain renders the plan as numbered stages.
func (p *Plan) Explain(spaceID string) string {
	var b strings.Builder
	if p.Diff != nil {
		saved := *p.List
		p.List = &p.Diff.A
		fmt.Fprintf(&b, "plan\n  1. list side A: %s\n     %s\n     %s\n", lang.ExprString(p.Diff.AExpr), p.CubCommand(), p.APIPath(spaceID))
		p.List = &p.Diff.B
		fmt.Fprintf(&b, "  2. list side B: %s\n     %s\n     %s\n", lang.ExprString(p.Diff.BExpr), p.CubCommand(), p.APIPath(spaceID))
		p.List = &saved
		b.WriteString("  3. pair units by identity and the label dimensions both sides share; compare DataHash\n     local\n  4. fetch data for the pair on screen\n     cub unit data <unit> (both sides), diffed locally\n")
		return b.String()
	}
	fmt.Fprintf(&b, "plan\n  1. list %s\n     %s\n     %s\n", p.Entity.Name, p.CubCommand(), p.APIPath(spaceID))
	n := 2
	for _, l := range p.Local {
		fmt.Fprintf(&b, "  %d. %s %s\n     local", n, l.Kind, l.Detail)
		if l.Reason != "" {
			fmt.Fprintf(&b, "   (%s)", l.Reason)
		}
		b.WriteString("\n")
		n++
	}
	if p.Rollout != nil {
		fmt.Fprintf(&b, "  %d. read the ChangeWorkflow revision the change order pins\n     GET /api/unit?where=UnitID = '…'   then   cub unit data <workflow> --revision <n>\n", n)
		fmt.Fprintf(&b, "  %d. resolve each stage's spaces\n     cub space list --where \"<stage.whereSpace> AND Labels.Component = '<component>'\"   filtered to InScopeSpaceIDs\n", n+1)
		fmt.Fprintf(&b, "  %d. derive taken/released from ResolvedSpaceIDs/ReleasedSpaceIDs, healthy from the space's live-status annotation; next stage and gates as cub variant promote checks them\n     local\n", n+2)
		fmt.Fprintf(&b, "  %d. the change per space: revisions carrying the start and end tags\n     cub revision list --space '*' --where \"SpaceID = '…' AND Tags ? '<tag>'\"   (twice: the where language has no OR)   then   GET /api/revision_data?where=RevisionID IN (…)\n", n+3)
	}
	return b.String()
}
