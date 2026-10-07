package changeorder

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"sync"

	"github.com/confighub/cub-commander/internal/cubclient"
)

// MemClient answers List, GetRaw and Send from canned rows: a tiny where
// evaluator over entity-keyed rows, enough for the queries this package
// makes, and a small stand-in for POST /promote that evaluates gates and
// plans actions the way the server does, over the same rows. Tests in
// several packages use it, so it is not a _test file.
type MemClient struct {
	mu   sync.Mutex
	Rows map[string][]cubclient.Row // path → rows
	Raw  map[string]string          // path → body
	Log  []string                   // every request, for assertions
	// Upgrades is what a promotion would write to a downstream unit: the
	// diff of its upgrade and the paths the merge withholds. A unit with an
	// upstream and no entry is marked, not upgraded.
	Upgrades map[string]Upgrade
	// Posts records every Send with a body; Promotes the applied promotions.
	Posts    []string
	Promotes []string
	// OnRelease answers a release publish; nil answers with a fake row.
	OnRelease func(path, body string) (int, []byte, error)
}

// Upgrade is the fixture's answer for one unit of a dry run.
type Upgrade struct {
	Diff      map[string]any
	Conflicts []any
}

func (m *MemClient) List(_ context.Context, path string, q url.Values) ([]cubclient.Row, error) {
	m.mu.Lock()
	m.Log = append(m.Log, path+"?"+q.Encode())
	rows, ok := m.Rows[path]
	m.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("MemClient: no rows for %s", path)
	}
	where := q.Get("where")
	var out []cubclient.Row
	for _, r := range rows {
		if where == "" || matches(where, r) {
			out = append(out, r)
		}
	}
	return out, nil
}

func (m *MemClient) GetRaw(_ context.Context, path string) (string, error) {
	m.mu.Lock()
	m.Log = append(m.Log, "GET "+path)
	body, ok := m.Raw[path]
	m.mu.Unlock()
	if !ok {
		return "", fmt.Errorf("MemClient: no body for %s", path)
	}
	return body, nil
}

func (m *MemClient) Send(_ context.Context, method, path string, q url.Values, _ string, body string) (int, []byte, error) {
	m.mu.Lock()
	m.Log = append(m.Log, method+" "+path+"?"+q.Encode()+" "+body)
	m.Posts = append(m.Posts, path+" "+body)
	m.mu.Unlock()
	switch {
	case path == "/promote":
		return m.promote(q, body)
	case strings.HasSuffix(path, "/release"):
		if m.OnRelease != nil {
			return m.OnRelease(path, body)
		}
		m.mu.Lock()
		n := len(m.Posts)
		m.mu.Unlock()
		out, _ := json.Marshal(map[string]any{"Release": map[string]any{"ReleaseID": fmt.Sprintf("rel-%d", n), "ReleaseNum": n}})
		return 200, out, nil
	}
	return 0, nil, fmt.Errorf("MemClient: no handler for %s %s", method, path)
}

var term = regexp.MustCompile(`^\s*([A-Za-z_.]+)\s*(=|\?|IN)\s*(.+?)\s*$`)

// matches evaluates a conjunction of `Field = 'v'`, `Field ? 'v'`,
// `Field IN ('a', 'b')` and `Field = 3` terms against a row's own entity
// (the first map value) and its label map.
func matches(where string, row cubclient.Row) bool {
	for _, t := range strings.Split(where, " AND ") {
		mm := term.FindStringSubmatch(t)
		if mm == nil {
			return false
		}
		field, op, rhs := mm[1], mm[2], mm[3]
		v := fieldValue(row, field)
		switch op {
		case "=":
			if fmt.Sprint(v) != strings.Trim(rhs, "'") {
				return false
			}
		case "?":
			want := strings.Trim(rhs, "'")
			switch x := v.(type) {
			case map[string]any:
				if _, ok := x[want]; !ok {
					return false
				}
			case []any:
				found := false
				for _, it := range x {
					if fmt.Sprint(it) == want {
						found = true
					}
				}
				if !found {
					return false
				}
			default:
				return false
			}
		case "IN":
			list := strings.Trim(rhs, "()")
			found := false
			for _, item := range strings.Split(list, ",") {
				if fmt.Sprint(v) == strings.Trim(strings.TrimSpace(item), "'") {
					found = true
				}
			}
			if !found {
				return false
			}
		}
	}
	return true
}

func fieldValue(row cubclient.Row, field string) any {
	segs := strings.Split(field, ".")
	// a path naming an entity key (Space.Slug) reads inside it
	if len(segs) > 1 {
		if m, ok := row[segs[0]].(map[string]any); ok {
			return dig(m, segs[1:])
		}
	}
	// otherwise the first entity map that has the field, else the flat row
	for _, v := range row {
		if m, ok := v.(map[string]any); ok {
			if x := dig(m, segs); x != nil {
				return x
			}
		}
	}
	return dig(row, segs)
}

func dig(m map[string]any, segs []string) any {
	var v any = m
	for _, s := range segs {
		mm, ok := v.(map[string]any)
		if !ok {
			return nil
		}
		v = mm[s]
	}
	return v
}

// promote is the stand-in for POST /promote: the target stage (named, or
// the next one a member has not taken), its entry gates over the previous
// stage in the CLI's words, and per space the units an upgrade would write,
// mark or clone. An apply records the spaces as resolved on the order.
func (m *MemClient) promote(q url.Values, body string) (int, []byte, error) {
	var req struct {
		ChangeOrderID, TargetStage, ExpectedPlan string
		DryRun, Force                            bool
	}
	if err := json.Unmarshal([]byte(body), &req); err != nil {
		return 400, []byte(`{"Message":"bad request"}`), fmt.Errorf("bad request")
	}
	var co map[string]any
	m.mu.Lock()
	for _, row := range m.Rows["/change_order"] {
		if c := own(row, "ChangeOrder"); str(c["ChangeOrderID"]) == req.ChangeOrderID {
			co = c
		}
	}
	m.mu.Unlock()
	if co == nil {
		return 404, []byte(`{"Message":"no such change order"}`), fmt.Errorf("server 404: no such change order")
	}
	if str(co["AbortedReason"]) != "" {
		return 400, []byte(`{"Message":"change order is aborted"}`), fmt.Errorf("server 400: change order is aborted")
	}
	wf := parseWorkflow(co["ChangeWorkflow"])
	if wf == nil {
		return 400, []byte(`{"Message":"no workflow"}`), fmt.Errorf("server 400: the change order has no ChangeWorkflow")
	}
	o := Order{ID: req.ChangeOrderID, Slug: str(co["Slug"]), SpaceID: str(co["SpaceID"]), InScope: strs(co["InScopeSpaceIDs"]), Resolved: strs(co["ResolvedSpaceIDs"]), Released: strs(co["ReleasedSpaceIDs"])}
	members := func(st Stage) []Space {
		rows, _ := NewCache().stageSpaces(context.Background(), m, st, o.InScope)
		var out []Space
		for _, row := range rows {
			sp, _ := parseSpace(row)
			sp.Taken = contains(o.Resolved, sp.ID)
			sp.Released = contains(o.Released, sp.ID)
			if h, ok := m.latestRelease(sp.ID); ok {
				sp.Health = h
			}
			out = append(out, sp)
		}
		return out
	}
	idx := -1
	for i, st := range wf.Stages {
		if req.TargetStage != "" {
			if st.Name == req.TargetStage {
				idx = i
			}
			continue
		}
		for _, sp := range members(st) {
			if !sp.Taken {
				idx = i
				break
			}
		}
		if idx >= 0 {
			break
		}
	}
	if req.TargetStage != "" && idx < 0 {
		return 400, []byte(`{"Message":"no such stage"}`), fmt.Errorf("server 400: no such stage")
	}
	result := map[string]any{"DryRun": req.DryRun, "ChangeOrderID": req.ChangeOrderID, "Spaces": []any{}}
	if idx < 0 {
		result["Complete"], result["Plan"] = true, "plan-complete"
		out, _ := json.Marshal(result)
		return 200, out, nil
	}
	st := wf.Stages[idx]
	plan := "plan-" + o.Slug + "-" + st.Name
	result["Plan"] = plan
	stageRes := map[string]any{"Name": st.Name, "Chosen": req.TargetStage == "", "Gates": []any{}}
	refused := false
	if idx > 0 {
		prev := wf.Stages[idx-1]
		stageRes["PreviousStage"] = prev.Name
		var gates []any
		for _, sp := range members(prev) {
			add := func(name string, ok bool, msg string) {
				g := map[string]any{"Prerequisite": name, "SpaceID": sp.ID, "SpaceSlug": sp.Slug, "Satisfied": ok}
				if !ok {
					g["Message"] = msg
					refused = true
				}
				gates = append(gates, g)
			}
			add(PrereqPromoted, sp.Taken, fmt.Sprintf("Variant '%s' has not taken change order '%s'", sp.Variant, o.Slug))
			for _, p := range st.Prerequisites {
				switch p {
				case PrereqReleased:
					add(p, !sp.Releasable || sp.Released, fmt.Sprintf("Variant '%s' has taken change order '%s' but has not released it", sp.Variant, o.Slug))
				case PrereqHealthy:
					reason := unhealthy(sp)
					if reason == "" && !sp.Health.ForOrder {
						reason = fmt.Sprintf("Variant '%s' has not published a Release carrying change order '%s'", sp.Variant, o.Slug)
					}
					add(p, reason == "", reason)
				default:
					add(p, false, fmt.Sprintf("prerequisite '%s' does not hold for Variant '%s'", p, sp.Variant))
				}
			}
		}
		stageRes["Gates"] = gates
	}
	result["Stages"] = []any{stageRes}
	if refused && !req.Force {
		out, _ := json.Marshal(result)
		return 409, out, fmt.Errorf("server 409: the Stage's entry gates do not hold")
	}
	if !req.DryRun && req.ExpectedPlan != "" && req.ExpectedPlan != plan {
		return 412, []byte(`{"Message":"the plan differs"}`), fmt.Errorf("server 412: the plan differs")
	}
	withDiff := strings.Contains(q.Get("include"), "Diff")
	var spaces []any
	var landed []string
	for _, sp := range members(st) {
		sr := map[string]any{"SpaceID": sp.ID, "SpaceSlug": sp.Slug, "Stage": st.Name, "UpstreamSpaceID": sp.Upstream, "Action": "Promote", "Units": []any{}, "Links": []any{}}
		if sp.ID == o.SpaceID {
			sr["Action"], sr["Reason"] = "Skipped", "the space the change order was created in"
			spaces = append(spaces, sr)
			continue
		}
		var units []any
		tracked := map[string]bool{}
		m.mu.Lock()
		unitRows := append([]cubclient.Row(nil), m.Rows["/unit"]...)
		m.mu.Unlock()
		for _, row := range unitRows {
			u := own(row, "Unit")
			if str(u["SpaceID"]) != sp.ID || str(u["UpstreamUnitID"]) == "" {
				continue
			}
			tracked[str(u["UpstreamUnitID"])] = true
			ur := map[string]any{"UnitID": u["UnitID"], "Slug": u["Slug"], "UpstreamUnitID": u["UpstreamUnitID"], "Action": "Mark"}
			if sp.Taken {
				ur["Action"] = "Unchanged"
			} else if up, ok := m.Upgrades[str(u["UnitID"])]; ok {
				ur["Action"] = "Upgrade"
				if withDiff {
					ur["Diff"] = up.Diff
					ur["Conflicts"] = up.Conflicts
				}
			}
			units = append(units, ur)
		}
		if !sp.Taken {
			for _, row := range unitRows {
				u := own(row, "Unit")
				if str(u["SpaceID"]) == sp.Upstream && !tracked[str(u["UnitID"])] {
					units = append(units, map[string]any{"Slug": u["Slug"], "UpstreamUnitID": u["UnitID"], "Action": "Clone"})
				}
			}
		}
		if sp.Taken {
			sr["Action"] = "Unchanged"
		} else {
			landed = append(landed, sp.ID)
		}
		sr["Units"] = units
		spaces = append(spaces, sr)
	}
	result["Spaces"] = spaces
	if !req.DryRun {
		m.mu.Lock()
		resolved := strs(co["ResolvedSpaceIDs"])
		for _, id := range landed {
			if !contains(resolved, id) {
				resolved = append(resolved, id)
			}
		}
		ids := make([]any, len(resolved))
		for i, id := range resolved {
			ids[i] = id
		}
		co["ResolvedSpaceIDs"] = ids
		co["Stage"] = st.Name
		m.Promotes = append(m.Promotes, o.Slug+" → "+st.Name+" ("+strings.Join(landed, ", ")+")")
		m.mu.Unlock()
	}
	out, _ := json.Marshal(result)
	return 200, out, nil
}

// latestRelease is a space's latest published release from the rows.
func (m *MemClient) latestRelease(spaceID string) (Health, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var best map[string]any
	for _, row := range m.Rows["/release"] {
		rel := own(row, "Release")
		if str(rel["SpaceID"]) != spaceID || rel["Published"] != true {
			continue
		}
		if best == nil || num(rel["ReleaseNum"]) > num(best["ReleaseNum"]) {
			best = rel
		}
	}
	if best == nil {
		return Health{}, false
	}
	h := Health{ReleaseNum: num(best["ReleaseNum"]), ReleaseID: str(best["ReleaseID"])}
	if ls, ok := best["LiveStatus"].(map[string]any); ok {
		h.Present = true
		h.Sync, h.Status, h.Operation, h.ObservedAt, h.Message = str(ls["Sync"]), str(ls["Health"]), str(ls["Operation"]), str(ls["ObservedAt"]), str(ls["Message"])
	}
	h.ForOrder = str(best["ChangeOrderID"]) != ""
	return h, true
}

// ChapterOne is the change-workflows demo's opening state, shaped like the
// live rows (Demo org, 2026-09-05, re-shaped for API 0.8): component
// cert-manager with a base, three class bases and six deployments; change
// order cert-manager-1-17-0 taken through test and released in dev and both
// test clusters, with us-east-test1 Degraded; and catalog-api with a fresh
// change order nothing has taken. The workflow is the demo's four-stage line.
func ChapterOne() *MemClient {
	space := func(id, slug, comp, role, stage, variant, target string) cubclient.Row {
		labels := map[string]any{"Component": comp, "Role": role, "Variant": variant, "DemoName": "workflows"}
		if stage != "" {
			labels["Stage"] = stage
		}
		sp := map[string]any{"SpaceID": id, "Slug": slug, "Labels": labels, "Annotations": map[string]any{}}
		// the variant tree: class bases clone the base, deployments their class base
		prefix := strings.SplitN(id, "-", 2)[0]
		switch role {
		case "base":
			if variant != "base" {
				sp["UpstreamSpaceID"] = prefix + "-base"
			}
		case "deployment":
			sp["UpstreamSpaceID"] = prefix + "-" + stage
		}
		if target != "" {
			sp["ReleaseTargetID"] = target
		}
		row := cubclient.Row{"Space": sp}
		if comp != "" {
			sp["ComponentID"] = "comp-" + comp
			row["Component"] = map[string]any{"ComponentID": "comp-" + comp, "Slug": comp}
		}
		return row
	}
	spaces := []cubclient.Row{
		space("cm-base", "cert-manager-base", "cert-manager", "base", "", "base", ""),
		space("cm-dev", "cert-manager-dev", "cert-manager", "base", "dev", "dev", ""),
		space("cm-test", "cert-manager-test", "cert-manager", "base", "test", "test", ""),
		space("cm-prod", "cert-manager-prod", "cert-manager", "base", "prod", "prod", ""),
		space("cm-dev1", "cert-manager-us-east-dev1", "cert-manager", "deployment", "dev", "us-east-dev1", "t-dev1"),
		space("cm-test1", "cert-manager-us-east-test1", "cert-manager", "deployment", "test", "us-east-test1", "t-test1"),
		space("cm-test2", "cert-manager-us-east-test2", "cert-manager", "deployment", "test", "us-east-test2", "t-test2"),
		space("cm-prod1", "cert-manager-us-east-prod1", "cert-manager", "deployment", "prod", "us-east-prod1", "t-prod1"),
		space("cm-prod2", "cert-manager-us-east-prod2", "cert-manager", "deployment", "prod", "us-east-prod2", "t-prod2"),
		space("cm-prod3", "cert-manager-us-east-prod3", "cert-manager", "deployment", "prod", "us-east-prod3", "t-prod3"),
		space("ca-base", "catalog-api-base", "catalog-api", "base", "", "base", ""),
		space("ca-dev", "catalog-api-dev", "catalog-api", "base", "dev", "dev", ""),
		space("ca-test", "catalog-api-test", "catalog-api", "base", "test", "test", ""),
		space("ca-prod", "catalog-api-prod", "catalog-api", "base", "prod", "prod", ""),
		space("ca-dev1", "catalog-api-us-east-dev1", "catalog-api", "deployment", "dev", "us-east-dev1", "t-dev1"),
		space("ca-test1", "catalog-api-us-east-test1", "catalog-api", "deployment", "test", "us-east-test1", "t-test1"),
		space("ca-test2", "catalog-api-us-east-test2", "catalog-api", "deployment", "test", "us-east-test2", "t-test2"),
		space("ca-prod1", "catalog-api-us-east-prod1", "catalog-api", "deployment", "prod", "us-east-prod1", "t-prod1"),
		space("ca-prod2", "catalog-api-us-east-prod2", "catalog-api", "deployment", "prod", "us-east-prod2", "t-prod2"),
		space("ca-prod3", "catalog-api-us-east-prod3", "catalog-api", "deployment", "prod", "us-east-prod3", "t-prod3"),
		space("wf", "workflows-platform", "", "", "", "", ""),
	}
	ids := func(s ...string) []any {
		out := make([]any, len(s))
		for i, x := range s {
			out[i] = x
		}
		return out
	}
	cmScope := ids("cm-base", "cm-dev", "cm-test", "cm-prod", "cm-dev1", "cm-test1", "cm-test2", "cm-prod1", "cm-prod2", "cm-prod3")
	caScope := ids("ca-base", "ca-dev", "ca-test", "ca-prod", "ca-dev1", "ca-test1", "ca-test2", "ca-prod1", "ca-prod2", "ca-prod3")
	// The workflow copy every order carries, as the server stamps it.
	workflow := map[string]any{
		"Stages": []any{
			map[string]any{"Name": "bases", "WhereSpace": "Labels.DemoName = 'workflows' AND Labels.Role = 'base' AND Labels.Variant IN ('dev', 'test', 'prod')"},
			map[string]any{"Name": "dev", "WhereSpace": "Labels.DemoName = 'workflows' AND Labels.Role = 'deployment' AND Labels.Stage = 'dev'"},
			map[string]any{"Name": "test", "WhereSpace": "Labels.DemoName = 'workflows' AND Labels.Role = 'deployment' AND Labels.Stage = 'test'", "Prerequisites": []any{"Released"}},
			map[string]any{"Name": "prod", "WhereSpace": "Labels.DemoName = 'workflows' AND Labels.Role = 'deployment' AND Labels.Stage = 'prod'", "Prerequisites": []any{"Released", "Healthy"}},
		},
		"Final": map[string]any{"Prerequisites": []any{"Released", "Healthy"}},
	}
	orders := []cubclient.Row{
		{"ChangeOrder": map[string]any{
			"ChangeOrderID": "co-cm", "Slug": "cert-manager-1-17-0", "SpaceID": "cm-base", "State": "InProgress", "Stage": "test", "UpdateType": "UpgradeUnit",
			"Description": "Bump cert-manager to v1.17.0", "CreatedAt": "2026-09-03T15:44:46Z",
			"StartTagID": "tag-cm-start", "EndTagID": "tag-cm-end", "ChangeWorkflowID": "wf-cm", "ChangeWorkflow": workflow,
			"InScopeSpaceIDs":  cmScope,
			"ResolvedSpaceIDs": ids("cm-base", "cm-dev", "cm-test", "cm-prod", "cm-dev1", "cm-test1", "cm-test2"),
			"ReleasedSpaceIDs": ids("cm-dev1", "cm-test1", "cm-test2"),
			"SkippedUnits":     map[string]any{"u-cm-ns": "already promoted through revision 2; marked, but carrying no revisions"},
			"Promotions": []any{
				map[string]any{"PromotedAt": "2026-09-03T16:00:00Z", "Stage": "bases", "UserID": "u-jesper", "SpaceIDs": ids("cm-dev", "cm-test", "cm-prod")},
				map[string]any{"PromotedAt": "2026-09-03T16:10:00Z", "Stage": "dev", "UserID": "u-jesper", "SpaceIDs": ids("cm-dev1")},
				map[string]any{"PromotedAt": "2026-09-03T16:20:00Z", "Stage": "test", "UserID": "u-jesper", "SpaceIDs": ids("cm-test1", "cm-test2")},
			},
			"Releases": []any{
				map[string]any{"SpaceID": "cm-dev1", "ReleaseID": "rel-dev1-3", "ReleaseNum": 3},
				map[string]any{"SpaceID": "cm-test1", "ReleaseID": "rel-test1-3", "ReleaseNum": 3},
				map[string]any{"SpaceID": "cm-test2", "ReleaseID": "rel-test2-3", "ReleaseNum": 3},
			},
		}, "Space": map[string]any{"SpaceID": "cm-base", "Slug": "cert-manager-base"}},
		{"ChangeOrder": map[string]any{
			"ChangeOrderID": "co-ca", "Slug": "catalog-api-5-3-0", "SpaceID": "ca-base", "State": "New", "Stage": "", "UpdateType": "UpgradeUnit",
			"Description": "catalog-api 5.3.0", "CreatedAt": "2026-09-05T10:00:00Z",
			"StartTagID": "tag-ca-start", "EndTagID": "tag-ca-end", "ChangeWorkflowID": "wf-ca", "ChangeWorkflow": workflow,
			"InScopeSpaceIDs":  caScope,
			"ResolvedSpaceIDs": ids("ca-base"),
		}, "Space": map[string]any{"SpaceID": "ca-base", "Slug": "catalog-api-base"}},
		{"ChangeOrder": map[string]any{
			"ChangeOrderID": "co-old", "Slug": "traefik-3-0-0", "SpaceID": "cm-base", "State": "Released",
			"Description": "old", "CreatedAt": "2026-08-01T00:00:00Z",
		}, "Space": map[string]any{"SpaceID": "cm-base", "Slug": "cert-manager-base"}},
	}
	live := func(health, msg string) map[string]any {
		return map[string]any{"Reporter": "cub-demo/argocd", "Sync": "Synced", "Health": health, "Operation": "Succeeded", "ObservedAt": "2026-09-03T15:44:42Z", "Message": msg}
	}
	release := func(space string, n int, order string, status map[string]any) cubclient.Row {
		rel := map[string]any{"ReleaseID": fmt.Sprintf("rel-%s-%d", strings.TrimPrefix(space, "cm-"), n), "SpaceID": space, "ReleaseNum": n, "Published": true, "LiveStatus": status}
		if order != "" {
			rel["ChangeOrderID"] = order
		}
		return cubclient.Row{"Release": rel}
	}
	releases := []cubclient.Row{
		release("cm-dev1", 2, "", live("Healthy", "")),
		release("cm-dev1", 3, "co-cm", live("Healthy", "")),
		release("cm-test1", 3, "co-cm", live("Degraded", "cert-manager-1-17-0: rollout stalled")),
		release("cm-test2", 3, "co-cm", live("Healthy", "")),
		// prod runs the previous state, healthily
		release("cm-prod1", 2, "", live("Healthy", "")),
		release("cm-prod2", 2, "", live("Healthy", "")),
		release("cm-prod3", 2, "", live("Healthy", "")),
	}
	unit := func(id, space, slug, upstream string) cubclient.Row {
		u := map[string]any{"UnitID": id, "SpaceID": space, "Slug": slug}
		if upstream != "" {
			u["UpstreamUnitID"] = upstream
		}
		return cubclient.Row{"Unit": u}
	}
	// catalog-api units: the base's two, cloned into every class base and
	// deployment; dev1 lacks the config unit so a preview there clones it.
	units := []cubclient.Row{
		unit("u-cm-ctl", "cm-base", "controller", ""), unit("u-cm-ns", "cm-base", "namespace", ""), unit("u-cm-wh", "cm-base", "webhook", ""),
		unit("u-d1-ctl", "cm-dev1", "controller", "u-cmdev-ctl"), unit("u-d1-ns", "cm-dev1", "namespace", "u-cmdev-ns"),
		unit("u-cmdev-ctl", "cm-dev", "controller", "u-cm-ctl"), unit("u-cmdev-ns", "cm-dev", "namespace", "u-cm-ns"),
		unit("u-ca-api", "ca-base", "api", ""), unit("u-ca-cfg", "ca-base", "config", ""),
		unit("u-cadev-api", "ca-dev", "api", "u-ca-api"), unit("u-cadev-cfg", "ca-dev", "config", "u-ca-cfg"),
		unit("u-catest-api", "ca-test", "api", "u-ca-api"), unit("u-catest-cfg", "ca-test", "config", "u-ca-cfg"),
		unit("u-caprod-api", "ca-prod", "api", "u-ca-api"), unit("u-caprod-cfg", "ca-prod", "config", "u-ca-cfg"),
		unit("u-cadev1-api", "ca-dev1", "api", "u-cadev-api"),
		unit("u-catest1-api", "ca-test1", "api", "u-catest-api"), unit("u-catest1-cfg", "ca-test1", "config", "u-catest-cfg"),
		unit("u-catest2-api", "ca-test2", "api", "u-catest-api"), unit("u-catest2-cfg", "ca-test2", "config", "u-catest-cfg"),
	}
	unitData := []cubclient.Row{
		{"UnitID": "u-cadev-api", "SpaceID": "ca-dev", "Data": "image: catalog-api:5.2.0\nmemory: 512Mi\n"},
		{"UnitID": "u-cadev-cfg", "SpaceID": "ca-dev", "Data": "log: info\n"},
		{"UnitID": "u-catest-api", "SpaceID": "ca-test", "Data": "image: catalog-api:5.2.0\nmemory: 2Gi\n"},
		{"UnitID": "u-catest-cfg", "SpaceID": "ca-test", "Data": "log: info\n"},
		{"UnitID": "u-caprod-api", "SpaceID": "ca-prod", "Data": "image: catalog-api:5.2.0\nmemory: 2Gi\n"},
		{"UnitID": "u-caprod-cfg", "SpaceID": "ca-prod", "Data": "log: warn\n"},
		{"UnitID": "u-cadev1-api", "SpaceID": "ca-dev1", "Data": "image: catalog-api:5.2.0\nmemory: 512Mi\n"},
		{"UnitID": "u-catest1-api", "SpaceID": "ca-test1", "Data": "image: catalog-api:5.2.0\nmemory: 2Gi\n"},
		{"UnitID": "u-catest1-cfg", "SpaceID": "ca-test1", "Data": "log: info\n"},
		{"UnitID": "u-catest2-api", "SpaceID": "ca-test2", "Data": "image: catalog-api:5.2.0\nmemory: 2Gi\n"},
		{"UnitID": "u-catest2-cfg", "SpaceID": "ca-test2", "Data": "log: info\n"},
		{"UnitID": "u-d1-ctl", "SpaceID": "cm-dev1", "Data": "image: quay.io/jetstack/cert-manager-controller:v1.17.0\nreplicas: 1\n"},
		{"UnitID": "u-d1-ns", "SpaceID": "cm-dev1", "Data": "name: cert-manager\n"},
	}
	// The server's diffs: flat documents, so the resource has no identity.
	diff := func(changes ...[3]string) map[string]any {
		var cs []any
		for _, c := range changes {
			cs = append(cs, map[string]any{"Path": c[0], "DisplayPath": c[0], "Segments": []any{map[string]any{"Field": c[0], "FromIndex": -1, "ToIndex": -1}}, "ChangeType": "Update", "FromValue": c[1], "ToValue": c[2]})
		}
		return map[string]any{"Resources": []any{map[string]any{"Resource": map[string]any{"ResourceType": "", "ResourceName": ""}, "ChangeType": "Update", "Changes": cs}}}
	}
	none := map[string]any{"Resources": []any{}}
	unitDiff := func(space, unit string, from, to int, d map[string]any) cubclient.Row {
		return cubclient.Row{"SpaceID": space, "UnitID": unit, "FromRevisionNum": float64(from), "ToRevisionNum": float64(to), "Diff": d}
	}
	image := [3]string{"image", "quay.io/jetstack/cert-manager-controller:v1.16.0", "quay.io/jetstack/cert-manager-controller:v1.17.0"}
	apiImage := [3]string{"image", "catalog-api:5.2.0", "catalog-api:5.3.0"}
	apiMemory := [3]string{"memory", "512Mi", "1Gi"}
	unitDiffs := []cubclient.Row{
		// cert-manager base: controller bumped (rev 2 → 3), the rest marked on rev 2
		unitDiff("cm-base", "u-cm-ctl", 2, 3, diff(image)),
		unitDiff("cm-base", "u-cm-ns", 2, 2, none),
		unitDiff("cm-base", "u-cm-wh", 2, 2, none),
		// what the promotion wrote in dev1
		unitDiff("cm-dev1", "u-d1-ctl", 2, 3, diff(image)),
		unitDiff("cm-dev1", "u-d1-ns", 2, 2, none),
		// catalog-api base: api bumped twice (2 → 4)
		unitDiff("ca-base", "u-ca-api", 2, 4, diff(apiImage, apiMemory)),
		unitDiff("ca-base", "u-ca-cfg", 1, 1, none),
		// the test class base as if promoted: it took the image and kept its 2Gi
		unitDiff("ca-test", "u-catest-api", 1, 2, diff(apiImage)),
		unitDiff("ca-test", "u-catest-cfg", 1, 1, none),
	}
	protectedMemory := []any{map[string]any{"Path": "memory", "Reason": "Protected", "Details": "the path is a protected local override", "Resource": map[string]any{"ResourceType": "", "ResourceName": ""}}}
	mem := &MemClient{
		Rows: map[string][]cubclient.Row{
			"/space":        spaces,
			"/change_order": orders,
			"/change_workflow": {
				{"ChangeWorkflow": map[string]any{"ChangeWorkflowID": "wf-cm", "Slug": "cert-manager-workflow", "SpaceID": "wf"}},
				{"ChangeWorkflow": map[string]any{"ChangeWorkflowID": "wf-ca", "Slug": "catalog-api-workflow", "SpaceID": "wf"}},
			},
			"/release":   releases,
			"/unit":      units,
			"/unit_data": unitData,
			"/unit_diff": unitDiffs,
		},
		Raw: map[string]string{
			"/space/ca-test/unit/u-catest-api/mutation_sources": `{"MutationSources":[{"Resource":{"ResourceType":"","ResourceName":""},"PathMutationMap":{"memory":{"Protected":true}}}]}`,
		},
		// The dry run: the api unit takes the base's image and memory; test
		// and prod keep their 2Gi, which is protected there and withheld.
		Upgrades: map[string]Upgrade{
			"u-cadev-api":   {Diff: diff(apiImage, apiMemory)},
			"u-catest-api":  {Diff: diff(apiImage), Conflicts: protectedMemory},
			"u-caprod-api":  {Diff: diff(apiImage), Conflicts: protectedMemory},
			"u-cadev1-api":  {Diff: diff(apiImage, apiMemory)},
			"u-catest1-api": {Diff: diff(apiImage), Conflicts: protectedMemory},
			"u-catest2-api": {Diff: diff(apiImage), Conflicts: protectedMemory},
		},
	}
	return mem
}
