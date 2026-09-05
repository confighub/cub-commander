package rollout

import (
	"context"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"sync"

	"github.com/confighub/cub-commander/internal/cubclient"
)

// MemClient answers List and GetRaw from canned rows: a tiny where evaluator
// over entity-keyed rows, enough for the queries this package makes. Tests
// in several packages use it, so it is not a _test file.
type MemClient struct {
	mu   sync.Mutex
	Rows map[string][]cubclient.Row // path → rows
	Raw  map[string]string          // path → body
	Log  []string                   // every request, for assertions
	// OnPatch answers PATCH requests; nil refuses them. Patches records each.
	OnPatch func(path string, q url.Values, body string) ([]cubclient.Row, int, error)
	Patches []url.Values
	// OnPost answers POSTs; nil answers a release with a fake row. Posts records each.
	OnPost func(path, body string) (cubclient.Row, error)
	Posts  []string
}

func (m *MemClient) PostRow(_ context.Context, path string, body string) (cubclient.Row, error) {
	m.mu.Lock()
	m.Log = append(m.Log, "POST "+path+" "+body)
	m.Posts = append(m.Posts, path+" "+body)
	m.mu.Unlock()
	if m.OnPost != nil {
		return m.OnPost(path, body)
	}
	return cubclient.Row{"ReleaseID": fmt.Sprintf("rel-%d", len(m.Posts)), "ReleaseNum": float64(len(m.Posts))}, nil
}

func (m *MemClient) PatchRows(_ context.Context, path string, q url.Values, body string) ([]cubclient.Row, int, error) {
	m.mu.Lock()
	m.Log = append(m.Log, "PATCH "+path+"?"+q.Encode())
	m.Patches = append(m.Patches, q)
	m.mu.Unlock()
	if m.OnPatch == nil {
		return nil, 0, fmt.Errorf("MemClient: no PATCH handler for %s", path)
	}
	return m.OnPatch(path, q, body)
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

// ChapterOne is the change-workflows demo's opening state, shaped like the
// live rows (Demo org, 2026-09-05): component cert-manager with a base,
// three class bases and six deployments; change order cert-manager-1-17-0
// taken through test and released in dev and both test clusters, with
// us-east-test1 Degraded; and catalog-api with a fresh change order nothing
// has taken. The workflow is the demo's four-stage line.
func ChapterOne() *MemClient {
	space := func(id, slug, comp, role, stage, variant, target, live string) cubclient.Row {
		labels := map[string]any{"Component": comp, "Role": role, "Variant": variant, "DemoName": "workflows"}
		if stage != "" {
			labels["Stage"] = stage
		}
		ann := map[string]any{}
		if live != "" {
			ann["confighub.com/live-status"] = live
		}
		// the variant tree: class bases clone the base, deployments their class base
		prefix := strings.SplitN(id, "-", 2)[0]
		switch role {
		case "base":
			if variant != "base" {
				ann["UpstreamSpaceID"] = prefix + "-base"
			}
		case "deployment":
			ann["UpstreamSpaceID"] = prefix + "-" + stage
		}
		sp := map[string]any{"SpaceID": id, "Slug": slug, "Labels": labels, "Annotations": ann}
		if target != "" {
			sp["ReleaseTargetID"] = target
		}
		return cubclient.Row{"Space": sp}
	}
	healthy := `{"source":"cub-demo/argocd","syncStatus":"Synced","healthStatus":"Healthy","operationPhase":"Succeeded","observedAt":"2026-09-03T15:44:42Z"}`
	degraded := `{"source":"cub-demo/argocd","syncStatus":"Synced","healthStatus":"Degraded","operationPhase":"Succeeded","observedAt":"2026-09-03T15:44:56Z","message":"cert-manager-1-17-0: rollout stalled"}`
	spaces := []cubclient.Row{
		space("cm-base", "cert-manager-base", "cert-manager", "base", "", "base", "", ""),
		space("cm-dev", "cert-manager-dev", "cert-manager", "base", "dev", "dev", "", ""),
		space("cm-test", "cert-manager-test", "cert-manager", "base", "test", "test", "", ""),
		space("cm-prod", "cert-manager-prod", "cert-manager", "base", "prod", "prod", "", ""),
		space("cm-dev1", "cert-manager-us-east-dev1", "cert-manager", "deployment", "dev", "us-east-dev1", "t-dev1", healthy),
		space("cm-test1", "cert-manager-us-east-test1", "cert-manager", "deployment", "test", "us-east-test1", "t-test1", degraded),
		space("cm-test2", "cert-manager-us-east-test2", "cert-manager", "deployment", "test", "us-east-test2", "t-test2", healthy),
		space("cm-prod1", "cert-manager-us-east-prod1", "cert-manager", "deployment", "prod", "us-east-prod1", "t-prod1", healthy),
		space("cm-prod2", "cert-manager-us-east-prod2", "cert-manager", "deployment", "prod", "us-east-prod2", "t-prod2", healthy),
		space("cm-prod3", "cert-manager-us-east-prod3", "cert-manager", "deployment", "prod", "us-east-prod3", "t-prod3", healthy),
		space("ca-base", "catalog-api-base", "catalog-api", "base", "", "base", "", ""),
		space("ca-dev", "catalog-api-dev", "catalog-api", "base", "dev", "dev", "", ""),
		space("ca-test", "catalog-api-test", "catalog-api", "base", "test", "test", "", ""),
		space("ca-prod", "catalog-api-prod", "catalog-api", "base", "prod", "prod", "", ""),
		space("ca-dev1", "catalog-api-us-east-dev1", "catalog-api", "deployment", "dev", "us-east-dev1", "t-dev1", ""),
		space("ca-test1", "catalog-api-us-east-test1", "catalog-api", "deployment", "test", "us-east-test1", "t-test1", ""),
		space("ca-test2", "catalog-api-us-east-test2", "catalog-api", "deployment", "test", "us-east-test2", "t-test2", ""),
		space("ca-prod1", "catalog-api-us-east-prod1", "catalog-api", "deployment", "prod", "us-east-prod1", "t-prod1", ""),
		space("ca-prod2", "catalog-api-us-east-prod2", "catalog-api", "deployment", "prod", "us-east-prod2", "t-prod2", ""),
		space("ca-prod3", "catalog-api-us-east-prod3", "catalog-api", "deployment", "prod", "us-east-prod3", "t-prod3", ""),
		space("wf", "workflows-platform", "", "", "", "", "", ""),
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
	orders := []cubclient.Row{
		{"ChangeOrder": map[string]any{
			"ChangeOrderID": "co-cm", "Slug": "cert-manager-1-17-0", "SpaceID": "cm-base", "State": "InProgress",
			"Description": "Bump cert-manager to v1.17.0", "CreatedAt": "2026-09-03T15:44:46Z",
			"StartTagID": "tag-cm-start", "EndTagID": "tag-cm-end",
			"InScopeSpaceIDs":  cmScope,
			"ResolvedSpaceIDs": ids("cm-base", "cm-dev", "cm-test", "cm-prod", "cm-dev1", "cm-test1", "cm-test2"),
			"ReleasedSpaceIDs": ids("cm-dev1", "cm-test1", "cm-test2"),
			"Annotations":      map[string]any{"confighub.com/change-workflow-unit-id": "wf-cm", "confighub.com/change-workflow-revision-num": "2"},
			"SkippedUnits":     map[string]any{"u-cm-ns": "already promoted through revision 2; marked, but carrying no revisions"},
		}, "Space": map[string]any{"SpaceID": "cm-base", "Slug": "cert-manager-base"}},
		{"ChangeOrder": map[string]any{
			"ChangeOrderID": "co-ca", "Slug": "catalog-api-5-3-0", "SpaceID": "ca-base", "State": "New",
			"Description": "catalog-api 5.3.0", "CreatedAt": "2026-09-05T10:00:00Z",
			"StartTagID": "tag-ca-start", "EndTagID": "tag-ca-end",
			"InScopeSpaceIDs":  caScope,
			"ResolvedSpaceIDs": ids("ca-base"),
			"Annotations":      map[string]any{"confighub.com/change-workflow-unit-id": "wf-ca", "confighub.com/change-workflow-revision-num": "3"},
		}, "Space": map[string]any{"SpaceID": "ca-base", "Slug": "catalog-api-base"}},
		{"ChangeOrder": map[string]any{
			"ChangeOrderID": "co-old", "Slug": "traefik-3-0-0", "SpaceID": "cm-base", "State": "Released",
			"Description": "old", "CreatedAt": "2026-08-01T00:00:00Z", "Annotations": map[string]any{},
		}, "Space": map[string]any{"SpaceID": "cm-base", "Slug": "cert-manager-base"}},
	}
	wfDoc := func(name string) string {
		return fmt.Sprintf(`apiVersion: confighub.com/v1
kind: ChangeWorkflow
metadata:
  name: %s
spec:
  stages:
    - name: bases
      whereSpace: "Labels.DemoName = 'workflows' AND Labels.Role = 'base' AND Labels.Variant IN ('dev', 'test', 'prod')"
    - name: dev
      whereSpace: "Labels.DemoName = 'workflows' AND Labels.Role = 'deployment' AND Labels.Stage = 'dev'"
    - name: test
      whereSpace: "Labels.DemoName = 'workflows' AND Labels.Role = 'deployment' AND Labels.Stage = 'test'"
      prerequisites: [released]
    - name: prod
      whereSpace: "Labels.DemoName = 'workflows' AND Labels.Role = 'deployment' AND Labels.Stage = 'prod'"
      prerequisites: [released, healthy]
  final:
    prerequisites: [released, healthy]
`, name)
	}
	rev := func(id, unit, space, slug string, num int, tags ...string) cubclient.Row {
		t := map[string]any{}
		for _, tg := range tags {
			t[tg] = tg
		}
		return cubclient.Row{
			"Revision": map[string]any{"RevisionID": id, "UnitID": unit, "SpaceID": space, "RevisionNum": float64(num), "Tags": t},
			"Unit":     map[string]any{"UnitID": unit, "Slug": slug},
		}
	}
	revisions := []cubclient.Row{
		// cert-manager base: controller bumped (rev 2 → 3), the rest marked on rev 2
		rev("r-ctl-2", "u-cm-ctl", "cm-base", "controller", 2, "tag-cm-start"),
		rev("r-ctl-3", "u-cm-ctl", "cm-base", "controller", 3, "tag-cm-end"),
		rev("r-ns-2", "u-cm-ns", "cm-base", "namespace", 2, "tag-cm-start", "tag-cm-end"),
		rev("r-wh-2", "u-cm-wh", "cm-base", "webhook", 2, "tag-cm-start", "tag-cm-end"),
		// what the promotion wrote in dev1
		rev("r-d1-ctl-2", "u-d1-ctl", "cm-dev1", "controller", 2, "tag-cm-start"),
		rev("r-d1-ctl-3", "u-d1-ctl", "cm-dev1", "controller", 3, "tag-cm-end"),
		rev("r-d1-ns-2", "u-d1-ns", "cm-dev1", "namespace", 2, "tag-cm-start", "tag-cm-end"),
		// catalog-api base: api bumped twice (2 → 4)
		rev("r-api-2", "u-ca-api", "ca-base", "api", 2, "tag-ca-start"),
		rev("r-api-4", "u-ca-api", "ca-base", "api", 4, "tag-ca-end"),
		rev("r-cfg-1", "u-ca-cfg", "ca-base", "config", 1, "tag-ca-start", "tag-ca-end"),
		// the dev class base marked as if the bases stage had landed there, so the
		// dev stage's preview can notice that dev1 lacks the config unit
		rev("r-cadev-api-1", "u-cadev-api", "ca-dev", "api", 1, "tag-ca-start"),
		rev("r-cadev-cfg-1", "u-cadev-cfg", "ca-dev", "config", 1, "tag-ca-start"),
		// the test class base as if promoted: it took the image and kept its 2Gi
		rev("r-catest-api-1", "u-catest-api", "ca-test", "api", 1, "tag-ca-start"),
		rev("r-catest-api-2", "u-catest-api", "ca-test", "api", 2, "tag-ca-end"),
		rev("r-catest-cfg-1", "u-catest-cfg", "ca-test", "config", 1, "tag-ca-start", "tag-ca-end"),
	}
	data := func(id, body string) cubclient.Row {
		return cubclient.Row{"RevisionID": id, "Data": body}
	}
	// catalog-api units: the base's two, cloned into every class base and
	// deployment; dev1 lacks the config unit so a preview there reports it.
	unit := func(id, space, slug, upstream string) cubclient.Row {
		u := map[string]any{"UnitID": id, "SpaceID": space, "Slug": slug}
		if upstream != "" {
			u["UpstreamUnitID"] = upstream
		}
		return cubclient.Row{"Unit": u}
	}
	units := []cubclient.Row{
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
	}
	revData := []cubclient.Row{
		data("r-ctl-2", "image: quay.io/jetstack/cert-manager-controller:v1.16.0\nreplicas: 1\n"),
		data("r-ctl-3", "image: quay.io/jetstack/cert-manager-controller:v1.17.0\nreplicas: 1\n"),
		data("r-d1-ctl-2", "image: quay.io/jetstack/cert-manager-controller:v1.16.0\nreplicas: 1\n"),
		data("r-d1-ctl-3", "image: quay.io/jetstack/cert-manager-controller:v1.17.0\nreplicas: 1\n"),
		data("r-api-2", "image: catalog-api:5.2.0\nmemory: 512Mi\n"),
		data("r-api-4", "image: catalog-api:5.3.0\nmemory: 1Gi\n"),
		data("r-catest-api-1", "image: catalog-api:5.2.0\nmemory: 2Gi\n"),
		data("r-catest-api-2", "image: catalog-api:5.3.0\nmemory: 2Gi\n"),
	}
	mem := &MemClient{
		Rows: map[string][]cubclient.Row{
			"/space":        spaces,
			"/change_order": orders,
			"/unit_data":    unitData,
			"/unit": append([]cubclient.Row{
				{"Unit": map[string]any{"UnitID": "wf-cm", "SpaceID": "wf", "Slug": "cert-manager-workflow"}},
				{"Unit": map[string]any{"UnitID": "wf-ca", "SpaceID": "wf", "Slug": "catalog-api-workflow"}},
			}, units...),
			"/space/wf/unit/wf-cm/revision": {{"Revision": map[string]any{"RevisionID": "wfr-cm-2", "RevisionNum": 2.0}}},
			"/space/wf/unit/wf-ca/revision": {{"Revision": map[string]any{"RevisionID": "wfr-ca-3", "RevisionNum": 3.0}}},
			"/revision":                     revisions,
			"/revision_data":                revData,
		},
		Raw: map[string]string{
			"/space/wf/unit/wf-cm/revision/wfr-cm-2/data":       wfDoc("cert-manager"),
			"/space/wf/unit/wf-ca/revision/wfr-ca-3/data":       wfDoc("catalog-api"),
			"/space/ca-test/unit/u-catest-api/mutation_sources": `{"MutationSources":[{"Resource":{"ResourceType":"","ResourceName":""},"PathMutationMap":{"memory":{"Protected":true}}}]}`,
		},
	}
	// The dry run: every unit of the space with an upstream, the api unit
	// taking the base's image and memory (test/prod keep their 2Gi: the
	// field is protected there), config unchanged.
	mem.OnPatch = func(path string, q url.Values, body string) ([]cubclient.Row, int, error) {
		where := q.Get("where")
		var out []cubclient.Row
		for _, row := range units {
			u := row["Unit"].(map[string]any)
			if !strings.Contains(where, "SpaceID = '"+str(u["SpaceID"])+"'") || str(u["UpstreamUnitID"]) == "" {
				continue
			}
			resp := cubclient.Row{"Unit": map[string]any{"UnitID": u["UnitID"], "Slug": u["Slug"], "SpaceID": u["SpaceID"]}}
			if q.Get("dry_run") == "true" {
				cur := ""
				for _, d := range unitData {
					if d["UnitID"] == u["UnitID"] {
						cur = str(d["Data"])
					}
				}
				if str(u["Slug"]) == "api" {
					mem := "1Gi"
					if strings.Contains(cur, "2Gi") {
						// test and prod protect the limit; the merge keeps it and says so
						mem = "2Gi"
						resp["MutationSources"] = []any{map[string]any{
							"Resource":        map[string]any{"ResourceType": "", "ResourceName": ""},
							"PathMutationMap": map[string]any{"memory": map[string]any{"Protected": true}},
						}}
					}
					resp["ConfigData"] = "image: catalog-api:5.3.0\nmemory: " + mem + "\n"
				} else {
					resp["ConfigData"] = cur
				}
			}
			out = append(out, resp)
		}
		return out, 200, nil
	}
	return mem
}
