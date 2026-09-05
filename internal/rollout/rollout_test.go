package rollout

import (
	"context"
	"strings"
	"testing"
)

func order(t *testing.T, c *MemClient, id string) *Rollout {
	t.Helper()
	rows, err := c.List(context.Background(), "/change_order", map[string][]string{"where": {"ChangeOrderID = '" + id + "'"}})
	if err != nil || len(rows) != 1 {
		t.Fatalf("order %s: %v (%d rows)", id, err, len(rows))
	}
	r, err := Load(context.Background(), c, NewCache(), rows[0])
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestBlockedByHealth(t *testing.T) {
	c := ChapterOne()
	r := order(t, c, "co-cm")
	if r.Err != "" {
		t.Fatal(r.Err)
	}
	if r.Component != "cert-manager" || r.WorkflowRef != "cert-manager @rev 2" {
		t.Errorf("component %q workflow %q", r.Component, r.WorkflowRef)
	}
	names := []string{}
	for _, s := range r.Stages {
		names = append(names, s.Name)
	}
	if got := strings.Join(names, ","); got != "source,bases,dev,test,prod" {
		t.Errorf("stages %s", got)
	}
	if r.NextName() != "prod" || r.Reached() != "test" {
		t.Errorf("next %q reached %q", r.NextName(), r.Reached())
	}
	if len(r.Stages[4].Spaces) != 3 || len(r.Stages[3].Spaces) != 2 {
		t.Errorf("prod has %d spaces, test %d", len(r.Stages[4].Spaces), len(r.Stages[3].Spaces))
	}
	ok, total := Tally(r.Gates)
	if ok != 2 || total != 3 {
		t.Errorf("gates %d of %d: %+v", ok, total, r.Gates)
	}
	if r.Gates[2].Name != PrereqHealthy || r.Gates[2].Reason != "Variant 'us-east-test1' is not healthy" {
		t.Errorf("healthy gate: %+v", r.Gates[2])
	}
	if r.State != StateDegraded || r.Blocker != "Variant 'us-east-test1' is not healthy" {
		t.Errorf("state %q blocker %q", r.State, r.Blocker)
	}
	if r.Completed {
		t.Error("completed")
	}
	taken, released, healthy := r.Stages[3].Counts()
	if taken != 2 || released != 2 || healthy != 1 {
		t.Errorf("test counts %d %d %d", taken, released, healthy)
	}
	// prod's clusters report Healthy, but on the previous state: nothing is
	// released there, so the change is not healthy anywhere in prod yet
	if _, _, healthy := r.Stages[4].Counts(); healthy != 0 {
		t.Errorf("prod healthy %d before any release", healthy)
	}
	// the stage clause carries the component, and the workflow is read once
	sawComponent := false
	for _, l := range c.Log {
		if strings.Contains(l, "Labels.Component+%3D+%27cert-manager%27") {
			sawComponent = true
		}
	}
	if !sawComponent {
		t.Errorf("stage clauses did not name the component: %v", c.Log)
	}
}

func TestFreshOrderIsReady(t *testing.T) {
	r := order(t, ChapterOne(), "co-ca")
	if r.NextName() != "bases" || r.Reached() != "" {
		t.Errorf("next %q reached %q", r.NextName(), r.Reached())
	}
	if r.State != StateReady || r.Blocker != NoBlocker {
		t.Errorf("state %q blocker %q", r.State, r.Blocker)
	}
	if ok, total := Tally(r.Gates); ok != 1 || total != 1 {
		t.Errorf("gates %d of %d", ok, total)
	}
	if !strings.Contains(strings.Join(r.CubCommands(), "\n"), "cub variant promote --change-order catalog-api-base/catalog-api-5-3-0 --target-stage bases --dry-run") {
		t.Errorf("cub commands: %v", r.CubCommands())
	}
}

func TestNoWorkflow(t *testing.T) {
	r := order(t, ChapterOne(), "co-old")
	if r.State != StateNoWorkflow || len(r.Stages) != 1 || r.NextName() != "" {
		t.Errorf("%+v", r)
	}
}

func TestDeriveReleaseGateAndCompletion(t *testing.T) {
	c := ChapterOne()
	r := order(t, c, "co-cm")
	// Take the release back from test2: the gate ahead of prod is now released.
	for i := range r.Stages[3].Spaces {
		if r.Stages[3].Spaces[i].Slug == "cert-manager-us-east-test2" {
			r.Stages[3].Spaces[i].Released = false
		}
	}
	r.State, r.Blocker = "", ""
	derive(r)
	if r.State != StateBlocked || !strings.Contains(r.Blocker, "has taken change order 'cert-manager-1-17-0' but has not released it") {
		t.Errorf("state %q blocker %q", r.State, r.Blocker)
	}
	// Land it everywhere, released and healthy: complete.
	for si := range r.Stages {
		for i := range r.Stages[si].Spaces {
			sp := &r.Stages[si].Spaces[i]
			sp.Taken, sp.Released = true, true
			if sp.Releasable {
				sp.Health = Health{Present: true, Sync: "Synced", Phase: "Succeeded", Status: "Healthy"}
			}
		}
	}
	r.State, r.Blocker = "", ""
	derive(r)
	if r.Next != -1 || !r.Completed || r.State != StateComplete || r.Reached() != "prod" {
		t.Errorf("next %d completed %v state %q reached %q", r.Next, r.Completed, r.State, r.Reached())
	}
	// Prod taken but not yet healthy: reached prod, not complete, degraded by final.
	for i := range r.Stages[4].Spaces {
		r.Stages[4].Spaces[i].Health = Health{}
	}
	r.State, r.Blocker = "", ""
	derive(r)
	if r.Completed || r.State != StateDegraded || !strings.HasPrefix(r.Blocker, "live-status not found for Variant 'us-east-prod1'") {
		t.Errorf("completed %v state %q blocker %q", r.Completed, r.State, r.Blocker)
	}
}

func TestChangeFromTags(t *testing.T) {
	c := ChapterOne()
	r := order(t, c, "co-cm")
	ch, err := Change(context.Background(), c, r.Order, "cm-base")
	if err != nil {
		t.Fatal(err)
	}
	if len(ch) != 3 || !ch[0].Touched || ch[0].Slug != "controller" || ch[1].Touched || ch[2].Touched {
		t.Fatalf("%+v", ch)
	}
	if ch[0].StartRev != 2 || ch[0].EndRev != 3 || !strings.Contains(ch[0].Before, "v1.16.0") || !strings.Contains(ch[0].After, "v1.17.0") {
		t.Errorf("%+v", ch[0])
	}
	// what the promotion wrote in dev1 reads the same way
	ch, err = Change(context.Background(), c, r.Order, "cm-dev1")
	if err != nil || len(ch) != 2 || !ch[0].Touched || ch[0].Slug != "controller" {
		t.Fatalf("%v %+v", err, ch)
	}
	// one revision_data call for the touched pair
	n := 0
	for _, l := range c.Log {
		if strings.HasPrefix(l, "/revision_data") {
			n++
		}
	}
	if n != 2 {
		t.Errorf("revision_data calls: %d", n)
	}
}

func TestParseWorkflowRefusesOtherKinds(t *testing.T) {
	if _, err := ParseWorkflow("kind: Deployment\n"); err == nil {
		t.Error("accepted a Deployment")
	}
	wf, err := ParseWorkflow("kind: ChangeWorkflow\nmetadata:\n  name: x\nspec:\n  stages:\n    - name: a\n      whereSpace: \"Labels.Stage = 'a'\"\n")
	if err != nil || wf.Name != "x" || len(wf.Stages) != 1 {
		t.Errorf("%v %+v", err, wf)
	}
	if _, err := stageWhere(Stage{Name: "s", WhereSpace: "Labels.Component = 'x'"}, "x"); err == nil || !strings.Contains(err.Error(), "names Labels.Component") {
		t.Errorf("component predicate not refused: %v", err)
	}
}

func TestSemanticIgnoresLayout(t *testing.T) {
	before := "apiVersion: apps/v1\nkind: Deployment\nmetadata:\n  name: api\nspec:\n  template:\n    spec:\n      containers:\n      - name: api\n        image: catalog-api:5.2.0\n        resources:\n          limits:\n            memory: 512Mi\n      - name: sidecar\n        image: envoy:1\n"
	after := "apiVersion: apps/v1\nkind: Deployment\nmetadata:\n  name: api\nspec:\n  template:\n    spec:\n      containers:\n        - name: api\n          image: \"catalog-api:5.3.0\"\n          resources:\n            limits:\n              memory: 1Gi\n        - name: sidecar\n          image: envoy:1\n"
	fields, na, nb, fmtOnly := Semantic(before, after)
	if fmtOnly || len(fields) != 2 {
		t.Fatalf("fields %+v formattingOnly %v", fields, fmtOnly)
	}
	if fields[0].Path != "spec.template.spec.containers[name=api].image" || fields[0].Before != "catalog-api:5.2.0" || fields[0].After != "catalog-api:5.3.0" {
		t.Errorf("%+v", fields[0])
	}
	if fields[1].Path != "spec.template.spec.containers[name=api].resources.limits.memory" || fields[1].After != "1Gi" {
		t.Errorf("%+v", fields[1])
	}
	if fields[0].Doc != "apps/v1/Deployment api" {
		t.Errorf("doc key %q", fields[0].Doc)
	}
	// the canonical texts differ only where the fields do
	var changed int
	la, lb := strings.Split(na, "\n"), strings.Split(nb, "\n")
	if len(la) != len(lb) {
		t.Fatalf("normalized line counts differ: %d vs %d\n%s\n---\n%s", len(la), len(lb), na, nb)
	}
	for i := range la {
		if la[i] != lb[i] {
			changed++
		}
	}
	if changed != 2 {
		t.Errorf("%d normalized lines differ:\n%s\n---\n%s", changed, na, nb)
	}
	// quoting and a comment alone are formatting only
	_, _, _, fmtOnly = Semantic(before, strings.ReplaceAll(before, "image: envoy:1", "image: \"envoy:1\" # sidecar"))
	if !fmtOnly {
		t.Error("quoting change not recognised as formatting only")
	}
	// a second document added shows as additions of its fields
	fields, _, _, _ = Semantic(before, before+"---\napiVersion: v1\nkind: Service\nmetadata:\n  name: api\nspec:\n  port: 80\n")
	if len(fields) == 0 || fields[len(fields)-1].Doc != "v1/Service api" || fields[len(fields)-1].Before != "" {
		t.Errorf("added doc: %+v", fields)
	}
	// non-YAML falls back to the text
	if _, a, b, _ := Semantic("{not: [yaml", "x"); a != "{not: [yaml" || b != "x" {
		t.Error("fallback lost the text")
	}
}

func TestPreviewAndPromoteStage(t *testing.T) {
	c := ChapterOne()
	r := order(t, c, "co-ca")
	// bases: the three class bases; api changes, config does not; test keeps 2Gi
	p, err := PreviewStage(context.Background(), c, r, 1)
	if err != nil {
		t.Fatal(err)
	}
	if p.Stage != "bases" || len(p.Spaces) != 3 {
		t.Fatalf("%+v", p)
	}
	byName := map[string]SpacePreview{}
	for _, sp := range p.Spaces {
		byName[sp.Space.Slug] = sp
	}
	dev := byName["catalog-api-dev"]
	if len(dev.Units) != 2 || dev.Units[0].Slug != "api" || dev.Units[0].NoChange || !dev.Units[1].NoChange {
		t.Fatalf("dev preview: %+v", dev.Units)
	}
	if len(dev.Units[0].Fields) != 2 || dev.Units[0].Fields[1].Path != "memory" || dev.Units[0].Fields[1].After != "1Gi" {
		t.Errorf("dev api fields: %+v", dev.Units[0].Fields)
	}
	test := byName["catalog-api-test"]
	if len(test.Units[0].Fields) != 1 || test.Units[0].Fields[0].Path != "image" {
		t.Errorf("test api fields (2Gi should be kept): %+v", test.Units[0].Fields)
	}
	// the upstream raised memory to 1Gi; test keeps 2Gi, and a protection says why
	if k := test.Units[0].Kept; len(k) != 1 || k[0].Path != "memory" || k[0].Current != "2Gi" || k[0].Upstream != "1Gi" || !k[0].Protected {
		t.Errorf("test api kept: %+v", k)
	}
	if len(dev.Units[0].Kept) != 0 {
		t.Errorf("dev api kept: %+v", dev.Units[0].Kept)
	}
	if q := c.Patches[0]; q.Get("include") != "ConfigData,MutationSources" {
		t.Errorf("dry run include: %s", q.Get("include"))
	}
	if got := MutationPath("spec.template.spec.containers[name=api].resources.limits.memory"); got != "spec.template.spec.containers.?name=api.resources.limits.memory" {
		t.Errorf("MutationPath: %s", got)
	}
	if got := MutationPath("spec.ports[0].port"); got != "spec.ports.0.port" {
		t.Errorf("MutationPath: %s", got)
	}
	if len(p.Blockers()) != 0 {
		t.Errorf("blockers: %v", p.Blockers())
	}
	units, fields := p.Changed()
	if units != 3 || fields != 4 {
		t.Errorf("changed %d units %d fields", units, fields)
	}
	// the dry run is the CLI's request
	q := c.Patches[0]
	if q.Get("upgrade") != "true" || q.Get("dry_run") != "true" || q.Get("change_order") != "co-ca" || !strings.Contains(q.Get("where"), "UpstreamUnitID IS NOT NULL") {
		t.Errorf("dry run query: %v", q)
	}
	// dev stage: dev1 lacks config → a blocker names it
	p, err = PreviewStage(context.Background(), c, r, 2)
	if err != nil {
		t.Fatal(err)
	}
	if b := p.Blockers(); len(b) != 1 || !strings.Contains(b[0], "catalog-api-us-east-dev1 lacks config") {
		t.Errorf("blockers: %v", b)
	}
	// promote bases: three PATCHes without dry_run, base skipped
	c.Patches = nil
	out, err := PromoteStage(context.Background(), c, r, 1)
	if err != nil || len(out) != 3 {
		t.Fatalf("%v %+v", err, out)
	}
	for _, o := range out {
		if o.Err != "" || o.Units != 2 || o.Changed != 2 || len(o.Errors) != 0 {
			t.Errorf("%+v", o)
		}
	}
	if len(c.Patches) != 3 || c.Patches[0].Get("dry_run") != "" || c.Patches[0].Get("change_order") != "co-ca" {
		t.Errorf("promote patches: %v", c.Patches)
	}
	if got := PromoteCommands(r, 1); len(got) != 1 || got[0] != "cub variant promote --change-order catalog-api-base/catalog-api-5-3-0 --target-stage bases" {
		t.Errorf("%v", got)
	}
}

func TestKeptAgainstTheOrderedChange(t *testing.T) {
	c := ChapterOne()
	r := order(t, c, "co-ca")
	// a test cluster two hops below the base: its class base already kept
	// 2Gi, so against the class base nothing is kept; against the ordered
	// change the memory limit is, and the dry run's protection says why
	p, err := PreviewStage(context.Background(), c, r, 3)
	if err != nil {
		t.Fatal(err)
	}
	var test1 *SpacePreview
	for i := range p.Spaces {
		if p.Spaces[i].Space.Slug == "catalog-api-us-east-test1" {
			test1 = &p.Spaces[i]
		}
	}
	if test1 == nil || len(test1.Units) == 0 {
		t.Fatalf("%+v", p)
	}
	api := test1.Units[0]
	if api.Slug != "api" || len(api.Fields) != 1 || len(api.Kept) != 1 || api.Kept[0].Path != "memory" || api.Kept[0].Current != "2Gi" || api.Kept[0].Upstream != "1Gi" || !api.Kept[0].Protected {
		t.Errorf("test1 api: fields %+v kept %+v", api.Fields, api.Kept)
	}
	// the promoted class base's own change reads the same way: image taken, memory kept
	ch, err := Change(context.Background(), c, r.Order, "ca-test")
	if err != nil {
		t.Fatal(err)
	}
	ch = WithKept(context.Background(), c, r, "ca-test", ch)
	if len(ch) != 2 || ch[0].Slug != "api" || len(ch[0].Kept) != 1 || ch[0].Kept[0].Current != "2Gi" || !ch[0].Kept[0].Protected {
		t.Errorf("class base change: %+v", ch)
	}
	if len(ch[1].Kept) != 0 {
		t.Errorf("config kept: %+v", ch[1].Kept)
	}
}
