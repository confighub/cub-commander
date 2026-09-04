package exec

import (
	"strings"
	"testing"

	"github.com/confighub/cub-commander/internal/cubclient"
	"github.com/confighub/cub-commander/internal/lang"
	"github.com/confighub/cub-commander/internal/plan"
)

func rows() []cubclient.Row {
	mk := func(slug, space, env string, head, rel float64, labels map[string]any) cubclient.Row {
		return cubclient.Row{
			"Unit":  map[string]any{"Slug": slug, "HeadRevisionNum": head, "LastReleasedRevisionNum": rel, "Labels": labels, "ApprovedBy": []any{}},
			"Space": map[string]any{"Slug": space, "Labels": map[string]any{"Environment": env}},
		}
	}
	return []cubclient.Row{
		mk("backend", "prod-eu", "prod", 17, 15, map[string]any{"Tier": "Backend"}),
		mk("frontend", "prod-eu", "prod", 3, 3, map[string]any{"Tier": "Frontend"}),
		mk("backend", "dev-1", "dev", 9, 9, map[string]any{"Tier": "Backend"}),
	}
}

func run(t *testing.T, stmt string) *Result {
	t.Helper()
	st, err := lang.ParseOne(stmt)
	if err != nil {
		t.Fatal(err)
	}
	p, err := plan.Compile(st.(*lang.SelectStmt), plan.Session{})
	if err != nil {
		t.Fatal(err)
	}
	res, err := Local(p, rows())
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func TestHavingOrderLimit(t *testing.T) {
	res := run(t, "SELECT Slug, Space.Slug, Space.Labels.Environment AS env FROM Unit HAVING (env = 'prod' OR Labels.Tier = 'Backend') AND NOT Slug LIKE 'front%' ORDER BY Space.Slug LIMIT 5")
	if len(res.Rows) != 2 || res.Rows[0][1] != "dev-1" || res.Rows[1][1] != "prod-eu" {
		t.Errorf("rows: %v", res.Rows)
	}
	if res.Headers[0] != "NAME" || res.Headers[1] != "SPACE" || res.Headers[2] != "env" {
		t.Errorf("headers: %v", res.Headers)
	}
	res = run(t, "SELECT Slug FROM Unit HAVING HeadRevisionNum > LastReleasedRevisionNum")
	if len(res.Rows) != 1 || res.Rows[0][0] != "backend" {
		t.Errorf("attribute comparison: %v", res.Rows)
	}
	res = run(t, "SELECT Slug FROM Unit HAVING LEN(ApprovedBy) = 0 AND Slug IN ('backend') AND Slug ~ '^back'")
	if len(res.Rows) != 2 {
		t.Errorf("len/in/regex: %v", res.Rows)
	}
}

func TestLabelExpansion(t *testing.T) {
	res := run(t, "Unit | columns Slug, Labels.*, Space.Labels.*")
	if got := strings.Join(res.Headers, " "); got != "NAME Tier Space·Environment" {
		t.Errorf("headers: %q", got)
	}
	if res.Rows[0][1] != "Backend" || res.Rows[0][2] != "prod" {
		t.Errorf("rows: %v", res.Rows[0])
	}
	if r, ok := res.Columns[1].Expr.(lang.Ref); !ok || r.Path != "Labels.Tier" {
		t.Errorf("expanded column expr: %+v", res.Columns[1])
	}
	// Default columns expand too, and a constant key is hidden.
	res = run(t, "Unit")
	if !strings.Contains(strings.Join(res.Headers, " "), "Tier") {
		t.Errorf("default headers: %v", res.Headers)
	}
}

func TestGroupCount(t *testing.T) {
	res := run(t, "SELECT Space.Labels.Environment AS env, COUNT(*) AS n, COUNT(DISTINCT Slug) AS d FROM Unit GROUP BY env ORDER BY n DESC")
	if len(res.Rows) != 2 || res.Rows[0][0] != "prod" || Format(res.Rows[0][1]) != "2" || Format(res.Rows[0][2]) != "2" || Format(res.Rows[1][1]) != "1" {
		t.Errorf("group: %v", res.Rows)
	}
}
