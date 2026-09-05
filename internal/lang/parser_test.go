package lang

import (
	"strings"
	"testing"
)

func TestServerWhereRoundTrip(t *testing.T) {
	cases := []string{
		"Labels.tier = 'Backend'",
		"UpstreamRevisionNum > 0 AND UpstreamRevisionNum < UpstreamUnit.HeadRevisionNum",
		"HeadRevisionNum > LastReleasedRevisionNum AND TargetID IS NOT NULL",
		"LEN(ApprovedBy) = 0",
		"ApprovedBy ? 'c9369257-0d7b-40d0-9127-454d90f5dcf8'",
		"Slug IN ('api', 'worker', 'db')",
		"Space.Labels.Environment IN ('production', 'staging')",
		"FromLink.*.Slug = 'upgrade-app'",
		"Slug LIKE 'test%'",
		"Slug ILIKE '%backend%'",
		"Slug ~ '^app-[0-9]+$'",
		"Slug !~~ 'temp%'",
		"Slug NOT IN ('a', 'b')",
		"Slug NOT LIKE 'x%'",
		"MergeSourceID = '7c61626f-ddbe-41af-93f6-b69f4ab6d308' IS NOT FALSE",
		"Values.Image/container-image LIKE '%:v1.2.%'",
		"ApplyGates.require-approval/vet-approvedby = true",
		"Labels.Environment = 'prod' AND Data.spec.replicas > 3",
		"Data.spec.containers.?name=nginx.image = 'nginx:1.21'",
		"Data.spec.|containers.*.image = 'nginx'",
		"Data.metadata.labels.app~1kubernetes~1io/name = 'checkout'",
		"CreatedAt > '2025-02-18T23:16:34'",
		"Labels.tier IS NULL",
	}
	for _, c := range cases {
		st, err := ParseOne("SELECT * FROM Unit WHERE " + c)
		if err != nil {
			t.Errorf("%s: %v", c, err)
			continue
		}
		got := ServerWhere(st.(*SelectStmt).Filters[0].Expr)
		if got != c {
			t.Errorf("round trip\n want %s\n got  %s", c, got)
		}
	}
}

func TestWhereRejectsWithHint(t *testing.T) {
	cases := map[string]string{
		"SELECT * FROM Unit WHERE Labels.a = 'x' OR Labels.b = 'y'": "HAVING",
		"SELECT * FROM Unit WHERE NOT Labels.a = 'x'":               "HAVING",
		"SELECT * FROM Unit WHERE get-replicas() > 2":               "HAVING",
	}
	for src, hint := range cases {
		_, err := ParseOne(src)
		if err == nil || !strings.Contains(err.Error(), hint) {
			t.Errorf("%s: want error mentioning %s, got %v", src, hint, err)
		}
	}
}

func TestParseShapes(t *testing.T) {
	st, err := ParseOne(`SELECT Slug, Space.Slug AS space, get-replicas() AS r, COUNT(*) AS n
	FROM Unit IN * WHERE Data.kind = 'Deployment'
	HAVING (r < 2 OR NOT clean) AND space != 'x'
	GROUP BY space ORDER BY UpdatedAt DESC, Slug LIMIT 10`)
	if err != nil {
		t.Fatal(err)
	}
	s := st.(*SelectStmt)
	if len(s.Columns) != 4 || s.Columns[1].Alias != "space" || s.Columns[2].Expr.(Call).Name != "get-replicas" {
		t.Errorf("columns: %+v", s.Columns)
	}
	if s.Scope == nil || !s.Scope.Org {
		t.Errorf("scope: %+v", s.Scope)
	}
	if len(s.Filters) != 2 || s.Filters[0].Local || !s.Filters[1].Local || ExprString(s.Filters[1].Expr) != "(r < 2 OR NOT clean) AND space != 'x'" {
		t.Errorf("filters: %+v", s.Filters)
	}
	if len(s.OrderBy) != 2 || !s.OrderBy[0].Desc || s.OrderBy[1].Desc || *s.Limit != 10 {
		t.Errorf("order/limit: %+v %v", s.OrderBy, *s.Limit)
	}
	// SQL prints as the canonical pipeline.
	want := "Unit | in *\n| where Data.kind = 'Deployment'\n| columns Slug, Space.Slug as space, get-replicas() as r, COUNT(*) as n\n| where (r < 2 OR NOT clean) AND space != 'x'\n| group by space\n| order by UpdatedAt desc, Slug\n| limit 10"
	if got := StmtString(s); got != want {
		t.Errorf("StmtString\n want %q\n got  %q", want, got)
	}
	// And the pipeline re-parses to the same thing.
	st2, err := ParseOne(want)
	if err != nil {
		t.Fatal(err)
	}
	if got := StmtString(st2.(*SelectStmt)); got != want {
		t.Errorf("round trip\n want %q\n got  %q", want, got)
	}
	// Pipes are optional; pipeline where steps keep order and are never rejected at parse time.
	p2, err := ParseOne("Unit in prod-eu where Slug = 'a' OR Slug = 'b' columns Slug where Slug LIKE 'a%' limit 5")
	if err != nil {
		t.Fatal(err)
	}
	s2 := p2.(*SelectStmt)
	if s2.Scope.Space != "prod-eu" || len(s2.Filters) != 2 || s2.ColumnsPos != 1 || len(s2.Columns) != 1 || *s2.Limit != 5 {
		t.Errorf("pipeline: %+v", s2)
	}
	// USE, EXPLAIN, SHOW, multiple statements.
	ss, err := Parse("USE prod-eu; Unit | where Slug = 'a'; EXPLAIN Space | limit 1; SHOW COLUMNS FROM Unit; SHOW VALUES OF Labels.Environment;")
	if err != nil {
		t.Fatal(err)
	}
	if len(ss) != 5 {
		t.Fatalf("want 5 statements, got %d", len(ss))
	}
	if ss[0].(*UseStmt).Space != "prod-eu" || !ss[1].(*SelectStmt).Star || ss[3].(*ShowStmt).Arg != "Unit" || ss[4].(*ShowStmt).Arg != "Labels.Environment" {
		t.Errorf("statements: %#v", ss)
	}
	// A data path keeps its glued pipe.
	st3, _ := ParseOne("Unit | where Data.spec.|containers.*.image = 'nginx'")
	if got := ServerWhere(st3.(*SelectStmt).Filters[0].Expr); got != "Data.spec.|containers.*.image = 'nginx'" {
		t.Errorf("split path: %q", got)
	}
}

func TestLexerErrors(t *testing.T) {
	if _, err := Lex(`Slug = 'a"b'`); err == nil {
		t.Error("want error on quote inside string")
	}
	if _, err := Lex(`Slug = 'a\b'`); err == nil {
		t.Error("want error on backslash inside string")
	}
}

func TestRolloutStepRoundTrip(t *testing.T) {
	src := "ChangeOrder | in * | where State IN ('New', 'InProgress', 'Resolved') | columns Slug, Space.Slug, state(), stage(), next(), blocker(), CreatedAt | order by CreatedAt desc"
	st, err := ParseOne(src)
	if err != nil {
		t.Fatal(err)
	}
	sel := st.(*SelectStmt)
	if len(sel.Columns) != 7 {
		t.Fatalf("columns: %d", len(sel.Columns))
	}
	if c, ok := sel.Columns[2].Expr.(Call); !ok || c.Name != "state" || len(c.Args) != 0 {
		t.Errorf("state() parsed as %#v", sel.Columns[2].Expr)
	}
	if got := StmtString(sel); !strings.Contains(got, "state(), stage(), next(), blocker()") {
		t.Errorf("printed: %s", got)
	}
	st, err = ParseOne("ChangeOrder | in * | where ChangeOrderID = 'x' | rollout stage test")
	if err != nil {
		t.Fatal(err)
	}
	sel = st.(*SelectStmt)
	if sel.Rollout == nil || sel.Rollout.Stage != "test" {
		t.Fatalf("rollout step: %+v", sel.Rollout)
	}
	again, err := ParseOne(StmtString(sel))
	if err != nil || again.(*SelectStmt).Rollout == nil || again.(*SelectStmt).Rollout.Stage != "test" {
		t.Errorf("round trip: %v %s", err, StmtString(sel))
	}
}
