// Package change order reads a change order -- a ChangeOrder moving through the
// ChangeWorkflow it was created under -- the way the server now tells it.
//
// The server owns the reading: the ChangeOrder carries a copy of its
// workflow, the Stage it has reached, and the promotions and releases that
// got it there; stage membership is each stage's selector intersected with
// the order's InScopeSpaceIDs; health is the LiveStatus of a space's latest
// published Release; and the entry gates of the next stage come from a dry
// run of POST /promote, which evaluates every (prerequisite, space) pair in
// the CLI's words and refuses with them. This package asks for those and
// arranges them for a screen; it derives nothing the server would derive
// differently.
package changeorder

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"slices"
	"sort"
	"strings"
	"sync"

	"github.com/confighub/cub-commander/internal/cubclient"
)

// Client is the slice of cubclient.Client this package needs; tests pass a
// MemClient. Send is used for the promote and release requests.
type Client interface {
	List(ctx context.Context, path string, q url.Values) ([]cubclient.Row, error)
	GetRaw(ctx context.Context, path string) (string, error)
	Send(ctx context.Context, method, path string, q url.Values, contentType, body string) (int, []byte, error)
}

// The prerequisites the server evaluates, as a promote result names them.
const (
	PrereqPromoted  = "Promoted"
	PrereqReleased  = "Released"
	PrereqHealthy   = "Healthy"
	PrereqValidated = "Validated"
	// StageCompleted is what the server records as a ChangeOrder's Stage once
	// the last stage satisfies the workflow's final prerequisites.
	StageCompleted = "Completed"
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
	Description, State, Stage    string
	UpdateType                   string
	AbortedReason, CreatedAt     string
	StartTagID, EndTagID         string
	WorkflowID                   string
	InScope, Resolved, Released  []string
	Skipped                      map[string]string
	// Workflow is the copy the order carries, taken when the workflow was
	// associated; nil when no workflow governs the order.
	Workflow   *Workflow
	Promotions []Promotion
	Failures   []PromotionFailure
	Overrides  []Override
	Releases   []OrderRelease
}

// Workflow is a ChangeWorkflowSpec: the stages, the final gates, and the
// names of the custom and attestation prerequisites a stage may cite.
type Workflow struct {
	Stages []Stage
	Final  []string
	Custom map[string]string // name → description
	Attest map[string]string // name → description
}

type Stage struct {
	Name                 string
	WhereSpace           string
	Prerequisites        []string
	ReleasePrerequisites []string
}

// Promotion is one recorded write of the change into spaces.
type Promotion struct {
	At, Stage, UserID string
	SpaceIDs          []string
}

// PromotionFailure is one recorded promotion that did not complete.
type PromotionFailure struct {
	At, Stage, UserID string
	Spaces            []FailedSpace
}

type FailedSpace struct {
	ID, Slug, Action, Reason string
	Units                    []string // "slug: error"
}

// Override is one recorded forced promotion.
type Override struct {
	At, Stage, UserID, Reason string
	SpaceIDs, FailedGates     []string
}

// OrderRelease is the earliest published Release of a space that carries the change.
type OrderRelease struct {
	SpaceID, ReleaseID string
	ReleaseNum         int
}

// ParseOrder reads an extended ChangeOrder row (entity-keyed, as the list
// API returns it and cubclient keeps it).
func ParseOrder(row cubclient.Row) Order {
	co := own(row, "ChangeOrder")
	o := Order{
		ID:            str(co["ChangeOrderID"]),
		Slug:          str(co["Slug"]),
		SpaceID:       str(co["SpaceID"]),
		SpaceSlug:     str(co["SpaceSlug"]),
		Description:   str(co["Description"]),
		State:         str(co["State"]),
		Stage:         str(co["Stage"]),
		UpdateType:    str(co["UpdateType"]),
		AbortedReason: str(co["AbortedReason"]),
		CreatedAt:     str(co["CreatedAt"]),
		StartTagID:    str(co["StartTagID"]),
		EndTagID:      str(co["EndTagID"]),
		WorkflowID:    str(co["ChangeWorkflowID"]),
		InScope:       strs(co["InScopeSpaceIDs"]),
		Resolved:      strs(co["ResolvedSpaceIDs"]),
		Released:      strs(co["ReleasedSpaceIDs"]),
		Skipped:       strmap(co["SkippedUnits"]),
		Workflow:      parseWorkflow(co["ChangeWorkflow"]),
	}
	if sp, ok := row["Space"].(map[string]any); ok && str(sp["Slug"]) != "" {
		o.SpaceSlug = str(sp["Slug"])
	}
	for _, p := range list(co["Promotions"]) {
		o.Promotions = append(o.Promotions, Promotion{At: str(p["PromotedAt"]), Stage: str(p["Stage"]), UserID: str(p["UserID"]), SpaceIDs: strs(p["SpaceIDs"])})
	}
	for _, f := range list(co["PromotionFailures"]) {
		pf := PromotionFailure{At: str(f["FailedAt"]), Stage: str(f["Stage"]), UserID: str(f["UserID"])}
		for _, s := range list(f["Spaces"]) {
			fs := FailedSpace{ID: str(s["SpaceID"]), Slug: str(s["SpaceSlug"]), Action: str(s["Action"]), Reason: firstNonEmpty(str(s["Reason"]), str(s["Error"]))}
			for _, u := range list(s["Units"]) {
				fs.Units = append(fs.Units, str(u["Slug"])+": "+str(u["Error"]))
			}
			pf.Spaces = append(pf.Spaces, fs)
		}
		o.Failures = append(o.Failures, pf)
	}
	for _, v := range list(co["PromotionOverrides"]) {
		o.Overrides = append(o.Overrides, Override{At: str(v["OverriddenAt"]), Stage: str(v["Stage"]), UserID: str(v["UserID"]), Reason: str(v["Reason"]), SpaceIDs: strs(v["SpaceIDs"]), FailedGates: strs(v["FailedGates"])})
	}
	for _, r := range list(co["Releases"]) {
		o.Releases = append(o.Releases, OrderRelease{SpaceID: str(r["SpaceID"]), ReleaseID: str(r["ReleaseID"]), ReleaseNum: num(r["ReleaseNum"])})
	}
	return o
}

// parseWorkflow reads the ChangeWorkflowSpec copy on an order; nil when the
// order carries none.
func parseWorkflow(v any) *Workflow {
	m, _ := v.(map[string]any)
	if m == nil {
		return nil
	}
	wf := &Workflow{Custom: map[string]string{}, Attest: map[string]string{}}
	for _, s := range list(m["Stages"]) {
		wf.Stages = append(wf.Stages, Stage{Name: str(s["Name"]), WhereSpace: str(s["WhereSpace"]), Prerequisites: strs(s["Prerequisites"]), ReleasePrerequisites: strs(s["ReleasePrerequisites"])})
	}
	if f, ok := m["Final"].(map[string]any); ok {
		wf.Final = strs(f["Prerequisites"])
	}
	for _, p := range list(m["CustomPrerequisites"]) {
		wf.Custom[str(p["Name"])] = str(p["Description"])
	}
	for _, p := range list(m["AttestationPrerequisites"]) {
		wf.Attest[str(p["Name"])] = str(p["Description"])
	}
	if len(wf.Stages) == 0 {
		return nil
	}
	return wf
}

// Ref is the "space/slug" form the CLI takes for --change-order.
func (o Order) Ref() string {
	if o.SpaceSlug != "" {
		return o.SpaceSlug + "/" + o.Slug
	}
	return o.Slug
}

// Health is the LiveStatus of a space's latest published Release, as the
// reporter (argobot) wrote it there.
type Health struct {
	Present    bool
	Sync       string // Synced, OutOfSync, Unknown
	Status     string // Healthy, Progressing, Degraded, Suspended, Missing, Unknown
	Operation  string // Running, Succeeded, Failed, or ""
	ObservedAt string
	Message    string
	Reporter   string
	ReleaseNum int
	ReleaseID  string
	// ForOrder says the latest published Release was published for this
	// change order, so the status describes the change and not what ran before.
	ForOrder bool
}

// OK is the server's Healthy gate reading: Synced and Healthy with no
// operation running or failed.
func (h Health) OK() bool {
	return h.Present && h.Sync == "Synced" && h.Status == "Healthy" && h.Operation != "Running" && h.Operation != "Failed"
}

// Space is one member of a stage with its three bits.
type Space struct {
	ID, Slug, Variant string
	Labels            map[string]string
	Releasable        bool   // has a ReleaseTargetID
	Upstream          string // UpstreamSpaceID: the space it was cloned from
	Taken, Released   bool
	Health            Health
}

// StageState is a stage with its resolved members and counts.
type StageState struct {
	Stage
	Source bool // the base the change was authored in, drawn before the workflow's stages
	Spaces []Space
}

// HealthyForChange is whether the space runs this change healthily: it has
// released the change and the live status of its latest release is good.
// Live status alone says the space is healthy on whatever it runs, which
// before the release is the previous state, so the strip does not count it.
func (sp Space) HealthyForChange() bool { return sp.Released && sp.Health.OK() }

func (s StageState) Counts() (taken, released, healthy int) {
	for _, sp := range s.Spaces {
		if sp.Taken {
			taken++
		}
		if sp.Released {
			released++
		}
		if sp.HealthyForChange() {
			healthy++
		}
	}
	return
}

// Gate is one (prerequisite, space) pair of the next stage's entry gates,
// as the server evaluated it, with its refusal text when it fails.
type Gate struct {
	Name   string // Promoted, Released, Healthy, Validated, or a custom prerequisite's name
	Space  string // the slug of the previous-stage space it was evaluated over
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

// Change order is the reading of one ChangeOrder.
type ChangeOrder struct {
	Order       Order
	Workflow    *Workflow
	WorkflowRef string // the workflow's slug (the order carries a copy of it)
	Component   string
	// Stages[0] is the source (the base space); the rest are the workflow's.
	Stages []StageState
	// Next indexes Stages: the stage the server would promote into next; -1
	// when every stage has the change. Gates are its entry gates as the dry
	// run evaluated them, or the final reading when Next is -1.
	Next      int
	Gates     []Gate
	Completed bool
	State     string
	Blocker   string
	// Plan is the gate dry run's result when the server answered one; nil
	// when it could not be asked (no workflow, aborted) or refused outright.
	Plan *PlanResult
	// Err is set when part of the reading could not be made; what could be
	// is still shown and State says so.
	Err string

	// lc is shared by copies of the reading (AfterPromote); built lazily by
	// the kept-field readings.
	lc *lineageCache
}

type lineageCache struct {
	mu  sync.Mutex
	lin *lineage
}

// Reached is the stage the server records the change as having reached:
// "" while it has not finished the first one, Completed at the end.
func (r *ChangeOrder) Reached() string { return r.Order.Stage }

// NextName is the stage the change would advance into, or "".
func (r *ChangeOrder) NextName() string {
	if r.Next <= 0 || r.Next >= len(r.Stages) {
		return ""
	}
	return r.Stages[r.Next].Name
}

// Cache remembers what is the same across the change orders of one run: the
// spaces a stage clause selects, the releases read for health, and the gate
// dry runs. Live status must be re-read on the next run, so a Cache lives
// for one statement. Workflow slugs never change and are cached for the process.
type Cache struct {
	mu     sync.Mutex
	spaces map[string][]cubclient.Row
	plans  map[string]*planAnswer
}

type planAnswer struct {
	plan *PlanResult
	err  error
}

func NewCache() *Cache {
	return &Cache{spaces: map[string][]cubclient.Row{}, plans: map[string]*planAnswer{}}
}

// workflowSlugs is the process-wide cache of ChangeWorkflowID → slug.
var workflowSlugs sync.Map

const spaceSelect = "SpaceID,Slug,Labels,Annotations,ReleaseTargetID,UpstreamSpaceID,ComponentID,Component.Slug"

// Load reads the change order for one ChangeOrder row.
func Load(ctx context.Context, c Client, cache *Cache, row cubclient.Row) (*ChangeOrder, error) {
	if cache == nil {
		cache = NewCache()
	}
	o := ParseOrder(row)
	r := &ChangeOrder{Order: o, Workflow: o.Workflow, Next: -1, lc: &lineageCache{}}

	// The base space, the workflow's name and the gate dry run follow from
	// the order alone; read them together.
	var (
		wfRef  string
		plan   *PlanResult
		planEr error
		wg     sync.WaitGroup
	)
	if o.Workflow != nil {
		wg.Add(1)
		go func() { defer wg.Done(); wfRef = workflowSlug(ctx, c, o.WorkflowID) }()
		if o.AbortedReason == "" {
			wg.Add(1)
			go func() { defer wg.Done(); plan, planEr = cache.gatePlan(ctx, c, o) }()
		}
	}
	base, component, err := spaceByID(ctx, c, o.SpaceID)
	wg.Wait()
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
	r.Component = firstNonEmpty(component, base.Labels["Component"])

	if o.AbortedReason != "" {
		r.State, r.Blocker = StateAborted, "Aborted: "+o.AbortedReason
	}
	if o.Workflow == nil {
		if r.State == "" {
			r.State, r.Blocker = StateNoWorkflow, "No ChangeWorkflow governs this change order, so it has no stages."
		}
		return r, nil
	}
	r.WorkflowRef = wfRef

	// Stage membership: each stage's selector intersected with the order's
	// scope, as the server computes it. The lookups are independent.
	wf := o.Workflow
	stageRows := make([][]cubclient.Row, len(wf.Stages))
	stageErrs := make([]error, len(wf.Stages))
	var swg sync.WaitGroup
	for i, st := range wf.Stages {
		swg.Add(1)
		go func(i int, st Stage) {
			defer swg.Done()
			stageRows[i], stageErrs[i] = cache.stageSpaces(ctx, c, st, o.InScope)
		}(i, st)
	}
	swg.Wait()
	var releasable []string
	for i, st := range wf.Stages {
		rows, err := stageRows[i], stageErrs[i]
		if err != nil {
			r.Err = err.Error()
			if r.State == "" {
				r.State, r.Blocker = StateUnknown, err.Error()
			}
			return r, nil
		}
		ss := StageState{Stage: st}
		for _, row := range rows {
			sp, _ := parseSpace(row)
			sp.Taken = contains(o.Resolved, sp.ID)
			sp.Released = contains(o.Released, sp.ID)
			if sp.Releasable {
				releasable = append(releasable, sp.ID)
			}
			ss.Spaces = append(ss.Spaces, sp)
		}
		sort.Slice(ss.Spaces, func(i, j int) bool { return ss.Spaces[i].Slug < ss.Spaces[j].Slug })
		r.Stages = append(r.Stages, ss)
	}

	// Health: the latest published release per space with a target.
	health, err := latestReleases(ctx, c, releasable)
	if err != nil {
		r.Err = err.Error()
	}
	for si := range r.Stages {
		for j := range r.Stages[si].Spaces {
			sp := &r.Stages[si].Spaces[j]
			if h, ok := health[sp.ID]; ok {
				h.ForOrder = h.ReleaseID != "" && releaseOf(o, sp.ID) == h.ReleaseID || h.ForOrder
				sp.Health = h
			}
		}
	}

	// The next stage and its gates, from the server's dry run.
	r.Plan = plan
	if planEr != nil {
		r.Err = firstNonEmpty(r.Err, planEr.Error())
	}
	derive(r, planEr)
	return r, nil
}

// releaseOf is the release the order records for a space, "" when none.
func releaseOf(o Order, spaceID string) string {
	for _, rel := range o.Releases {
		if rel.SpaceID == spaceID {
			return rel.ReleaseID
		}
	}
	return ""
}

// derive sets Next, Gates, Completed and the console state from the bits
// and the dry run. The dry run answers for the next stage; the final reading,
// which the server only records on a write, is taken from the bits so that a
// release or a health report shows up before the next promotion asks.
func derive(r *ChangeOrder, planErr error) {
	o := r.Order
	wfStages := r.Stages[1:]
	// Where the change would go next, by the bits: the first stage some
	// member has not taken. The dry run's answer replaces it when it came.
	r.Next = -1
	for i, st := range wfStages {
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
	r.Completed = o.Stage == StageCompleted
	if p := r.Plan; p != nil {
		switch {
		case p.Complete:
			r.Next = -1
		case len(p.Stages) > 0:
			for i, st := range wfStages {
				if st.Name == p.Stages[0].Name {
					r.Next = i + 1
				}
			}
			r.Gates = p.Stages[0].Gates
		}
	}
	if r.Next < 0 && len(wfStages) > 0 {
		// The final reading: the last stage's members against final.prerequisites.
		r.Gates = finalGates(r.Workflow, &r.Stages[len(r.Stages)-1], o)
		if !r.Completed && Open(r.Gates) {
			r.Completed = true // every check this reading can make holds; the server records it on the next write
		}
	}
	if r.State != "" { // aborted
		return
	}
	if r.Plan == nil && planErr != nil && r.Next > 0 {
		r.State, r.Blocker = StateUnknown, "The server did not answer the dry run that evaluates the gates: "+planErr.Error()
		return
	}
	failing := firstFailing(r.Gates)
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

// firstFailing picks the gate to report as the blocker: the server lists
// every (prerequisite, space) pair, and the CLI's order of checks -- promoted,
// validated, released, healthy, then custom -- says which one matters first.
func firstFailing(gates []Gate) *Gate {
	rank := func(name string) int {
		switch name {
		case PrereqPromoted:
			return 0
		case PrereqValidated:
			return 1
		case PrereqReleased:
			return 2
		case PrereqHealthy:
			return 3
		}
		return 4
	}
	var best *Gate
	for i := range gates {
		g := &gates[i]
		if g.OK {
			continue
		}
		if best == nil || rank(g.Name) < rank(best.Name) {
			best = g
		}
	}
	return best
}

// finalGates is this reading of final.prerequisites over the last stage:
// Promoted and Released from the order's sets, Healthy from the release
// live status, in the CLI's words. A custom or attestation prerequisite
// is not evaluated here and is reported as such, not as failing.
func finalGates(wf *Workflow, last *StageState, o Order) []Gate {
	var out []Gate
	if len(last.Spaces) == 0 {
		return []Gate{{Name: PrereqPromoted, Reason: fmt.Sprintf("the last stage '%s' selects no Space", last.Name)}}
	}
	for _, sp := range last.Spaces {
		v := sp.Variant
		g := Gate{Name: PrereqPromoted, Space: sp.Slug, OK: sp.Taken}
		if !g.OK {
			g.Reason = fmt.Sprintf("Variant '%s' has not taken change order '%s'", v, o.Slug)
		}
		out = append(out, g)
		for _, p := range wf.Final {
			g := Gate{Name: p, Space: sp.Slug, OK: true}
			switch p {
			case PrereqReleased:
				if sp.Releasable && !sp.Released {
					g.OK, g.Reason = false, fmt.Sprintf("Variant '%s' has taken change order '%s' but has not released it", v, o.Slug)
				}
			case PrereqHealthy:
				if reason := unhealthy(sp); reason != "" {
					g.OK, g.Reason = false, reason
				}
			case PrereqPromoted:
				continue
			default:
				g.Reason = fmt.Sprintf("%s: not evaluated here; the server evaluates it when the last stage is released", p)
			}
			out = append(out, g)
		}
	}
	return out
}

// unhealthy is the Healthy gate's verdict for a space as text, "" when healthy.
func unhealthy(sp Space) string {
	v := sp.Variant
	switch {
	case !sp.Releasable:
		return fmt.Sprintf("Variant '%s' has no ReleaseTargetID, so its health cannot be determined", v)
	case !sp.Released:
		return fmt.Sprintf("Variant '%s' has not released the change order's revisions", v)
	case !sp.Health.Present:
		return fmt.Sprintf("no live status has been reported for Variant '%s'", v)
	case sp.Health.Sync != "Synced":
		return fmt.Sprintf("Variant '%s' is not synced", v)
	case sp.Health.Operation == "Running" || sp.Health.Operation == "Failed":
		return fmt.Sprintf("Variant '%s' has an operation %s", v, strings.ToLower(sp.Health.Operation))
	case sp.Health.Status != "Healthy":
		return fmt.Sprintf("Variant '%s' is not healthy", v)
	}
	return ""
}

// stageSpaces lists a stage's members: its selector ANDed with the order's
// scope, so that the one where clause asks exactly what the server asks.
func (c *Cache) stageSpaces(ctx context.Context, cl Client, st Stage, inScope []string) ([]cubclient.Row, error) {
	if len(inScope) == 0 {
		return nil, nil
	}
	where := strings.TrimSpace(st.WhereSpace)
	if where != "" {
		where += " AND "
	}
	where += "SpaceID IN (" + quoted(inScope) + ")"
	c.mu.Lock()
	rows, ok := c.spaces[where]
	c.mu.Unlock()
	if ok {
		return rows, nil
	}
	rows, err := cl.List(ctx, "/space", url.Values{"where": {where}, "select": {spaceSelect}, "include": {"ComponentID"}})
	if err != nil {
		return nil, fmt.Errorf("failed to resolve the Spaces of Stage '%s': %w", st.Name, err)
	}
	c.mu.Lock()
	c.spaces[where] = rows
	c.mu.Unlock()
	return rows, nil
}

// latestReleases reads the latest published Release of each space, for its
// LiveStatus, a batch of spaces per call.
func latestReleases(ctx context.Context, c Client, spaceIDs []string) (map[string]Health, error) {
	out := map[string]Health{}
	const batch = 100
	for start := 0; start < len(spaceIDs); start += batch {
		end := min(start+batch, len(spaceIDs))
		rows, err := c.List(ctx, "/release", url.Values{
			"where":    {"Published = true AND SpaceID IN (" + quoted(spaceIDs[start:end]) + ")"},
			"select":   {"SpaceID,ReleaseNum,ReleaseID,LiveStatus,ChangeOrderID"},
			"order_by": {"DESC:ReleaseNum"},
		})
		if err != nil {
			return out, fmt.Errorf("release live status: %w", err)
		}
		for _, row := range rows {
			rel := own(row, "Release")
			sid := str(rel["SpaceID"])
			if cur, ok := out[sid]; ok && cur.ReleaseNum >= num(rel["ReleaseNum"]) {
				continue
			}
			h := Health{ReleaseNum: num(rel["ReleaseNum"]), ReleaseID: str(rel["ReleaseID"])}
			if ls, ok := rel["LiveStatus"].(map[string]any); ok && ls != nil {
				h.Present = str(ls["Sync"]) != "" || str(ls["Health"]) != ""
				h.Sync, h.Status, h.Operation = str(ls["Sync"]), str(ls["Health"]), str(ls["Operation"])
				h.ObservedAt, h.Message, h.Reporter = str(ls["ObservedAt"]), str(ls["Message"]), str(ls["Reporter"])
			}
			out[sid] = h
		}
	}
	return out, nil
}

// workflowSlug names a ChangeWorkflow by ID, "" when it cannot be read.
func workflowSlug(ctx context.Context, c Client, id string) string {
	if id == "" {
		return ""
	}
	if v, ok := workflowSlugs.Load(id); ok {
		return v.(string)
	}
	rows, err := c.List(ctx, "/change_workflow", url.Values{"where": {fmt.Sprintf("ChangeWorkflowID = '%s'", id)}, "select": {"Slug"}})
	if err != nil || len(rows) == 0 {
		return ""
	}
	slug := str(own(rows[0], "ChangeWorkflow")["Slug"])
	if slug != "" {
		workflowSlugs.Store(id, slug)
	}
	return slug
}

func spaceByID(ctx context.Context, cl Client, id string) (Space, string, error) {
	rows, err := cl.List(ctx, "/space", url.Values{"where": {fmt.Sprintf("SpaceID = '%s'", id)}, "select": {spaceSelect}, "include": {"ComponentID"}})
	if err != nil {
		return Space{}, "", fmt.Errorf("failed to fetch Space %s: %w", id, err)
	}
	if len(rows) == 0 {
		return Space{}, "", fmt.Errorf("Space %s not found", id)
	}
	sp, comp := parseSpace(rows[0])
	return sp, comp, nil
}

// parseSpace reads a space row and the slug of the Component it names.
func parseSpace(row cubclient.Row) (Space, string) {
	sp := own(row, "Space")
	s := Space{ID: str(sp["SpaceID"]), Slug: str(sp["Slug"]), Labels: strmap(sp["Labels"])}
	s.Releasable = str(sp["ReleaseTargetID"]) != ""
	s.Variant = s.Labels["Variant"]
	if s.Variant == "" {
		s.Variant = s.Slug
	}
	s.Upstream = firstNonEmpty(str(sp["UpstreamSpaceID"]), strmap(sp["Annotations"])["UpstreamSpaceID"])
	comp := ""
	if c, ok := row["Component"].(map[string]any); ok {
		comp = str(c["Slug"])
	}
	return s, comp
}

// CubCommands are the CLI lines behind the reading.
func (r *ChangeOrder) CubCommands() []string {
	out := []string{fmt.Sprintf("cub changeorder get %s --space %s", r.Order.Slug, r.Order.SpaceSlug)}
	if r.Workflow != nil && r.Next > 0 {
		out = append(out, fmt.Sprintf("cub variant promote --change-order %s --target-stage %s --dry-run -o mutations", r.Order.Ref(), r.NextName()))
	}
	return out
}

// own returns the entity map of an entity-keyed row, or the row itself.
func own(row cubclient.Row, entity string) map[string]any {
	if m, ok := row[entity].(map[string]any); ok {
		return m
	}
	return row
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

func num(v any) int {
	switch x := v.(type) {
	case float64:
		return int(x)
	case int:
		return x
	case json.Number:
		n, _ := x.Int64()
		return int(n)
	}
	return 0
}

func strs(v any) []string {
	items, _ := v.([]any)
	out := make([]string, 0, len(items))
	for _, it := range items {
		out = append(out, str(it))
	}
	return out
}

func list(v any) []map[string]any {
	items, _ := v.([]any)
	out := make([]map[string]any, 0, len(items))
	for _, it := range items {
		if m, ok := it.(map[string]any); ok {
			out = append(out, m)
		}
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

func quoted(ids []string) string {
	q := make([]string, len(ids))
	for i, id := range ids {
		q[i] = "'" + id + "'"
	}
	return strings.Join(q, ", ")
}

func contains(list []string, s string) bool { return slices.Contains(list, s) }

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// Text renders the reading as plain text, for `-e` and for logs.
func (r *ChangeOrder) Text() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s  %s\n", r.Order.Slug, r.Order.Description)
	fmt.Fprintf(&b, "state %s · stage %s · %s · blocker: %s\n", r.Order.State, firstNonEmpty(r.Order.Stage, "-"), r.State, r.Blocker)
	if r.Workflow == nil {
		return b.String()
	}
	fmt.Fprintf(&b, "workflow %s · component %s · completed %v\n\n", firstNonEmpty(r.WorkflowRef, r.Order.WorkflowID), r.Component, r.Completed)
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
			if sp.HealthyForChange() {
				flags += "H"
			} else if sp.Released && sp.Health.Present {
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
		fmt.Fprintf(&b, "  %s\n", g.Line())
	}
	if n := len(r.Order.Failures); n > 0 {
		f := r.Order.Failures[n-1]
		fmt.Fprintf(&b, "\nlast promotion failure: %s, stage %s (%d recorded)\n", f.At, f.Stage, n)
		for _, s := range f.Spaces {
			fmt.Fprintf(&b, "  %s %s: %s\n", s.Slug, s.Action, s.Reason)
		}
	}
	b.WriteString("\n" + strings.Join(r.CubCommands(), "\n") + "\n")
	return b.String()
}

// Line renders a gate as ✓/✗ with the server's reason, or the pair when it holds.
func (g Gate) Line() string {
	if g.OK {
		s := "✓ " + g.Name
		if g.Space != "" {
			s += " · " + g.Space
		}
		if g.Reason != "" {
			s += " · " + g.Reason
		}
		return s
	}
	return "✗ " + firstNonEmpty(g.Reason, g.Name+" does not hold for "+g.Space)
}
