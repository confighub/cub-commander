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
	workflowSlugs.Delete("wf-cm") // another test may have cached it
	r := order(t, c, "co-cm")
	if r.Err != "" {
		t.Fatal(r.Err)
	}
	if r.Component != "cert-manager" || r.WorkflowRef != "cert-manager-workflow" {
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
	// the server evaluated every (prerequisite, space) pair of prod's gates
	// over test: Promoted, Released, Healthy × test1, test2
	ok, total := Tally(r.Gates)
	if ok != 5 || total != 6 {
		t.Errorf("gates %d of %d: %+v", ok, total, r.Gates)
	}
	var failing *Gate
	for i := range r.Gates {
		if !r.Gates[i].OK {
			failing = &r.Gates[i]
		}
	}
	if failing == nil || failing.Name != PrereqHealthy || failing.Space != "cert-manager-us-east-test1" || failing.Reason != "Variant 'us-east-test1' is not healthy" {
		t.Errorf("healthy gate: %+v", failing)
	}
	if r.State != StateDegraded || r.Blocker != "Variant 'us-east-test1' is not healthy" {
		t.Errorf("state %q blocker %q", r.State, r.Blocker)
	}
	if r.Completed || r.Plan == nil || !r.Plan.Refused {
		t.Errorf("completed %v plan %+v", r.Completed, r.Plan)
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
	if sp := r.Stages[3].Spaces[0]; !sp.Health.Present || !sp.Health.ForOrder || sp.Health.Status != "Degraded" || sp.Health.ReleaseNum != 3 {
		t.Errorf("test1 health: %+v", sp.Health)
	}
	if len(r.Order.Promotions) != 3 || r.Order.Promotions[2].Stage != "test" {
		t.Errorf("promotions: %+v", r.Order.Promotions)
	}
	// stage membership is the selector intersected with the order's scope,
	// and the workflow's name is read once
	sawScope, wfReads := false, 0
	for _, l := range c.Log {
		if strings.Contains(l, "Labels.Stage+%3D+%27prod%27+AND+SpaceID+IN+") {
			sawScope = true
		}
		if strings.HasPrefix(l, "/change_workflow?") {
			wfReads++
		}
	}
	if !sawScope || wfReads != 1 {
		t.Errorf("scope %v workflow reads %d: %v", sawScope, wfReads, c.Log)
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
	if ok, total := Tally(r.Gates); ok != 0 || total != 0 {
		t.Errorf("the first stage has no gates: %d of %d", ok, total)
	}
	if !strings.Contains(strings.Join(r.CubCommands(), "\n"), "cub variant promote --change-order catalog-api-base/catalog-api-5-3-0 --target-stage bases --dry-run") {
		t.Errorf("cub commands: %v", r.CubCommands())
	}
}

func TestNoWorkflow(t *testing.T) {
	r := order(t, ChapterOne(), "co-old")
	if r.State != StateNoWorkflow || len(r.Stages) != 1 || r.NextName() != "" || r.Workflow != nil {
		t.Errorf("%+v", r)
	}
}

// setOrder edits a change order row in the fixture, the way the server
// would hold it after a write elsewhere.
func setOrder(c *MemClient, id string, edit func(co map[string]any)) {
	for _, row := range c.Rows["/change_order"] {
		if co := own(row, "ChangeOrder"); str(co["ChangeOrderID"]) == id {
			edit(co)
		}
	}
}

func TestReleaseGateFromServer(t *testing.T) {
	c := ChapterOne()
	// Take the release back from test2: the gate ahead of prod is now Released.
	setOrder(c, "co-cm", func(co map[string]any) { co["ReleasedSpaceIDs"] = []any{"cm-dev1", "cm-test1"} })
	r := order(t, c, "co-cm")
	if r.State != StateBlocked || !strings.Contains(r.Blocker, "has taken change order 'cert-manager-1-17-0' but has not released it") {
		t.Errorf("state %q blocker %q", r.State, r.Blocker)
	}
}

func TestCompletionIsReadFromTheBits(t *testing.T) {
	c := ChapterOne()
	all := []any{"cm-base", "cm-dev", "cm-test", "cm-prod", "cm-dev1", "cm-test1", "cm-test2", "cm-prod1", "cm-prod2", "cm-prod3"}
	setOrder(c, "co-cm", func(co map[string]any) {
		co["ResolvedSpaceIDs"] = all
		co["ReleasedSpaceIDs"] = []any{"cm-dev1", "cm-test1", "cm-test2", "cm-prod1", "cm-prod2", "cm-prod3"}
	})
	for _, row := range c.Rows["/release"] {
		rel := own(row, "Release")
		if strings.HasPrefix(str(rel["SpaceID"]), "cm-prod") {
			rel["ChangeOrderID"] = "co-cm"
		}
	}
	r := order(t, c, "co-cm")
	if r.Next != -1 || r.Plan == nil || !r.Plan.Complete {
		t.Fatalf("next %d plan %+v", r.Next, r.Plan)
	}
	// the server has not recorded completion (it does so on a write), but
	// every final gate this reading can make holds
	if !r.Completed || r.State != StateComplete {
		t.Errorf("completed %v state %q gates %+v", r.Completed, r.State, r.Gates)
	}
	// prod1 unhealthy again: final fails on Healthy
	for _, row := range c.Rows["/release"] {
		rel := own(row, "Release")
		if str(rel["SpaceID"]) == "cm-prod1" {
			rel["LiveStatus"].(map[string]any)["Health"] = "Progressing"
		}
	}
	r = order(t, c, "co-cm")
	if r.Completed || r.State != StateDegraded || !strings.Contains(r.Blocker, "us-east-prod1") {
		t.Errorf("completed %v state %q blocker %q", r.Completed, r.State, r.Blocker)
	}
	// unless the server says Completed
	setOrder(c, "co-cm", func(co map[string]any) { co["Stage"] = StageCompleted })
	if r = order(t, c, "co-cm"); !r.Completed || r.Reached() != StageCompleted {
		t.Errorf("server completion not honoured: %v %q", r.Completed, r.Reached())
	}
}

func TestPreviewKeptAndClones(t *testing.T) {
	c := ChapterOne()
	r := order(t, c, "co-ca")
	p, err := PreviewStage(context.Background(), c, r, 1)
	if err != nil {
		t.Fatal(err)
	}
	if p.Plan != "plan-catalog-api-5-3-0-bases" || p.Refused || len(p.Blockers()) != 0 || len(p.Spaces) != 3 {
		t.Fatalf("%+v blockers %v", p, p.Blockers())
	}
	byUnit := func(sp SpacePreview, slug string) UnitPreview {
		for _, u := range sp.Units {
			if u.Slug == slug {
				return u
			}
		}
		t.Fatalf("%s has no unit %s: %+v", sp.Space.Slug, slug, sp.Units)
		return UnitPreview{}
	}
	bySlug := func(p *Preview, slug string) SpacePreview {
		for _, sp := range p.Spaces {
			if sp.Space.Slug == slug {
				return sp
			}
		}
		t.Fatalf("no space %s in %+v", slug, p.Spaces)
		return SpacePreview{}
	}
	dev := bySlug(p, "catalog-api-dev")
	if dev.Action != "Promote" {
		t.Fatalf("%+v", dev)
	}
	if api := byUnit(dev, "api"); api.Action != "Upgrade" || len(api.Fields) != 2 || api.NoChange || len(api.Kept) != 0 {
		t.Errorf("dev api: %+v", api)
	}
	if cfg := byUnit(dev, "config"); !cfg.NoChange || cfg.Action != "Mark" {
		t.Errorf("dev config: %+v", cfg)
	}
	test := bySlug(p, "catalog-api-test")
	api := byUnit(test, "api")
	if len(api.Fields) != 1 || api.Fields[0].Path != "image" || len(api.Kept) != 1 {
		t.Fatalf("test api: %+v", api)
	}
	if k := api.Kept[0]; k.Path != "memory" || k.Current != "2Gi" || k.Upstream != "1Gi" || !k.Protected || k.Why != "Protected" {
		t.Errorf("kept: %+v", k)
	}
	if units, fields := p.Changed(); units != 3 || fields != 4 {
		t.Errorf("changed %d units %d fields", units, fields)
	}
	// a stage past the next is gated on the next having the change: refused, nothing planned
	p, err = PreviewStage(context.Background(), c, r, 2)
	if err != nil || !p.Refused || len(p.Spaces) != 0 || len(p.Blockers()) == 0 {
		t.Fatalf("preview of dev before bases: %v %+v", err, p)
	}
	// once bases has landed, dev: the server clones the unit dev1 lacks
	if _, _, err := PromoteStage(context.Background(), c, r, 1, ""); err != nil {
		t.Fatal(err)
	}
	r = order(t, c, "co-ca")
	p, err = PreviewStage(context.Background(), c, r, 2)
	if err != nil || p.Refused {
		t.Fatalf("%v %+v", err, p)
	}
	dev1 := bySlug(p, "catalog-api-us-east-dev1")
	if cfg := byUnit(dev1, "config"); !cfg.New || cfg.Action != "Clone" {
		t.Errorf("dev1 config: %+v", cfg)
	}
}

func TestPromoteAppliesWithThePlan(t *testing.T) {
	c := ChapterOne()
	r := order(t, c, "co-ca")
	if _, _, err := PromoteStage(context.Background(), c, r, 1, "plan-stale"); err == nil || !strings.Contains(err.Error(), "412") {
		t.Errorf("stale plan: %v", err)
	}
	out, res, err := PromoteStage(context.Background(), c, r, 1, "plan-catalog-api-5-3-0-bases")
	if err != nil || res == nil || len(out) != 3 {
		t.Fatalf("%v %+v", err, out)
	}
	for _, o := range out {
		if o.Action != "Promote" || o.Changed != 1 || o.Marked != 1 || len(o.Errors) != 0 {
			t.Errorf("%+v", o)
		}
	}
	if len(c.Promotes) != 1 || !strings.HasPrefix(c.Promotes[0], "catalog-api-5-3-0 → bases") {
		t.Errorf("promotes: %v", c.Promotes)
	}
	if got := PromoteCommands(r, 1, res.Plan); len(got) != 1 || got[0] != "cub variant promote --change-order catalog-api-base/catalog-api-5-3-0 --target-stage bases --expected-plan plan-catalog-api-5-3-0-bases" {
		t.Errorf("%v", got)
	}
	// re-read: bases taken, dev next
	r = order(t, c, "co-ca")
	if taken, _, _ := r.Stages[1].Counts(); taken != 3 || r.NextName() != "dev" || r.Reached() != "bases" {
		t.Errorf("after promote: taken %d next %q reached %q", taken, r.NextName(), r.Reached())
	}
	// B's release reads the promote's outcomes as taken before the re-read
	after := AfterPromote(order(t, ChapterOne(), "co-ca"), out)
	if taken, _, _ := after.Stages[1].Counts(); taken != 3 {
		t.Errorf("AfterPromote taken %d", taken)
	}
}

func TestPromoteRefusedByTheServer(t *testing.T) {
	c := ChapterOne()
	r := order(t, c, "co-cm")
	out, res, err := PromoteStage(context.Background(), c, r, 4, "")
	if err == nil || !strings.Contains(err.Error(), "promote refused by the gates: Variant 'us-east-test1' is not healthy") || out != nil || res == nil || !res.Refused {
		t.Errorf("%v %+v %+v", err, out, res)
	}
	if len(c.Promotes) != 0 {
		t.Errorf("a refusal wrote: %v", c.Promotes)
	}
}

func TestChangeInSpaces(t *testing.T) {
	c := ChapterOne()
	r := order(t, c, "co-cm")
	base, err := ChangeIn(context.Background(), c, r, "cm-base")
	if err != nil || len(base) != 3 {
		t.Fatalf("%v %+v", err, base)
	}
	if u := base[0]; u.Slug != "controller" || !u.Touched || u.StartRev != 2 || u.EndRev != 3 || len(u.Fields) != 1 || u.Fields[0].Before != "quay.io/jetstack/cert-manager-controller:v1.16.0" {
		t.Errorf("%+v", u)
	}
	if base[1].Touched || base[2].Touched {
		t.Errorf("marked units read as touched: %+v", base[1:])
	}
	dev1, err := ChangeIn(context.Background(), c, r, "cm-dev1")
	if err != nil || len(dev1) != 2 || dev1[0].Slug != "controller" || len(dev1[0].Kept) != 0 {
		t.Fatalf("%v %+v", err, dev1)
	}
	// the ordered change is read once however many spaces ask
	n := 0
	for _, l := range c.Log {
		if strings.HasPrefix(l, "/unit_diff?") && strings.Contains(l, "cm-base") {
			n++
		}
	}
	if n != 1 {
		t.Errorf("ordered change read %d times: %v", n, c.Log)
	}
	// the test class base of catalog-api kept its memory: the change there
	// is the image only, with the base's memory change marked kept
	r = order(t, c, "co-ca")
	test, err := ChangeIn(context.Background(), c, r, "ca-test")
	if err != nil || len(test) != 2 {
		t.Fatalf("%v %+v", err, test)
	}
	if api := test[0]; api.Slug != "api" || len(api.Fields) != 1 || len(api.Kept) != 1 || !api.Kept[0].Protected || api.Kept[0].Current != "2Gi" {
		t.Errorf("%+v", api)
	}
}

func TestText(t *testing.T) {
	r := order(t, ChapterOne(), "co-cm")
	txt := r.Text()
	for _, want := range []string{"cert-manager-1-17-0  Bump cert-manager to v1.17.0", "stage test · Degraded", "▲prod", "next: prod · gates 5 of 6 satisfied", "✗ Variant 'us-east-test1' is not healthy", "✓ Released · cert-manager-us-east-test2", "cub changeorder get cert-manager-1-17-0 --space cert-manager-base"} {
		if !strings.Contains(txt, want) {
			t.Errorf("text lacks %q:\n%s", want, txt)
		}
	}
}
