package changeorder

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"sync"
)

// UnitChange is what a ChangeOrder did to one unit in one space: the
// revision its start tag marks against the one its end tag marks, as the
// server diffs them. In the base that is the ordered change itself; in a
// space that has taken the change it is what the promotion wrote there. A
// unit whose two tags land on the same revision is covered but untouched.
type UnitChange struct {
	UnitID, Slug     string
	StartRev, EndRev int
	Touched          bool
	Err              string
	Fields           []FieldChange
	// Kept are the ordered change's fields this space did not take (ChangeIn).
	Kept []KeptField
}

// Change reads the change order's change in one space: one unit_diff call
// from the revisions before the order to the ones it arrived at, which is
// `cub unit diff` over Before:ChangeOrder and ChangeOrder for every unit.
// Units the order does not cover there (no tag on either side) are left out.
func Change(ctx context.Context, c Client, o Order, spaceID string, names map[string]unitInfo) ([]UnitChange, error) {
	rows, err := c.List(ctx, "/unit_diff", url.Values{
		"where": {fmt.Sprintf("SpaceID = '%s'", spaceID)},
		"from":  {"Before:ChangeOrder:" + o.ID},
		"to":    {"ChangeOrder:" + o.ID},
	})
	if err != nil {
		return nil, fmt.Errorf("the change in the space: %w", err)
	}
	var out []UnitChange
	for _, row := range rows {
		u := UnitChange{UnitID: str(row["UnitID"]), StartRev: num(row["FromRevisionNum"]), EndRev: num(row["ToRevisionNum"]), Err: errText(row["Error"])}
		if u.StartRev == 0 && u.EndRev == 0 && u.Err == "" {
			continue // not covered here
		}
		u.Slug = firstNonEmpty(names[u.UnitID].Slug, u.UnitID)
		u.Touched = u.StartRev != u.EndRev
		u.Fields = ParseConfigDiff(row["Diff"])
		if u.Touched && len(u.Fields) == 0 && u.Err == "" {
			u.Err = ""
		}
		out = append(out, u)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Touched != out[j].Touched {
			return out[i].Touched
		}
		return out[i].Slug < out[j].Slug
	})
	return out, nil
}

// The reference for "kept" is the ordered change itself -- what the change
// order did in the base -- not the space's immediate upstream. A class base
// that kept a protected value passes an upstream change that no longer
// mentions it, so a deployment compared with its class base would see
// nothing kept while the reviewer, who is rolling out the base's change,
// very much wants to. Each unit is followed up its UpgradeUnit lineage to
// the base unit, and the base unit's field changes are the reference.

type unitInfo struct {
	Slug, Upstream string
}

// lineage caches, per Change order, what following units to the base needs.
type lineage struct {
	mu       sync.Mutex
	list     []UnitChange                   // the ordered change, as Change returns it
	ordered  map[string]UnitChange          // base unit ID → the ordered change
	units    map[string]map[string]unitInfo // space ID → unit ID → info
	upstream map[string]string              // space ID → upstream space ID
}

func (r *ChangeOrder) lineage(ctx context.Context, c Client) (*lineage, error) {
	if r.lc == nil {
		r.lc = &lineageCache{}
	}
	r.lc.mu.Lock()
	defer r.lc.mu.Unlock()
	if r.lc.lin != nil {
		return r.lc.lin, nil
	}
	l := &lineage{units: map[string]map[string]unitInfo{}, upstream: map[string]string{}, ordered: map[string]UnitChange{}}
	for _, st := range r.Stages {
		for _, sp := range st.Spaces {
			l.upstream[sp.ID] = sp.Upstream
		}
	}
	names, err := l.spaceUnits(ctx, c, r.Order.SpaceID)
	if err != nil {
		return nil, fmt.Errorf("the base's units: %w", err)
	}
	changes, err := Change(ctx, c, r.Order, r.Order.SpaceID, names)
	if err != nil {
		return nil, fmt.Errorf("the ordered change: %w", err)
	}
	l.list = changes
	for _, uc := range changes {
		l.ordered[uc.UnitID] = uc
	}
	r.lc.lin = l
	return l, nil
}

// OrderedChange is the change order's own change in its base, read once per
// reading and shared with the kept-field derivation.
func OrderedChange(ctx context.Context, c Client, r *ChangeOrder) ([]UnitChange, error) {
	l, err := r.lineage(ctx, c)
	if err != nil {
		return nil, err
	}
	return l.list, nil
}

// ChangeIn is the change in one space -- the ordered change for the base,
// what the promotion wrote for any other -- with the ordered change's fields
// the space did not take marked as kept.
func ChangeIn(ctx context.Context, c Client, r *ChangeOrder, spaceID string) ([]UnitChange, error) {
	if spaceID == r.Order.SpaceID {
		return OrderedChange(ctx, c, r)
	}
	l, err := r.lineage(ctx, c)
	if err != nil {
		return nil, err
	}
	names, err := l.spaceUnits(ctx, c, spaceID)
	if err != nil {
		return nil, err
	}
	changes, err := Change(ctx, c, r.Order, spaceID, names)
	if err != nil {
		return nil, err
	}
	current, _ := currentData(ctx, c, spaceID)
	for i := range changes {
		u := &changes[i]
		root, ok := l.rootChange(ctx, c, r, spaceID, u.UnitID)
		if !ok {
			continue
		}
		u.Kept = keptFields(root.Fields, u.Fields, current[u.UnitID], nil, func() map[string]map[string]bool {
			return unitProtection(ctx, c, spaceID, u.UnitID)
		})
	}
	return changes, nil
}

func (l *lineage) spaceUnits(ctx context.Context, c Client, spaceID string) (map[string]unitInfo, error) {
	l.mu.Lock()
	m, ok := l.units[spaceID]
	l.mu.Unlock()
	if ok {
		return m, nil
	}
	rows, err := c.List(ctx, "/unit", url.Values{"where": {fmt.Sprintf("SpaceID = '%s'", spaceID)}, "select": {"UnitID,Slug,UpstreamUnitID"}})
	if err != nil {
		return nil, err
	}
	m = map[string]unitInfo{}
	for _, row := range rows {
		u := own(row, "Unit")
		m[str(u["UnitID"])] = unitInfo{Slug: str(u["Slug"]), Upstream: str(u["UpstreamUnitID"])}
	}
	l.mu.Lock()
	l.units[spaceID] = m
	l.mu.Unlock()
	return m, nil
}

// rootChange follows a unit up to the base and returns the ordered change
// for the base unit it descends from, if the change touched it.
func (l *lineage) rootChange(ctx context.Context, c Client, r *ChangeOrder, spaceID, unitID string) (UnitChange, bool) {
	for hop := 0; hop < 8 && spaceID != ""; hop++ {
		if spaceID == r.Order.SpaceID {
			uc, ok := l.ordered[unitID]
			return uc, ok && uc.Touched
		}
		units, err := l.spaceUnits(ctx, c, spaceID)
		if err != nil {
			return UnitChange{}, false
		}
		info, ok := units[unitID]
		if !ok || info.Upstream == "" {
			return UnitChange{}, false
		}
		unitID = info.Upstream
		spaceID = l.upstream[spaceID]
	}
	return UnitChange{}, false
}

// unitProtection reads a unit's current MutationSources for its protected paths.
func unitProtection(ctx context.Context, c Client, spaceID, unitID string) map[string]map[string]bool {
	body, err := c.GetRaw(ctx, "/space/"+spaceID+"/unit/"+unitID+"/mutation_sources")
	if err != nil {
		return nil
	}
	var resp struct {
		MutationSources []any
	}
	if json.Unmarshal([]byte(body), &resp) != nil {
		return nil
	}
	return protectedPaths(resp.MutationSources)
}
