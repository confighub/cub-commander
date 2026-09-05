package plan

import (
	"fmt"
	"strings"
	"testing"

	"github.com/confighub/cub-commander/internal/lang"
)

// Golden: statement → cub command + API request. This is the honesty contract.
func TestGolden(t *testing.T) {
	cases := []struct {
		stmt, space, cub, api string
	}{
		{
			"SELECT Slug, Space.Slug FROM Unit IN * WHERE Labels.Environment = 'prod'", "",
			`cub unit list --space '*' --where "Labels.Environment = 'prod'" --columns Unit.Slug,Space.Slug`,
			`GET /api/unit?include=BridgeWorkerID%2CChangeSetID%2CFromLinkID%2CSpaceID%2CTargetID%2CUnitEventID%2CUpstreamUnitID&select=BridgeWorker.Slug%2CBridgeWorkerID%2CChangeSet.Slug%2CChangeSetID%2CFromLink.Slug%2CFromLinkID%2CSlug%2CSpace.Slug%2CSpaceID%2CTarget.Slug%2CTargetID%2CUnitID%2CUpstreamUnit.Slug%2CUpstreamUnitID&where=Labels.Environment+%3D+%27prod%27`,
		},
		{
			"SELECT Slug, Target.Slug, HeadRevisionNum FROM Unit", "prod-eu",
			`cub unit list --space prod-eu --columns Unit.Slug,Target.Slug,Unit.HeadRevisionNum`,
			`GET /api/space/{prod-eu}/unit?include=BridgeWorkerID%2CChangeSetID%2CFromLinkID%2CSpaceID%2CTargetID%2CUnitEventID%2CUpstreamUnitID&select=BridgeWorker.Slug%2CBridgeWorkerID%2CChangeSet.Slug%2CChangeSetID%2CFromLink.Slug%2CFromLinkID%2CHeadRevisionNum%2CSlug%2CSpace.Slug%2CSpaceID%2CTarget.Slug%2CTargetID%2CUnitID%2CUpstreamUnit.Slug%2CUpstreamUnitID`,
		},
		{
			"SELECT Slug, Labels.Environment FROM Space WHERE Labels.Component = 'checkout'", "prod-eu",
			`cub space list --where "Labels.Component = 'checkout'" --columns Space.Slug,Space.Labels.Environment`,
			`GET /api/space?select=Labels%2CSlug%2CSpaceID&where=Labels.Component+%3D+%27checkout%27`,
		},
		{
			"SELECT Slug FROM Unit IN * WHERE UpstreamRevisionNum > 0 AND UpstreamRevisionNum < UpstreamUnit.HeadRevisionNum", "",
			`cub unit list --space '*' --where "UpstreamRevisionNum > 0 AND UpstreamRevisionNum < UpstreamUnit.HeadRevisionNum" --columns Unit.Slug`,
			`GET /api/unit?include=BridgeWorkerID%2CChangeSetID%2CFromLinkID%2CSpaceID%2CTargetID%2CUnitEventID%2CUpstreamUnitID&select=BridgeWorker.Slug%2CBridgeWorkerID%2CChangeSet.Slug%2CChangeSetID%2CFromLink.Slug%2CFromLinkID%2CSlug%2CSpace.Slug%2CSpaceID%2CTarget.Slug%2CTargetID%2CUnitID%2CUpstreamUnit.Slug%2CUpstreamUnitID&where=UpstreamRevisionNum+%3E+0+AND+UpstreamRevisionNum+%3C+UpstreamUnit.HeadRevisionNum`,
		},
		{
			"SELECT Slug, Space.Slug, FromUnit.Slug FROM Link IN *", "",
			`cub link list --space '*' --columns Link.Slug,Space.Slug,FromUnit.Slug`,
			`GET /api/link?include=FromUnitID%2CSpaceID%2CToSpaceID%2CToUnitID&select=FromUnit.Slug%2CFromUnitID%2CLinkID%2CSlug%2CSpace.Slug%2CSpaceID%2CToSpace.Slug%2CToSpaceID%2CToUnit.Slug%2CToUnitID`,
		},
	}
	for _, c := range cases {
		st, err := lang.ParseOne(c.stmt)
		if err != nil {
			t.Errorf("%s: %v", c.stmt, err)
			continue
		}
		p, err := Compile(st.(*lang.SelectStmt), Session{Space: c.space})
		if err != nil {
			t.Errorf("%s: %v", c.stmt, err)
			continue
		}
		if got := p.CubCommand(); got != c.cub {
			t.Errorf("%s\n cub want %s\n     got  %s", c.stmt, c.cub, got)
		}
		if got := p.APIPath(""); got != c.api {
			t.Errorf("%s\n api want %s\n     got  %s", c.stmt, c.api, got)
		}
	}
}

func TestBareJoinNameIsAttribute(t *testing.T) {
	p, err := Compile(mustSel("Unit | columns Slug, ApprovedBy, FromLinkID"), Session{})
	if err != nil {
		t.Fatal(err)
	}
	for _, inc := range p.List.Include {
		if inc == "ApprovedByID" {
			t.Errorf("ApprovedBy must select, not include: %v", p.List.Include)
		}
	}
	sel := fmt.Sprint(p.List.Select)
	for _, want := range []string{"ApprovedBy ", "FromLinkID", " Slug ", "UnitID", "Space.Slug"} {
		if !strings.Contains(sel+" ", want) {
			t.Errorf("select missing %q: %v", want, p.List.Select)
		}
	}
}

func TestLocalStages(t *testing.T) {
	st, _ := lang.ParseOne("SELECT Space.Labels.Environment AS env, COUNT(*) AS n FROM Unit IN * HAVING env = 'prod' OR env = 'staging' GROUP BY env ORDER BY n DESC LIMIT 5")
	p, err := Compile(st.(*lang.SelectStmt), Session{})
	if err != nil {
		t.Fatal(err)
	}
	kinds := []string{}
	for _, l := range p.Local {
		kinds = append(kinds, l.Kind)
	}
	if want := "where group order limit"; join(kinds) != want {
		t.Errorf("stages: %v", kinds)
	}
	if p.Local[0].Reason == "" || p.List.Include[0] == "" {
		t.Errorf("explain reasons or include missing: %+v", p)
	}
	if _, err := Compile(mustSel("SELECT get-replicas() FROM Unit"), Session{}); err == nil {
		t.Error("function columns should be rejected in M1")
	}
}

// The pipeline rule: the leading run of pushable where steps is the server
// stage; from the first step that cannot be pushed, everything is local.
func TestPushdownRun(t *testing.T) {
	p, err := Compile(mustSel("Unit | in * | where Labels.a = 'x' | where Slug LIKE 'a%' OR Slug LIKE 'b%' | where Labels.b = 'y' | columns Slug, get-replicas() as r"), Session{})
	if err == nil {
		t.Fatal("function columns should still be rejected in M1")
	}
	p, err = Compile(mustSel("Unit | in * | where Labels.a = 'x' | where Slug LIKE 'a%' OR Slug LIKE 'b%' | where Labels.b = 'y' | columns Slug as name | where name != 'z'"), Session{})
	if err != nil {
		t.Fatal(err)
	}
	if p.List.Where != "Labels.a = 'x'" {
		t.Errorf("server where: %q", p.List.Where)
	}
	if want := []bool{true, false, false, false}; fmt.Sprint(p.Pushed) != fmt.Sprint(want) {
		t.Errorf("pushed: %v", p.Pushed)
	}
	if len(p.Local) != 3 || p.Local[0].Reason == "" || p.Local[1].Reason != "follows a local step" || !strings.Contains(p.Local[2].Reason, "alias") {
		t.Errorf("local stages: %+v", p.Local)
	}
	if !strings.Contains(p.Explain(""), "OR is not in the server where grammar") {
		t.Errorf("explain: %s", p.Explain(""))
	}
}

func join(s []string) string {
	out := ""
	for i, x := range s {
		if i > 0 {
			out += " "
		}
		out += x
	}
	return out
}

func mustSel(s string) *lang.SelectStmt {
	st, err := lang.ParseOne(s)
	if err != nil {
		panic(err)
	}
	return st.(*lang.SelectStmt)
}

// A selector above the unit level is lifted to the units inside it.
func TestDiffLiftsToUnit(t *testing.T) {
	p, err := Compile(mustSel("Space | in * | where Labels.Component = 'cart' | diff Labels.Variant = 'dev' vs Labels.Variant = 'prod' by Slug"), Session{})
	if err != nil {
		t.Fatal(err)
	}
	if p.Entity.Name != "Unit" || p.Diff == nil {
		t.Fatalf("plan: %+v", p)
	}
	if p.Diff.A.Where != "Space.Labels.Component = 'cart' AND Space.Labels.Variant = 'dev'" || p.Diff.By[0].Path != "Space.Slug" {
		t.Errorf("lifted: %q by %v", p.Diff.A.Where, p.Diff.By)
	}
	p, err = Compile(mustSel("Target | in * | diff Labels.Cluster = 'a' vs Labels.Cluster = 'b'"), Session{})
	if err != nil || p.Diff.B.Where != "Target.Labels.Cluster = 'b'" {
		t.Errorf("target: %v %+v", err, p.Diff)
	}
	p, err = Compile(mustSel("Resource | in * | where Space.Labels.Component = 'cart' | diff Unit.Labels.Environment = 'dev' vs Unit.Labels.Environment = 'prod'"), Session{})
	if err != nil || p.Diff.A.Where != "Space.Labels.Component = 'cart' AND Labels.Environment = 'dev'" {
		t.Errorf("resource: %v %+v", err, p.Diff)
	}
	if _, err := Compile(mustSel("Resource | diff ResourceType = 'a' vs ResourceType = 'b'"), Session{}); err == nil {
		t.Errorf("resource-own attribute should be rejected")
	}
}

func TestRolloutPlan(t *testing.T) {
	st, err := lang.ParseOne("ChangeOrder | in * | where State IN ('New', 'InProgress') | columns Slug, state(), blocker()")
	if err != nil {
		t.Fatal(err)
	}
	p, err := Compile(st.(*lang.SelectStmt), Session{})
	if err != nil {
		t.Fatal(err)
	}
	if !p.RolloutCols || len(p.Local) != 1 || p.Local[0].Kind != "rollout" {
		t.Errorf("rollout columns not planned: %+v", p.Local)
	}
	sel := strings.Join(p.List.Select, ",")
	for _, f := range []string{"Annotations", "ResolvedSpaceIDs", "ReleasedSpaceIDs", "StartTagID", "EndTagID", "InScopeSpaceIDs", "Space.Slug"} {
		if !strings.Contains(sel, f) {
			t.Errorf("select lacks %s: %s", f, sel)
		}
	}
	if !strings.Contains(p.Explain(""), "rollout state(), stage(), next(), blocker()") {
		t.Errorf("explain: %s", p.Explain(""))
	}
	st, _ = lang.ParseOne("ChangeOrder | in * | where ChangeOrderID = 'x' | rollout")
	p, err = Compile(st.(*lang.SelectStmt), Session{})
	if err != nil || p.Rollout == nil {
		t.Fatalf("rollout step: %v", err)
	}
	if !strings.Contains(p.Explain(""), "cub variant promote") && !strings.Contains(p.Explain(""), "Tags ?") {
		t.Errorf("explain: %s", p.Explain(""))
	}
	if _, err := Compile(mustSel("Unit | in * | columns Slug, state()"), Session{}); err == nil {
		t.Error("state() accepted on Unit")
	}
	if _, err := Compile(mustSel("Unit | in * | rollout"), Session{}); err == nil {
		t.Error("rollout accepted on Unit")
	}
}
