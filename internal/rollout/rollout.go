// Package rollout derives a rollout -- a ChangeOrder moving through the
// ChangeWorkflow it was created under -- from what the server stores.
//
// Nothing stores a stage. The server derives ResolvedSpaceIDs, ReleasedSpaceIDs
// and State when a ChangeOrder is read; everything about stages, the next hop
// and its gates is the client's reading, and this package reads it the way
// `cub variant promote` and `cub changeorder get` do (public/cmd/cub/
// variant_promote.go), down to the refusal messages, so that what commander
// shows is what the CLI would say.
package rollout

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/confighub/cub-commander/internal/cubclient"
)

// Client is the slice of cubclient.Client this package needs; tests pass a
// MemClient.
type Client interface {
	List(ctx context.Context, path string, q url.Values) ([]cubclient.Row, error)
	GetRaw(ctx context.Context, path string) (string, error)
}

const (
	annWorkflowUnit = "confighub.com/change-workflow-unit-id"
	annWorkflowRev  = "confighub.com/change-workflow-revision-num"
	annLiveStatus   = "confighub.com/live-status"
	labelComponent  = "Component"

	// The prerequisites a stage may declare, as the CLI knows them.
	PrereqReleased = "released"
	PrereqHealthy  = "healthy"
	// PrereqTaken is the implicit gate every stage has: the previous stage
	// must have taken the change. It is listed so a tally reads "1 of 2".
	PrereqTaken = "taken"
)

// The console states, in the Web UI's words so the two surfaces agree.
const (
	StateReady       = "Ready to Promote"
	StateDegraded    = "Degraded"
	StateBlocked     = "Unreleased changes"
	StateProgressing = "Progressing"
	StateComplete    = "Complete"
	StateAborted     = "Aborted"
	StateNoWorkflow  = "No ChangeWorkflow"
	StateUnknown     = "Not reported"
	NoBlocker        = "No blocker."
)

// Order is a ChangeOrder row, read once.
type Order struct {
	ID, Slug, SpaceID, SpaceSlug string
	Description, State           string
	AbortedReason, CreatedAt     string
	StartTagID, EndTagID         string
	InScope, Resolved, Released  []string
	Annotations                  map[string]string
	Skipped                      map[string]string
}

// ParseOrder reads an extended ChangeOrder row (entity-keyed, as the list
// API returns it and cubclient keeps it).
func ParseOrder(row cubclient.Row) Order {
	co, _ := row["ChangeOrder"].(map[string]any)
	if co == nil {
		co = row
	}
	o := Order{
		ID:            str(co["ChangeOrderID"]),
		Slug:          str(co["Slug"]),
		SpaceID:       str(co["SpaceID"]),
		Description:   str(co["Description"]),
		State:         str(co["State"]),
		AbortedReason: str(co["AbortedReason"]),
		CreatedAt:     str(co["CreatedAt"]),
		StartTagID:    str(co["StartTagID"]),
		EndTagID:      str(co["EndTagID"]),
		InScope:       strs(co["InScopeSpaceIDs"]),
		Resolved:      strs(co["ResolvedSpaceIDs"]),
		Released:      strs(co["ReleasedSpaceIDs"]),
		Annotations:   strmap(co["Annotations"]),
		Skipped:       strmap(co["SkippedUnits"]),
	}
	if sp, ok := row["Space"].(map[string]any); ok {
		o.SpaceSlug = str(sp["Slug"])
	}
	return o
}

// Ref is the "space/slug" form the CLI takes for --change-order.
func (o Order) Ref() string {
	if o.SpaceSlug != "" {
		return o.SpaceSlug + "/" + o.Slug
	}
	return o.Slug
}

// Workflow is the parsed ChangeWorkflow definition (SDK core/changeworkflow).
type Workflow struct {
	Name   string
	Stages []Stage
	Final  []string // final.prerequisites
}

type Stage struct {
	Name          string
	WhereSpace    string
	Prerequisites []string
}

// Health is a space's live-status annotation as argobot writes it.
type Health struct {
	Present    bool
	Sync       string `json:"syncStatus"`
	Phase      string `json:"operationPhase"`
	Status     string `json:"healthStatus"`
	ObservedAt string `json:"observedAt"`
	Message    string `json:"message"`
	Source     string `json:"source"`
}

// OK is the CLI's reading: Synced, Succeeded, Healthy.
func (h Health) OK() bool {
	return h.Present && h.Sync == "Synced" && h.Phase == "Succeeded" && h.Status == "Healthy"
}

// Space is one member of a stage with its three bits.
type Space struct {
	ID, Slug, Variant string
	Labels            map[string]string
	Releasable        bool   // has a ReleaseTargetID
	Upstream          string // the UpstreamSpaceID annotation cub variant create stamps
	Taken, Released   bool
	Health            Health
}

// StageState is a stage with its resolved members and counts.
type StageState struct {
	Stage
	Source bool // the base the change was authored in, drawn before the workflow's stages
	Spaces []Space
}

func (s StageState) Counts() (taken, released, healthy int) {
	for _, sp := range s.Spaces {
		if sp.Taken {
			taken++
		}
		if sp.Released {
			released++
		}
		if sp.Health.OK() {
			healthy++
		}
	}
	return
}

// Gate is one prerequisite of the next stage, evaluated over the previous
// stage's members, with the CLI's refusal text when it fails.
type Gate struct {
	Name   string
	OK     bool
	Reason string
}

func Tally(gates []Gate) (ok, total int) {
	for _, g := range gates {
		if g.OK {
			ok++
		}
	}
	return ok, len(gates)
}

func Open(gates []Gate) bool {
	ok, total := Tally(gates)
	return ok == total
}

// Rollout is the derived reading of one ChangeOrder.
type Rollout struct {
	Order       Order
	Workflow    *Workflow
	WorkflowRef string // "unit-slug @rev N"
	Component   string
	// Stages[0] is the source (the base space); the rest are the workflow's.
	Stages []StageState
	// Next indexes Stages: the first stage not every member has taken; -1
	// when every stage has it. Gates are Next's entry gates, or final's when
	// Next is -1.
	Next      int
	Gates     []Gate
	Completed bool
	State     string
	Blocker   string
	// Err is set when the workflow could not be read; Stages then holds only
	// the source and State says so.
	Err string
}

// Reached is the last workflow stage the change has reached, the CLI's
// "Stage" column: "" while it has not finished the first one.
func (r *Rollout) Reached() string {
	if r.Workflow == nil || len(r.Stages) < 2 {
		return ""
	}
	switch {
	case r.Next < 0:
		return r.Stages[len(r.Stages)-1].Name
	case r.Next > 1:
		return r.Stages[r.Next-1].Name
	}
	return ""
}

// NextName is the stage the change would advance into, or "".
func (r *Rollout) NextName() string {
	if r.Next < 0 || r.Next >= len(r.Stages) {
		return ""
	}
	return r.Stages[r.Next].Name
}

// Cache remembers what is the same across rollouts: parsed workflow
// revisions and the spaces a stage clause selects.
type Cache struct {
	workflows map[string]*Workflow
	spaces    map[string][]cubclient.Row
	wfErr     map[string]error
}

func NewCache() *Cache {
	return &Cache{workflows: map[string]*Workflow{}, spaces: map[string][]cubclient.Row{}, wfErr: map[string]error{}}
}

// Load derives the rollout for one ChangeOrder row.
func Load(ctx context.Context, c Client, cache *Cache, row cubclient.Row) (*Rollout, error) {
	if cache == nil {
		cache = NewCache()
	}
	o := ParseOrder(row)
	r := &Rollout{Order: o, Next: -1}

	base, err := spaceByID(ctx, c, o.SpaceID)
	if err != nil {
		return nil, err
	}
	if o.SpaceSlug == "" {
		o.SpaceSlug = base.Slug
		r.Order.SpaceSlug = base.Slug
	}
	base.Taken = true // the change was authored here
	base.Released = true
	r.Stages = []StageState{{Stage: Stage{Name: "source"}, Source: true, Spaces: []Space{base}}}

	if o.AbortedReason != "" {
		r.State, r.Blocker = StateAborted, "Aborted: "+o.AbortedReason
	}

	unitID, ok := o.Annotations[annWorkflowUnit]
	if !ok {
		if r.State == "" {
			r.State, r.Blocker = StateNoWorkflow, "No ChangeWorkflow governs this rollout, so it has no stages."
		}
		return r, nil
	}
	wf, ref, err := cache.workflow(ctx, c, unitID, o.Annotations[annWorkflowRev])
	if err != nil {
		r.Err = err.Error()
		if r.State == "" {
			r.State, r.Blocker = StateUnknown, "Could not read the ChangeWorkflow this rollout names: "+err.Error()
		}
		return r, nil
	}
	r.Workflow, r.WorkflowRef = wf, ref

	r.Component = base.Labels[labelComponent]
	if r.Component == "" {
		r.Err = fmt.Sprintf("Space '%s' has no %s label, so there is no component for a ChangeWorkflow's stages to select within", base.Slug, labelComponent)
		if r.State == "" {
			r.State, r.Blocker = StateUnknown, r.Err
		}
		return r, nil
	}

	for _, st := range wf.Stages {
		rows, err := cache.stageSpaces(ctx, c, st, r.Component)
		if err != nil {
			r.Err = err.Error()
			if r.State == "" {
				r.State, r.Blocker = StateUnknown, err.Error()
			}
			return r, nil
		}
		ss := StageState{Stage: st}
		for _, row := range rows {
			sp := parseSpace(row)
			if len(o.InScope) > 0 && !contains(o.InScope, sp.ID) {
				continue
			}
			sp.Taken = contains(o.Resolved, sp.ID)
			sp.Released = contains(o.Released, sp.ID)
			ss.Spaces = append(ss.Spaces, sp)
		}
		sort.Slice(ss.Spaces, func(i, j int) bool { return ss.Spaces[i].Slug < ss.Spaces[j].Slug })
		r.Stages = append(r.Stages, ss)
	}
	derive(r)
	return r, nil
}

// derive is the pure part: next stage, gates, completion, console state.
func derive(r *Rollout) {
	stages := r.Stages[1:] // the workflow's
	r.Next = -1
	for i, st := range stages {
		reached := len(st.Spaces) > 0
		for _, sp := range st.Spaces {
			if !sp.Taken {
				reached = false
				break
			}
		}
		if !reached {
			r.Next = i + 1
			break
		}
	}
	if r.Next > 0 {
		next := r.Stages[r.Next]
		var prev *StageState
		if r.Next > 1 {
			prev = &r.Stages[r.Next-1]
		}
		r.Gates = gates(next.Name, next.Prerequisites, prev, r.Order, true)
	}
	// Completed: the last stage satisfies final.prerequisites.
	if len(stages) > 0 {
		last := &r.Stages[len(r.Stages)-1]
		final := gates("final", r.Workflow.Final, last, r.Order, false)
		r.Completed = len(last.Spaces) > 0 && Open(final)
		if r.Next < 0 {
			r.Gates = final
		}
	}
	if r.State != "" { // aborted
		return
	}
	var failing *Gate
	for i := range r.Gates {
		if !r.Gates[i].OK {
			failing = &r.Gates[i]
			break
		}
	}
	switch {
	case failing == nil && r.Next < 0:
		r.State, r.Blocker = StateComplete, NoBlocker
	case failing == nil:
		r.State, r.Blocker = StateReady, NoBlocker
	case failing.Name == PrereqHealthy:
		r.State, r.Blocker = StateDegraded, failing.Reason
	case failing.Name == PrereqReleased:
		r.State, r.Blocker = StateBlocked, failing.Reason
	default:
		r.State, r.Blocker = StateProgressing, failing.Reason
	}
}

// gates evaluates a stage's entry gates over the previous stage's members,
// in the CLI's order and words (validateStageEntryGates,
// checkVariantPrerequisites). The first stage has no previous stage and so
// no gates: the change is promoting out of the base it was authored in. When
// entry is true a missing previous stage yields the source's trivial gate so
// the tally reads "1 of 1" the way the UI draws it.
func gates(stage string, prereqs []string, prev *StageState, o Order, entry bool) []Gate {
	if prev == nil {
		if entry {
			return []Gate{{Name: PrereqTaken, OK: true, Reason: "the base has the change"}}
		}
		return nil
	}
	out := []Gate{{Name: PrereqTaken, OK: true}}
	for _, p := range prereqs {
		out = append(out, Gate{Name: p, OK: true})
	}
	if len(prev.Spaces) == 0 {
		out[0] = Gate{Name: PrereqTaken, Reason: fmt.Sprintf("unable to promote to stage '%s', its previous stage '%s' selects no Space", stage, prev.Name)}
		return out
	}
	fail := func(name, reason string) {
		for i := range out {
			if out[i].Name == name && out[i].OK {
				out[i].OK, out[i].Reason = false, reason
			}
		}
	}
	for _, sp := range prev.Spaces {
		v := sp.Variant
		if !sp.Taken {
			fail(PrereqTaken, fmt.Sprintf("unable to promote to stage '%s', Variant '%s' has not taken change order '%s'", stage, v, o.Slug))
			continue
		}
		for _, p := range prereqs {
			switch p {
			case PrereqHealthy:
				if reason := unhealthy(sp); reason != "" {
					fail(p, reason)
				}
			case PrereqReleased:
				switch {
				case !sp.Releasable:
					fail(p, fmt.Sprintf("unable to promote to stage '%s', Variant '%s' cannot have any released changes, missing ReleaseTargetID", stage, v))
				case !sp.Released:
					fail(p, fmt.Sprintf("unable to promote to stage '%s', Variant '%s' has taken change order '%s' but has not released it", stage, v, o.Slug))
				}
			default:
				fail(p, fmt.Sprintf("unrecognized prerequisite for Stage '%s': '%s'", stage, p))
			}
		}
	}
	return out
}

// unhealthy is checkVariantIsHealthy's verdict as text, "" when healthy.
func unhealthy(sp Space) string {
	v := sp.Variant
	switch {
	case !sp.Releasable:
		return fmt.Sprintf("Variant '%s' has no ReleaseTargetID, so its health cannot be determined", v)
	case !sp.Health.Present:
		return fmt.Sprintf("live-status not found for Variant '%s'", v)
	case sp.Health.Sync != "Synced":
		return fmt.Sprintf("Variant '%s' is not synced", v)
	case sp.Health.Phase != "Succeeded":
		return fmt.Sprintf("Variant '%s' has not succeeded in deployment", v)
	case sp.Health.Status != "Healthy":
		return fmt.Sprintf("Variant '%s' is not healthy", v)
	}
	return ""
}

var componentPredicate = regexp.MustCompile(`(?i)\bLabels\.` + labelComponent + `\b`)

// stageWhere is the CLI's stageWhereSpace: the stage's selector conjoined
// with the component, refusing a selector that names the component itself.
func stageWhere(st Stage, component string) (string, error) {
	if componentPredicate.MatchString(st.WhereSpace) {
		return "", fmt.Errorf("stage '%s' names Labels.%s in its whereSpace %q: the component is the change order's own and is appended to every stage's selector, so remove the predicate", st.Name, labelComponent, st.WhereSpace)
	}
	cw := fmt.Sprintf("Labels.%s = '%s'", labelComponent, component)
	if st.WhereSpace == "" {
		return cw, nil
	}
	return st.WhereSpace + " AND " + cw, nil
}

const spaceSelect = "SpaceID,Slug,Labels,Annotations,ReleaseTargetID"

func (c *Cache) stageSpaces(ctx context.Context, cl Client, st Stage, component string) ([]cubclient.Row, error) {
	where, err := stageWhere(st, component)
	if err != nil {
		return nil, err
	}
	if rows, ok := c.spaces[where]; ok {
		return rows, nil
	}
	rows, err := cl.List(ctx, "/space", url.Values{"where": {where}, "select": {spaceSelect}})
	if err != nil {
		return nil, fmt.Errorf("failed to resolve the Spaces of Stage '%s': %w", st.Name, err)
	}
	c.spaces[where] = rows
	return rows, nil
}

// workflow reads the pinned revision of the workflow unit and parses it.
func (c *Cache) workflow(ctx context.Context, cl Client, unitID, rev string) (*Workflow, string, error) {
	key := unitID + "@" + rev
	if wf, ok := c.workflows[key]; ok {
		return wf, wfRef(wf, rev), nil
	}
	if err, ok := c.wfErr[key]; ok {
		return nil, "", err
	}
	wf, err := loadWorkflow(ctx, cl, unitID, rev)
	if err != nil {
		c.wfErr[key] = err
		return nil, "", err
	}
	c.workflows[key] = wf
	return wf, wfRef(wf, rev), nil
}

func wfRef(wf *Workflow, rev string) string { return wf.Name + " @rev " + rev }

func loadWorkflow(ctx context.Context, cl Client, unitID, rev string) (*Workflow, error) {
	units, err := cl.List(ctx, "/unit", url.Values{"where": {fmt.Sprintf("UnitID = '%s'", unitID)}, "select": {"UnitID,SpaceID,Slug"}})
	if err != nil {
		return nil, fmt.Errorf("failed to fetch ChangeWorkflow unit %s: %w", unitID, err)
	}
	if len(units) == 0 {
		return nil, fmt.Errorf("ChangeWorkflow unit %s not found", unitID)
	}
	u, _ := units[0]["Unit"].(map[string]any)
	if u == nil {
		u = units[0]
	}
	spaceID, slug := str(u["SpaceID"]), str(u["Slug"])
	base := "/space/" + spaceID + "/unit/" + unitID
	revs, err := cl.List(ctx, base+"/revision", url.Values{"where": {"RevisionNum = " + rev}, "select": {"RevisionID,RevisionNum"}})
	if err != nil || len(revs) == 0 {
		return nil, fmt.Errorf("failed to fetch revision %s of ChangeWorkflow unit %s", rev, slug)
	}
	rv, _ := revs[0]["Revision"].(map[string]any)
	if rv == nil {
		rv = revs[0]
	}
	data, err := cl.GetRaw(ctx, base+"/revision/"+str(rv["RevisionID"])+"/data")
	if err != nil {
		return nil, fmt.Errorf("failed to fetch data of revision %s of unit %s: %w", rev, slug, err)
	}
	wf, err := ParseWorkflow(data)
	if err != nil {
		return nil, fmt.Errorf("unit %s revision %s does not carry a ChangeWorkflow definition: %w", slug, rev, err)
	}
	if wf.Name == "" {
		wf.Name = slug
	}
	return wf, nil
}

// ParseWorkflow parses a ChangeWorkflow document.
func ParseWorkflow(doc string) (*Workflow, error) {
	var raw struct {
		Kind     string `yaml:"kind"`
		Metadata struct {
			Name string `yaml:"name"`
		} `yaml:"metadata"`
		Spec struct {
			Stages []struct {
				Name          string   `yaml:"name"`
				WhereSpace    string   `yaml:"whereSpace"`
				Prerequisites []string `yaml:"prerequisites"`
			} `yaml:"stages"`
			Final struct {
				Prerequisites []string `yaml:"prerequisites"`
			} `yaml:"final"`
		} `yaml:"spec"`
	}
	if err := yaml.Unmarshal([]byte(doc), &raw); err != nil {
		return nil, err
	}
	if raw.Kind != "" && raw.Kind != "ChangeWorkflow" {
		return nil, fmt.Errorf("kind is %s, not ChangeWorkflow", raw.Kind)
	}
	wf := &Workflow{Name: raw.Metadata.Name, Final: raw.Spec.Final.Prerequisites}
	for _, s := range raw.Spec.Stages {
		wf.Stages = append(wf.Stages, Stage{Name: s.Name, WhereSpace: s.WhereSpace, Prerequisites: s.Prerequisites})
	}
	if len(wf.Stages) == 0 {
		return nil, fmt.Errorf("no stages")
	}
	return wf, nil
}

func spaceByID(ctx context.Context, cl Client, id string) (Space, error) {
	rows, err := cl.List(ctx, "/space", url.Values{"where": {fmt.Sprintf("SpaceID = '%s'", id)}, "select": {spaceSelect}})
	if err != nil {
		return Space{}, fmt.Errorf("failed to fetch Space %s: %w", id, err)
	}
	if len(rows) == 0 {
		return Space{}, fmt.Errorf("Space %s not found", id)
	}
	return parseSpace(rows[0]), nil
}

func parseSpace(row cubclient.Row) Space {
	sp, _ := row["Space"].(map[string]any)
	if sp == nil {
		sp = row
	}
	s := Space{ID: str(sp["SpaceID"]), Slug: str(sp["Slug"]), Labels: strmap(sp["Labels"])}
	s.Releasable = str(sp["ReleaseTargetID"]) != ""
	s.Variant = s.Labels["Variant"]
	if s.Variant == "" {
		s.Variant = s.Slug
	}
	s.Upstream = strmap(sp["Annotations"])["UpstreamSpaceID"]
	if ls := strmap(sp["Annotations"])[annLiveStatus]; ls != "" {
		if json.Unmarshal([]byte(ls), &s.Health) == nil {
			s.Health.Present = true
		}
	}
	return s
}

// CubCommands are the CLI lines behind the reading and the actions on it.
func (r *Rollout) CubCommands() []string {
	out := []string{fmt.Sprintf("cub changeorder get %s --space %s", r.Order.Slug, r.Order.SpaceSlug)}
	if r.Workflow != nil && r.Next > 0 {
		out = append(out, fmt.Sprintf("cub variant promote --change-order %s --target-stage %s --dry-run -o mutations", r.Order.Ref(), r.NextName()))
	}
	return out
}

func str(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case nil:
		return ""
	case float64:
		return strings.TrimSuffix(fmt.Sprintf("%.0f", x), ".0")
	}
	return fmt.Sprint(v)
}

func strs(v any) []string {
	items, _ := v.([]any)
	out := make([]string, 0, len(items))
	for _, it := range items {
		out = append(out, str(it))
	}
	return out
}

func strmap(v any) map[string]string {
	m, _ := v.(map[string]any)
	out := make(map[string]string, len(m))
	for k, val := range m {
		out[k] = str(val)
	}
	return out
}

func contains(list []string, s string) bool { return slices.Contains(list, s) }

// Text renders the reading as plain text, for `-e` and for logs.
func (r *Rollout) Text() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s  %s\n", r.Order.Slug, r.Order.Description)
	fmt.Fprintf(&b, "state %s · %s · blocker: %s\n", r.Order.State, r.State, r.Blocker)
	if r.Workflow == nil {
		return b.String()
	}
	fmt.Fprintf(&b, "workflow %s · component %s · completed %v\n\n", r.WorkflowRef, r.Component, r.Completed)
	fmt.Fprintf(&b, "%-10s %-7s %-9s %-8s %s\n", "STAGE", "TAKEN", "RELEASED", "HEALTHY", "SPACES")
	for i, st := range r.Stages {
		mark := " "
		if i == r.Next {
			mark = "▲"
		}
		n := len(st.Spaces)
		taken, released, healthy := st.Counts()
		var names []string
		for _, sp := range st.Spaces {
			flags := ""
			if sp.Taken {
				flags += "T"
			}
			if sp.Released {
				flags += "R"
			}
			if sp.Health.OK() {
				flags += "H"
			} else if sp.Health.Present {
				flags += "!"
			}
			if flags != "" {
				flags = "[" + flags + "]"
			}
			names = append(names, sp.Slug+flags)
		}
		fmt.Fprintf(&b, "%s%-9s %-7s %-9s %-8s %s\n", mark, st.Name, fmt.Sprintf("%d/%d", taken, n), fmt.Sprintf("%d/%d", released, n), fmt.Sprintf("%d/%d", healthy, n), strings.Join(names, " "))
	}
	ok, total := Tally(r.Gates)
	switch {
	case r.Next > 0:
		fmt.Fprintf(&b, "\nnext: %s · gates %d of %d satisfied\n", r.NextName(), ok, total)
	default:
		fmt.Fprintf(&b, "\nevery stage has taken it · final %d of %d satisfied\n", ok, total)
	}
	for _, g := range r.Gates {
		glyph := "✓"
		if !g.OK {
			glyph = "✗"
		}
		reason := g.Reason
		if reason == "" {
			reason = g.Name
		}
		fmt.Fprintf(&b, "  %s %s\n", glyph, reason)
	}
	b.WriteString("\n" + strings.Join(r.CubCommands(), "\n") + "\n")
	return b.String()
}
