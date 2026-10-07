package rollout

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"

	"github.com/confighub/cub-commander/internal/cubclient"
)

// PlanResult is a promote result: what a promotion did, or on a dry run
// would do, with the gates of every stage it enters. A refusal by the gates
// (409) is the same shape with Refused set and nothing written.
type PlanResult struct {
	DryRun   bool
	Plan     string // digest of the planned actions; sent back as ExpectedPlan
	Complete bool   // every stage already has the change
	Refused  bool   // the gates do not hold
	Status   int
	Stages   []StageResult
	Spaces   []SpaceResult
}

type StageResult struct {
	Name, PreviousStage string
	Chosen, Forced      bool
	Gates               []Gate
}

type SpaceResult struct {
	SpaceID, SpaceSlug                 string
	UpstreamSpaceID, UpstreamSpaceSlug string
	Stage                              string
	Action                             string // Promote, Unchanged, Skipped, Blocked, Failed
	Reason                             string
	Units                              []UnitResult
	Links                              []LinkResult
}

type UnitResult struct {
	UnitID, Slug, UpstreamUnitID   string
	Action                         string // Upgrade, Resolve, Mark, Empty, Revive, Clone, Invoke, Unchanged, Skip
	Reason                         string
	Err                            string
	FromUpstreamRev, ToUpstreamRev int
	Fields                         []FieldChange // include=Diff
	Conflicts                      []Conflict
}

type LinkResult struct {
	Slug, Action, Reason, Err string
}

// ParsePlan decodes a promote response of any of its statuses.
func ParsePlan(status int, body []byte) (*PlanResult, error) {
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		return nil, fmt.Errorf("decode promote result: %w", err)
	}
	p := &PlanResult{DryRun: m["DryRun"] == true, Plan: str(m["Plan"]), Complete: m["Complete"] == true, Refused: status == http.StatusConflict, Status: status}
	for _, st := range list(m["Stages"]) {
		sr := StageResult{Name: str(st["Name"]), PreviousStage: str(st["PreviousStage"]), Chosen: st["Chosen"] == true, Forced: st["Forced"] == true}
		for _, g := range list(st["Gates"]) {
			sr.Gates = append(sr.Gates, Gate{Name: str(g["Prerequisite"]), Space: str(g["SpaceSlug"]), OK: g["Satisfied"] == true, Reason: str(g["Message"])})
		}
		p.Stages = append(p.Stages, sr)
	}
	for _, sp := range list(m["Spaces"]) {
		s := SpaceResult{SpaceID: str(sp["SpaceID"]), SpaceSlug: str(sp["SpaceSlug"]), UpstreamSpaceID: str(sp["UpstreamSpaceID"]), UpstreamSpaceSlug: str(sp["UpstreamSpaceSlug"]), Stage: str(sp["Stage"]), Action: str(sp["Action"]), Reason: firstNonEmpty(str(sp["Reason"]), errText(sp["Error"]))}
		for _, u := range list(sp["Units"]) {
			s.Units = append(s.Units, UnitResult{
				UnitID: str(u["UnitID"]), Slug: str(u["Slug"]), UpstreamUnitID: str(u["UpstreamUnitID"]),
				Action: str(u["Action"]), Reason: str(u["Reason"]), Err: errText(u["Error"]),
				FromUpstreamRev: num(u["FromUpstreamRevisionNum"]), ToUpstreamRev: num(u["ToUpstreamRevisionNum"]),
				Fields: ParseConfigDiff(u["Diff"]), Conflicts: ParseConflicts(u["Conflicts"]),
			})
		}
		for _, l := range list(sp["Links"]) {
			s.Links = append(s.Links, LinkResult{Slug: str(l["Slug"]), Action: str(l["Action"]), Reason: str(l["Reason"]), Err: errText(l["Error"])})
		}
		p.Spaces = append(p.Spaces, s)
	}
	return p, nil
}

func errText(v any) string {
	switch e := v.(type) {
	case map[string]any:
		return firstNonEmpty(str(e["Message"]), str(e["message"]), str(e["Error"]))
	case string:
		return e
	}
	return ""
}

// promote sends one promote request and reads its result, whatever the
// status: 200 and 207 carry what was done, 409 the gates that refused it.
func promote(ctx context.Context, c Client, q url.Values, req map[string]any) (*PlanResult, error) {
	body, _ := json.Marshal(req)
	status, out, err := c.Send(ctx, http.MethodPost, "/promote", q, "application/json", string(body))
	switch status {
	case http.StatusOK, http.StatusMultiStatus, http.StatusConflict:
		p, perr := ParsePlan(status, out)
		if perr != nil {
			if err != nil {
				return nil, err
			}
			return nil, perr
		}
		return p, nil
	case http.StatusPreconditionFailed:
		return nil, fmt.Errorf("the promotion would now do something different from the dry run (412); R re-runs it")
	}
	if err != nil {
		return nil, err
	}
	return nil, fmt.Errorf("promote: unexpected status %d", status)
}

// gatePlan is the dry run that evaluates the next stage's gates, once per
// order per run.
func (c *Cache) gatePlan(ctx context.Context, cl Client, o Order) (*PlanResult, error) {
	c.mu.Lock()
	a, ok := c.plans[o.ID]
	c.mu.Unlock()
	if ok {
		return a.plan, a.err
	}
	p, err := promote(ctx, cl, nil, map[string]any{"ChangeOrderID": o.ID, "DryRun": true})
	c.mu.Lock()
	c.plans[o.ID] = &planAnswer{plan: p, err: err}
	c.mu.Unlock()
	return p, err
}

// ---- preview

// UnitPreview is what promoting would do to one unit of one space.
type UnitPreview struct {
	UnitID, Slug string
	Action       string // Upgrade, Clone, Mark, …
	Reason       string
	Fields       []FieldChange
	NoChange     bool
	New          bool // Clone: the space does not have the unit yet
	Err          string
	// Kept are the fields of the ordered change this merge leaves alone:
	// the space's value stays, as a local override or a protected path.
	Kept []KeptField
}

// KeptField is an upstream change the merge does not bring.
type KeptField struct {
	Doc, Path string
	Current   string // what the space keeps
	Upstream  string // what the upstream changed it to
	Protected bool
	Why       string // the server's reason, when it withheld the path
}

// SpacePreview is one space of the dry run.
type SpacePreview struct {
	Space   Space
	Action  string // Promote, Unchanged, Skipped, Blocked, Failed
	Reason  string
	Stage   string
	Units   []UnitPreview
	Links   LinkSummary
	Err     string // Blocked or Failed: the reason
	Skipped string // Skipped: the reason
}

type LinkSummary struct {
	Create, Adopted   int
	Skipped, Orphaned []string
}

// Preview is a stage's dry run.
type Preview struct {
	Stage    string
	Plan     string
	Complete bool
	Refused  bool
	Gates    []Gate
	Spaces   []SpacePreview
}

// Blockers are the reasons a promote of this preview would be refused or
// would land short.
func (p *Preview) Blockers() []string {
	var out []string
	if p.Refused {
		for _, g := range p.Gates {
			if !g.OK {
				out = append(out, g.Reason)
			}
		}
	}
	for _, sp := range p.Spaces {
		if sp.Err != "" {
			out = append(out, sp.Space.Slug+": "+sp.Err)
		}
		for _, u := range sp.Units {
			if u.Err != "" {
				out = append(out, sp.Space.Slug+"/"+u.Slug+": "+u.Err)
			}
		}
	}
	return out
}

// Changed counts the units the promotion would write and the fields that change.
func (p *Preview) Changed() (units, fields int) {
	for _, sp := range p.Spaces {
		for _, u := range sp.Units {
			if !u.NoChange && u.Err == "" {
				units++
				fields += len(u.Fields)
			}
		}
	}
	return
}

// PreviewStage dry-runs the promotion into one stage with include=Diff, the
// request `cub variant promote --change-order … --target-stage … --dry-run
// -o mutations` makes, and reads what each unit would change. The ordered
// change's fields a merge leaves alone are added as Kept.
func PreviewStage(ctx context.Context, c Client, r *Rollout, stage int) (*Preview, error) {
	if stage <= 0 || stage >= len(r.Stages) {
		return nil, fmt.Errorf("no such stage")
	}
	st := r.Stages[stage]
	res, err := promote(ctx, c, url.Values{"include": {"Diff"}}, map[string]any{"ChangeOrderID": r.Order.ID, "DryRun": true, "TargetStage": st.Name})
	if err != nil {
		return nil, err
	}
	p := &Preview{Stage: st.Name, Plan: res.Plan, Complete: res.Complete, Refused: res.Refused}
	for _, sr := range res.Stages {
		p.Gates = append(p.Gates, sr.Gates...)
	}
	lin, _ := r.lineage(ctx, c) // nil only when the ordered change cannot be read; kept is then skipped
	byID := map[string]Space{}
	for _, s := range r.Stages {
		for _, sp := range s.Spaces {
			byID[sp.ID] = sp
		}
	}
	for _, sr := range res.Spaces {
		sp, ok := byID[sr.SpaceID]
		if !ok {
			sp = Space{ID: sr.SpaceID, Slug: sr.SpaceSlug, Upstream: sr.UpstreamSpaceID}
		}
		out := SpacePreview{Space: sp, Action: sr.Action, Reason: sr.Reason, Stage: sr.Stage}
		switch sr.Action {
		case "Skipped":
			out.Skipped = sr.Reason
		case "Blocked", "Failed":
			out.Err = sr.Reason
		}
		current := map[string]string{}
		if lin != nil && sr.Action == "Promote" {
			current, _ = currentData(ctx, c, sr.SpaceID)
		}
		for _, u := range sr.Units {
			up := UnitPreview{UnitID: u.UnitID, Slug: u.Slug, Action: u.Action, Reason: u.Reason, Fields: u.Fields, Err: u.Err}
			switch u.Action {
			case "Unchanged", "Skip", "Mark":
				up.NoChange = true
			case "Clone":
				up.New = true
			default:
				up.NoChange = len(u.Fields) == 0 && u.Reason == "NoChange"
			}
			if up.Err == "" && lin != nil && !up.New && sr.Action == "Promote" {
				if root, ok := lin.rootChange(ctx, c, r, sr.SpaceID, u.UnitID); ok {
					up.Kept = keptFields(root.Fields, u.Fields, current[u.UnitID], u.Conflicts, func() map[string]map[string]bool {
						return unitProtection(ctx, c, sr.SpaceID, u.UnitID)
					})
				}
			}
			out.Units = append(out.Units, up)
		}
		for _, l := range sr.Links {
			switch {
			case l.Action == "Create":
				out.Links.Create++
			case l.Action == "Unchanged" && l.Reason == "Adopted":
				out.Links.Adopted++
			case l.Action == "Skip":
				out.Links.Skipped = append(out.Links.Skipped, l.Slug+": "+l.Reason)
			case l.Action == "Orphaned":
				out.Links.Orphaned = append(out.Links.Orphaned, l.Slug)
			}
		}
		sort.SliceStable(out.Units, func(i, j int) bool {
			ci, cj := !out.Units[i].NoChange || len(out.Units[i].Kept) > 0, !out.Units[j].NoChange || len(out.Units[j].Kept) > 0
			if ci != cj {
				return ci
			}
			return out.Units[i].Slug < out.Units[j].Slug
		})
		p.Spaces = append(p.Spaces, out)
	}
	return p, nil
}

// currentData reads a space's unit data in one call, by unit ID.
func currentData(ctx context.Context, c Client, spaceID string) (map[string]string, error) {
	rows, err := c.List(ctx, "/unit_data", url.Values{"where": {fmt.Sprintf("SpaceID = '%s'", spaceID)}})
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, row := range rows {
		out[str(row["UnitID"])] = str(row["Data"])
	}
	return out, nil
}

// keptFields are the ordered change's field changes the merge does not
// carry into this unit: not among the fields the dry run changes, and the
// current value is not already the upstream's. A path the server withheld
// (Conflicts) is protected and says why; otherwise protection is looked up
// in the unit's MutationSources, lazily, since most units keep nothing.
func keptFields(upstream, would []FieldChange, current string, conflicts []Conflict, protection func() map[string]map[string]bool) []KeptField {
	if len(upstream) == 0 {
		return nil
	}
	changing := map[string]bool{}
	for _, f := range would {
		changing[f.Doc+"|"+f.Path] = true
	}
	withheld := map[string]Conflict{}
	for _, c := range conflicts {
		withheld[c.Doc+"|"+c.Path] = c
	}
	values, _ := Values(current)
	var out []KeptField
	var prot map[string]map[string]bool
	for _, f := range upstream {
		if f.Path == "" || changing[f.Doc+"|"+f.Path] {
			continue
		}
		cur := values[f.Doc][f.Local]
		if cur == f.After {
			continue // already there
		}
		if f.After == "" {
			continue // the upstream removed it; not a kept value in the sense that matters here
		}
		k := KeptField{Doc: f.Doc, Path: f.Path, Current: cur, Upstream: f.After}
		if c, ok := withheld[f.Doc+"|"+f.Resolved]; ok {
			k.Protected, k.Why = true, firstNonEmpty(c.Reason, c.Details)
		} else {
			if prot == nil && protection != nil {
				prot = protection()
			}
			k.Protected = isProtected(f, prot)
		}
		out = append(out, k)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Doc+out[i].Path < out[j].Doc+out[j].Path })
	return out
}

// protectedPaths reads a MutationSources list into resource key → protected
// paths, keyed the way the server names resources ("" when unknown).
func protectedPaths(v any) map[string]map[string]bool {
	out := map[string]map[string]bool{}
	for _, rm := range list(v) {
		key := docName(rm["Resource"])
		pm, _ := rm["PathMutationMap"].(map[string]any)
		for path, mi := range pm {
			m, _ := mi.(map[string]any)
			if m != nil && m["Protected"] == true {
				if out[key] == nil {
					out[key] = map[string]bool{}
				}
				out[key][path] = true
			}
		}
	}
	return out
}

// isProtected says whether a recorded protection covers a path: the same
// resolved path, or an ancestor of it.
func isProtected(f FieldChange, protected map[string]map[string]bool) bool {
	for res, paths := range protected {
		if res != "" && f.Doc != "" && res != f.Doc {
			continue
		}
		for p := range paths {
			for _, mp := range []string{f.Resolved, f.Path} {
				if mp == p || len(mp) > len(p) && strings.HasPrefix(mp, p) && mp[len(p)] == '.' {
					return true
				}
			}
		}
	}
	return false
}

// ---- promote

// Outcome is what one space's promotion did.
type Outcome struct {
	Space   Space
	Action  string // Promote, Unchanged, Skipped, Blocked, Failed
	Reason  string
	Units   int      // units the server acted on
	Changed int      // units written: upgraded, cloned, emptied, revived, invoked, resolved
	Marked  int      // units covered but carrying no change
	Added   []string // clones
	Errors  []string // per-unit and per-link errors, slug: message
	Err     string   // the space was Blocked or Failed
	Skipped string   // why nothing was done (the base itself, out of scope)
}

// PromoteStage promotes the change into one stage: one request, which the
// server runs space by space in upstream order, continuing past a failure
// and refusing with the gates before writing anything. expectedPlan is the
// Plan of the dry run the reader saw; the server refuses (412) when it would
// now do anything different. The result is also returned so a caller can
// read the gates of a refusal.
func PromoteStage(ctx context.Context, c Client, r *Rollout, stage int, expectedPlan string) ([]Outcome, *PlanResult, error) {
	if stage <= 0 || stage >= len(r.Stages) {
		return nil, nil, fmt.Errorf("no such stage")
	}
	req := map[string]any{"ChangeOrderID": r.Order.ID, "TargetStage": r.Stages[stage].Name}
	if expectedPlan != "" {
		req["ExpectedPlan"] = expectedPlan
	}
	res, err := promote(ctx, c, nil, req)
	if err != nil {
		return nil, nil, err
	}
	if res.Refused {
		var reasons []string
		for _, st := range res.Stages {
			for _, g := range st.Gates {
				if !g.OK {
					reasons = append(reasons, g.Reason)
				}
			}
		}
		return nil, res, fmt.Errorf("promote refused by the gates: %s", strings.Join(reasons, "; "))
	}
	byID := map[string]Space{}
	for _, s := range r.Stages {
		for _, sp := range s.Spaces {
			byID[sp.ID] = sp
		}
	}
	var out []Outcome
	for _, sr := range res.Spaces {
		sp, ok := byID[sr.SpaceID]
		if !ok {
			sp = Space{ID: sr.SpaceID, Slug: sr.SpaceSlug}
		}
		o := Outcome{Space: sp, Action: sr.Action, Reason: sr.Reason}
		switch sr.Action {
		case "Skipped":
			o.Skipped = sr.Reason
		case "Blocked", "Failed":
			o.Err = sr.Reason
		}
		for _, u := range sr.Units {
			if u.Err != "" {
				o.Errors = append(o.Errors, u.Slug+": "+u.Err)
				continue
			}
			switch u.Action {
			case "Unchanged", "Skip":
				continue
			case "Mark":
				o.Marked++
			case "Clone":
				o.Changed++
				o.Added = append(o.Added, u.Slug)
			default:
				o.Changed++
			}
			o.Units++
		}
		for _, l := range sr.Links {
			if l.Err != "" {
				o.Errors = append(o.Errors, "link "+l.Slug+": "+l.Err)
			}
		}
		out = append(out, o)
	}
	return out, res, nil
}

// PromoteCommands are the CLI lines a promote of this stage stands for.
func PromoteCommands(r *Rollout, stage int, expectedPlan string) []string {
	if stage <= 0 || stage >= len(r.Stages) {
		return nil
	}
	line := fmt.Sprintf("cub variant promote --change-order %s --target-stage %s", r.Order.Ref(), r.Stages[stage].Name)
	if expectedPlan != "" {
		line += " --expected-plan " + expectedPlan
	}
	return []string{line}
}

// PromoteRequest is the request body a promote of this stage sends, for
// showing before the confirm.
func PromoteRequest(r *Rollout, stage int, expectedPlan string) string {
	req := map[string]any{"ChangeOrderID": r.Order.ID, "TargetStage": r.Stages[stage].Name}
	if expectedPlan != "" {
		req["ExpectedPlan"] = expectedPlan
	}
	b, _ := json.Marshal(req)
	return string(b)
}

var _ = cubclient.Row{} // the package keeps rows as cubclient keeps them
