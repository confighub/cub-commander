package rollout

import (
	"context"
	"fmt"
	"net/url"
	"sort"
	"strings"

	"github.com/confighub/cub-commander/internal/cubclient"
)

// Writer is the client surface the actions need on top of Client.
type Writer interface {
	Client
	PatchRows(ctx context.Context, path string, q url.Values, body string) ([]cubclient.Row, int, error)
}

// UnitPreview is what promoting would do to one unit of one space: the
// server's dry run of the real upgrade against the unit's current data.
type UnitPreview struct {
	UnitID, Slug string
	Current      string
	Would        string
	Fields       []FieldChange
	NormBefore   string
	NormAfter    string
	NoChange     bool
	Err          string
}

// SpacePreview is one target space of a stage.
type SpacePreview struct {
	Space   Space
	Units   []UnitPreview
	Missing []string // base units the space lacks; the upgrade cannot bring them
	Err     string
	Skipped string // why nothing was previewed (the base itself)
}

// Preview is a stage's dry run.
type Preview struct {
	Stage  string
	Spaces []SpacePreview
}

// Blockers are the reasons a promote of this preview would be refused.
func (p *Preview) Blockers() []string {
	var out []string
	for _, sp := range p.Spaces {
		if len(sp.Missing) > 0 {
			out = append(out, fmt.Sprintf("%s lacks %s; the upgrade cannot clone them (run cub variant promote, which does)", sp.Space.Slug, strings.Join(sp.Missing, ", ")))
		}
		if sp.Err != "" {
			out = append(out, sp.Space.Slug+": "+sp.Err)
		}
	}
	return out
}

// Changed counts the units the promotion would change.
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

const bulkPatchBody = "{}"

func promoteWhere(spaceID string) string {
	return fmt.Sprintf("SpaceID = '%s' AND UpstreamUnitID IS NOT NULL", spaceID)
}

func promoteQuery(o Order, spaceID string, dryRun bool) url.Values {
	q := url.Values{
		"where":        {promoteWhere(spaceID)},
		"upgrade":      {"true"},
		"change_order": {o.ID},
		"include":      {"UnitEventID,TargetID,UpstreamUnitID,SpaceID"},
	}
	if dryRun {
		q.Set("dry_run", "true")
		q.Set("include", "ConfigData")
	}
	return q
}

// PreviewStage dry-runs the promotion into every space of one stage, the
// same request cub variant promote makes with --dry-run, and reads the
// would-be configuration against the current one. The base is skipped as
// the CLI skips it. A unit the space lacks is reported as Missing: the
// upgrade alone does not clone it, so a promote here would land short.
func PreviewStage(ctx context.Context, c Writer, r *Rollout, stage int) (*Preview, error) {
	if stage <= 0 || stage >= len(r.Stages) {
		return nil, fmt.Errorf("no such stage")
	}
	st := r.Stages[stage]
	p := &Preview{Stage: st.Name}
	covered := map[string]map[string]string{} // upstream space → units carrying the start tag
	for _, sp := range st.Spaces {
		out := SpacePreview{Space: sp}
		if sp.ID == r.Order.SpaceID {
			out.Skipped = "the space the change order was created in"
			p.Spaces = append(p.Spaces, out)
			continue
		}
		if sp.Upstream == "" {
			out.Err = fmt.Sprintf("space %s has no UpstreamSpaceID annotation; only spaces created by 'cub variant create' can be promoted", sp.Slug)
			p.Spaces = append(p.Spaces, out)
			continue
		}
		baseUnits, ok := covered[sp.Upstream]
		if !ok {
			var err error
			if baseUnits, err = coveredUnits(ctx, c, r.Order, sp.Upstream); err != nil {
				return nil, err
			}
			covered[sp.Upstream] = baseUnits
		}
		// what the space has, and which base units it tracks
		units, err := c.List(ctx, "/unit", url.Values{"where": {fmt.Sprintf("SpaceID = '%s'", sp.ID)}, "select": {"UnitID,Slug,UpstreamUnitID"}})
		if err != nil {
			out.Err = err.Error()
			p.Spaces = append(p.Spaces, out)
			continue
		}
		tracked := map[string]bool{}
		slugs := map[string]string{}
		for _, row := range units {
			u := own(row, "Unit")
			tracked[str(u["UpstreamUnitID"])] = true
			slugs[str(u["UnitID"])] = str(u["Slug"])
		}
		for id, slug := range baseUnits {
			if !tracked[id] {
				out.Missing = append(out.Missing, slug)
			}
		}
		sort.Strings(out.Missing)
		// current data, one call
		current := map[string]string{}
		data, err := c.List(ctx, "/unit_data", url.Values{"where": {fmt.Sprintf("SpaceID = '%s'", sp.ID)}})
		if err != nil {
			out.Err = err.Error()
			p.Spaces = append(p.Spaces, out)
			continue
		}
		for _, row := range data {
			current[str(row["UnitID"])] = str(row["Data"])
		}
		// the dry run
		rows, _, err := c.PatchRows(ctx, "/unit", promoteQuery(r.Order, sp.ID, true), bulkPatchBody)
		if err != nil {
			out.Err = err.Error()
			p.Spaces = append(p.Spaces, out)
			continue
		}
		for _, row := range rows {
			u := own(row, "Unit")
			up := UnitPreview{UnitID: str(u["UnitID"]), Slug: firstNonEmpty(str(u["Slug"]), slugs[str(u["UnitID"])]), Would: str(row["ConfigData"])}
			up.Current = current[up.UnitID]
			if e, ok := row["Error"].(map[string]any); ok && e != nil {
				up.Err = firstNonEmpty(str(e["Message"]), str(e["message"]), "error")
			}
			if up.Err == "" {
				if up.Would == "" || up.Would == up.Current {
					up.NoChange = true
				} else {
					up.Fields, up.NormBefore, up.NormAfter, _ = Semantic(up.Current, up.Would)
					if len(up.Fields) == 0 {
						up.NoChange = true // layout only
					}
				}
			}
			out.Units = append(out.Units, up)
		}
		sort.Slice(out.Units, func(i, j int) bool {
			ci, cj := !out.Units[i].NoChange, !out.Units[j].NoChange
			if ci != cj {
				return ci
			}
			return out.Units[i].Slug < out.Units[j].Slug
		})
		p.Spaces = append(p.Spaces, out)
	}
	return p, nil
}

// coveredUnits is the set of units of one upstream space the change order
// covers (they carry its start tag), by ID → slug. A downstream space must
// track each of them for an upgrade alone to bring the whole change; the
// CLI clones the rest, which this preview only reports.
func coveredUnits(ctx context.Context, c Client, o Order, upstreamSpaceID string) (map[string]string, error) {
	rows, err := c.List(ctx, "/revision", url.Values{
		"where":   {fmt.Sprintf("SpaceID = '%s' AND Tags ? '%s'", upstreamSpaceID, o.StartTagID)},
		"select":  {"RevisionID,UnitID,Unit.Slug"},
		"include": {"UnitID"},
	})
	if err != nil {
		return nil, fmt.Errorf("base units: %w", err)
	}
	out := map[string]string{}
	for _, row := range rows {
		rv := own(row, "Revision")
		id := str(rv["UnitID"])
		slug := id
		if u, ok := row["Unit"].(map[string]any); ok && str(u["Slug"]) != "" {
			slug = str(u["Slug"])
		}
		out[id] = slug
	}
	return out, nil
}

// Outcome is what one space's promotion did.
type Outcome struct {
	Space   Space
	Status  int
	Units   int      // responses
	Changed int      // responses with a new revision (no error)
	Errors  []string // per-unit errors, slug: message
	Err     string   // the call itself failed
	Skipped string
}

// PromoteStage runs the promotion into every space of a stage, one space at
// a time as cub variant promote does: a failure is reported and the spaces
// after it are still promoted. Gates are the caller's business (checked on a
// fresh reading immediately before); the server passes over units already
// carrying the end tag, so running it again is safe.
func PromoteStage(ctx context.Context, c Writer, r *Rollout, stage int) ([]Outcome, error) {
	if stage <= 0 || stage >= len(r.Stages) {
		return nil, fmt.Errorf("no such stage")
	}
	var out []Outcome
	for _, sp := range r.Stages[stage].Spaces {
		o := Outcome{Space: sp}
		if sp.ID == r.Order.SpaceID {
			o.Skipped = "the space the change order was created in"
			out = append(out, o)
			continue
		}
		rows, status, err := c.PatchRows(ctx, "/unit", promoteQuery(r.Order, sp.ID, false), bulkPatchBody)
		o.Status = status
		if err != nil {
			o.Err = err.Error()
			out = append(out, o)
			continue
		}
		o.Units = len(rows)
		for _, row := range rows {
			u := own(row, "Unit")
			if e, ok := row["Error"].(map[string]any); ok && e != nil {
				o.Errors = append(o.Errors, str(u["Slug"])+": "+firstNonEmpty(str(e["Message"]), str(e["message"]), "error"))
				continue
			}
			o.Changed++
		}
		out = append(out, o)
	}
	return out, nil
}

// PromoteCommands are the CLI lines a promote of this stage stands for.
func PromoteCommands(r *Rollout, stage int) []string {
	if stage <= 0 || stage >= len(r.Stages) {
		return nil
	}
	return []string{fmt.Sprintf("cub variant promote --change-order %s --target-stage %s", r.Order.Ref(), r.Stages[stage].Name)}
}

// own returns the entity map of an entity-keyed row, or the row itself.
func own(row cubclient.Row, entity string) map[string]any {
	if m, ok := row[entity].(map[string]any); ok {
		return m
	}
	return row
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
