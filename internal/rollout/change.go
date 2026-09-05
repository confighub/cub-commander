package rollout

import (
	"context"
	"fmt"
	"net/url"
	"sort"
	"strings"
)

// UnitChange is what a ChangeOrder did to one unit in one space: the revision
// its start tag marks against the one its end tag marks. In the base that is
// the ordered change itself (the start tag is where the variants last took
// from, the end tag the head when the order was cut); in a space that has
// taken the change it is what the promotion wrote there (the start tag goes
// on the head before the merge, the end tag on the revision it arrives at).
// A unit whose two tags land on the same revision is covered but untouched.
type UnitChange struct {
	UnitID, Slug     string
	StartRev, EndRev int
	StartID, EndID   string
	Before, After    string
	Touched          bool
	Err              string
	// The change read as configuration (Semantic): the fields that differ,
	// both sides canonically re-encoded, and whether only layout changed.
	Fields                []FieldChange
	NormBefore, NormAfter string
	FormattingOnly        bool
	// Kept are the ordered change's fields this space did not take (WithKept).
	Kept []KeptField
}

// Change reads the tag pair for every unit of one space. Two list calls (the
// where language has no OR) and one revision_data call for the bodies of the
// touched units.
func Change(ctx context.Context, c Client, o Order, spaceID string) ([]UnitChange, error) {
	byUnit := map[string]*UnitChange{}
	load := func(tagID string, set func(u *UnitChange, id string, num int)) error {
		if tagID == "" {
			return nil
		}
		rows, err := c.List(ctx, "/revision", url.Values{
			"where":   {fmt.Sprintf("SpaceID = '%s' AND Tags ? '%s'", spaceID, tagID)},
			"select":  {"RevisionID,UnitID,RevisionNum,Unit.Slug"},
			"include": {"UnitID"},
		})
		if err != nil {
			return err
		}
		for _, row := range rows {
			rv, _ := row["Revision"].(map[string]any)
			if rv == nil {
				rv = row
			}
			uid := str(rv["UnitID"])
			u := byUnit[uid]
			if u == nil {
				u = &UnitChange{UnitID: uid}
				byUnit[uid] = u
			}
			if un, ok := row["Unit"].(map[string]any); ok && u.Slug == "" {
				u.Slug = str(un["Slug"])
			}
			num := 0
			if f, ok := rv["RevisionNum"].(float64); ok {
				num = int(f)
			}
			set(u, str(rv["RevisionID"]), num)
		}
		return nil
	}
	if err := load(o.StartTagID, func(u *UnitChange, id string, n int) { u.StartID, u.StartRev = id, n }); err != nil {
		return nil, fmt.Errorf("start tag: %w", err)
	}
	if err := load(o.EndTagID, func(u *UnitChange, id string, n int) { u.EndID, u.EndRev = id, n }); err != nil {
		return nil, fmt.Errorf("end tag: %w", err)
	}
	var want []string
	var out []UnitChange
	for _, u := range byUnit {
		u.Touched = u.StartID != u.EndID
		if u.Slug == "" {
			u.Slug = u.UnitID
		}
		if u.Touched {
			if u.StartID != "" {
				want = append(want, u.StartID)
			}
			if u.EndID != "" {
				want = append(want, u.EndID)
			}
		}
		out = append(out, *u)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Touched != out[j].Touched {
			return out[i].Touched
		}
		return out[i].Slug < out[j].Slug
	})
	if len(want) == 0 {
		return out, nil
	}
	data, err := RevisionData(ctx, c, want)
	if err != nil {
		return nil, err
	}
	for i := range out {
		if !out[i].Touched {
			continue
		}
		out[i].Before = data[out[i].StartID]
		out[i].After = data[out[i].EndID]
		if out[i].StartID != "" && out[i].Before == "" || out[i].EndID != "" && out[i].After == "" {
			out[i].Err = "revision data missing"
			continue
		}
		out[i].Fields, out[i].NormBefore, out[i].NormAfter, out[i].FormattingOnly = Semantic(out[i].Before, out[i].After)
	}
	return out, nil
}

// RevisionData fetches the bodies of revisions by ID in one call, in batches.
//
// The org-wide revision endpoints keep one row per unit unless told
// otherwise (distinct_on=Off, which then demands a limit); a before/after
// pair is two revisions of one unit, so Off it is.
func RevisionData(ctx context.Context, c Client, ids []string) (map[string]string, error) {
	out := map[string]string{}
	const batch = 100
	for start := 0; start < len(ids); start += batch {
		end := min(start+batch, len(ids))
		quoted := make([]string, 0, end-start)
		for _, id := range ids[start:end] {
			quoted = append(quoted, "'"+id+"'")
		}
		rows, err := c.List(ctx, "/revision_data", url.Values{
			"where":       {"RevisionID IN (" + strings.Join(quoted, ", ") + ")"},
			"distinct_on": {"Off"},
			"limit":       {fmt.Sprint(len(quoted))},
		})
		if err != nil {
			return nil, fmt.Errorf("revision data: %w", err)
		}
		for _, row := range rows {
			out[str(row["RevisionID"])] = str(row["Data"])
		}
	}
	return out, nil
}
