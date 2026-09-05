package rollout

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"sync"
)

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

// lineage caches, per Rollout, what following units to the base needs.
type lineage struct {
	mu       sync.Mutex
	list     []UnitChange                   // the ordered change, as Change returns it
	ordered  map[string]UnitChange          // base unit ID → the ordered change
	units    map[string]map[string]unitInfo // space ID → unit ID → info
	upstream map[string]string              // space ID → upstream space ID
}

func (r *Rollout) lineage(ctx context.Context, c Client) (*lineage, error) {
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
	changes, err := Change(ctx, c, r.Order, r.Order.SpaceID)
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
func OrderedChange(ctx context.Context, c Client, r *Rollout) ([]UnitChange, error) {
	l, err := r.lineage(ctx, c)
	if err != nil {
		return nil, err
	}
	return l.list, nil
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
func (l *lineage) rootChange(ctx context.Context, c Client, r *Rollout, spaceID, unitID string) (UnitChange, bool) {
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

// unitProtection reads a unit's current MutationSources for its protected
// paths.
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

// WithKept annotates a space's change (what the promotion wrote there) with
// the ordered change's fields it did not bring, read against the revision
// the change arrived at, with protection from the unit's MutationSources.
func WithKept(ctx context.Context, c Client, r *Rollout, spaceID string, changes []UnitChange) []UnitChange {
	if spaceID == r.Order.SpaceID {
		return changes
	}
	l, err := r.lineage(ctx, c)
	if err != nil {
		return changes
	}
	for i := range changes {
		u := &changes[i]
		root, ok := l.rootChange(ctx, c, r, spaceID, u.UnitID)
		if !ok {
			continue
		}
		state := u.After
		if !u.Touched {
			// untouched: the revision both tags mark; its body was not fetched
			data, err := RevisionData(ctx, c, []string{u.EndID})
			if err != nil {
				continue
			}
			state = data[u.EndID]
		}
		kept := keptFields(root.Fields, u.Fields, state, nil)
		if len(kept) == 0 {
			continue
		}
		prot := unitProtection(ctx, c, spaceID, u.UnitID)
		for k := range kept {
			kept[k].Protected = isProtected(kept[k], prot)
		}
		u.Kept = kept
	}
	return changes
}

func isProtected(k KeptField, protected map[string]map[string]bool) bool {
	mp := MutationPath(k.Path)
	for res, paths := range protected {
		if res != "" && k.Doc != "" && res != k.Doc {
			continue
		}
		for p := range paths {
			if mp == p || len(mp) > len(p) && mp[:len(p)] == p && mp[len(p)] == '.' {
				return true
			}
		}
	}
	return false
}
