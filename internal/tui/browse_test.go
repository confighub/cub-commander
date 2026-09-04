package tui

import (
	"context"
	"net/url"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/confighub/cub-commander/internal/catalog"
	"github.com/confighub/cub-commander/internal/cubclient"
	"github.com/confighub/cub-commander/internal/exec"
	"github.com/confighub/cub-commander/internal/lang"
	"github.com/confighub/cub-commander/internal/plan"
)

// fakeResources answers the org-wide resource query for the stub units.
func fakeResources(t *testing.T) Fetcher {
	return func(ctx context.Context, path string, q url.Values) ([]cubclient.Row, error) {
		if path != "/resource" || !strings.HasPrefix(q.Get("where"), "UnitID IN (") {
			t.Errorf("fetch path %s %v", path, q)
		}
		var rows []cubclient.Row
		for _, id := range []string{"u1", "u2"} {
			if strings.Contains(q.Get("where"), "'"+id+"'") {
				rows = append(rows, cubclient.Row{"Resource": map[string]any{"ResourceType": "apps/v1/Deployment", "ResourceName": "/" + id + "-api", "UnitID": id, "UnitSlug": id}})
			}
		}
		return rows, nil
	}
}

func TestChooserThenBrowse(t *testing.T) {
	var m tea.Model = New(planSession(), stubRunner(t), nil, nil)
	m, _ = m.Update(tea.WindowSizeMsg{Width: 140, Height: 40})
	v := m.View().Content
	if !strings.Contains(v, "Browse by") || !strings.Contains(v, "Component") {
		t.Fatalf("start screen is not the chooser:\n%s", v)
	}
	mm := m.(Model)
	mm.chooserOpen = false
	mm.focus = focusCmd
	m = typeText(mm, "Unit | in * | browse by Space.Labels.Environment, Labels.Tier")
	m = press(m, "enter")
	mm = m.(Model)
	if mm.mode != modeBrowse || mm.browse == nil {
		t.Fatalf("browse mode not entered: mode=%v status=%q", mm.mode, mm.status)
	}
	panes := mm.browse.panes()
	if len(panes) != 3 || len(panes[0]) != 2 || panes[0][0].label != "dev" || panes[0][1].n != 1 {
		t.Fatalf("panes: %+v", panes)
	}
	v = m.View().Content
	if !strings.Contains(v, "Space·Environment (2)") || !strings.Contains(v, "Tier") || !strings.Contains(v, "prod-eu/") || !strings.Contains(v, "Units (") {
		t.Errorf("browse view:\n%s", v)
	}
	// Move to prod (down), into the Tier pane (right): only prod units remain.
	m = press(m, "down", "right")
	mm = m.(Model)
	panes = mm.browse.panes()
	if len(panes[1]) != 1 || len(panes[2]) != 1 {
		t.Errorf("filtered panes: %d %d", len(panes[1]), len(panes[2]))
	}
	if sel := mm.browse.selections(panes); len(sel) != 2 || sel[0].Op != "=" {
		t.Errorf("selections: %+v", sel)
	}
	if !strings.Contains(m.View().Content, "[Space.Labels.Environment = 'prod']") {
		t.Errorf("pending chips missing")
	}
	// g commits: where steps appear, browse step is gone, grid shows.
	m = press(m, "g")
	mm = m.(Model)
	want := "Unit | in *\n| where Space.Labels.Environment = 'prod' AND Labels.Tier = 'Backend'"
	if !strings.HasPrefix(mm.cmd.Value(), want) || strings.Contains(mm.cmd.Value(), "browse") {
		t.Errorf("commit: %q", mm.cmd.Value())
	}
	if mm.mode != modeResults {
		t.Errorf("mode after commit: %v", mm.mode)
	}
	m = press(m, "b")
	if !m.(Model).chooserOpen {
		t.Errorf("chooser not reopened")
	}
	m = press(m, "esc")
	if m.(Model).chooserOpen {
		t.Errorf("chooser not closed")
	}
}

func TestResourcesPane(t *testing.T) {
	var m tea.Model = New(planSession(), stubRunner(t), nil, nil)
	m, _ = m.Update(tea.WindowSizeMsg{Width: 160, Height: 40})
	mm := m.(Model)
	mm.chooserOpen, mm.focus = false, focusCmd
	mm.fetcher = fakeResources(t)
	m = typeText(mm, "Unit | in * | browse by Space.Slug")
	m = press(m, "enter")
	mm = m.(Model)
	if mm.browse == nil || mm.browse.hop {
		t.Fatalf("browse: %+v", mm.browse)
	}
	// → from the Units pane opens the Resources pane, adds the hop to the
	// statement, and fetches the highlighted unit's resources.
	mm.browse.pane = 1
	var cmd tea.Cmd
	m, cmd = mm.Update(tea.KeyPressMsg{Code: tea.KeyRight})
	mm = m.(Model)
	if !mm.browse.hop || mm.browse.pane != mm.browse.hopPane() || !strings.HasSuffix(mm.cmd.Value(), ", Resource") {
		t.Fatalf("hop: hop=%v pane=%d cmd=%q", mm.browse.hop, mm.browse.pane, mm.cmd.Value())
	}
	if cmd == nil {
		t.Fatal("no fetch")
	}
	m, _ = m.Update(cmd())
	if v := m.View().Content; !strings.Contains(v, "Resources (1)") || !strings.Contains(v, "-api") {
		t.Errorf("single-unit pane:\n%s", v)
	}
	// ← onto Units keeps the pane; ← onto the axis pane shows the union of
	// every unit under the selection, labelled with the unit.
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyLeft})
	if !m.(Model).browse.hop {
		t.Errorf("hop dropped while on Units")
	}
	m, cmd = m.Update(tea.KeyPressMsg{Code: tea.KeyLeft})
	if cmd != nil {
		m, _ = m.Update(cmd())
	}
	mm = m.(Model)
	panes := mm.browse.panes()
	if hp := panes[mm.browse.hopPane()]; len(hp) != 2 || !strings.Contains(hp[0].label, "·u") {
		t.Errorf("union pane: %+v", hp)
	}
	if v := m.View().Content; !strings.Contains(v, "Resources (2)") {
		t.Errorf("union view:\n%s", v)
	}
	// Preview in resources mode does not add a second resources pane.
	mm.previewMode = previewResources
	if v := mm.View().Content; strings.Count(v, "Resources (") != 1 {
		t.Errorf("duplicate resources pane:\n%s", v)
	}
	// r toggles the pane off and drops the hop from the statement, and back on.
	m, _ = mm.Update(tea.KeyPressMsg{Code: 'r', Text: "r"})
	if mm = m.(Model); mm.browse.hop || strings.Contains(mm.cmd.Value(), "Resource") {
		t.Errorf("r off: hop=%v cmd=%q", mm.browse.hop, mm.cmd.Value())
	}
	m, _ = mm.Update(tea.KeyPressMsg{Code: 'r', Text: "r"})
	if mm = m.(Model); !mm.browse.hop || !strings.HasSuffix(mm.cmd.Value(), ", Resource") {
		t.Errorf("r on: hop=%v cmd=%q", mm.browse.hop, mm.cmd.Value())
	}
	_ = lang.Ref{}
}

func TestPresetsWithoutSample(t *testing.T) {
	m := New(plan.Session{}, nil, nil, nil)
	items := m.presets()
	if len(items) < 8 || !strings.Contains(items[0].stmt, "browse by Labels.Component, Labels.Variant") {
		t.Errorf("presets: %+v", items)
	}
	found := false
	for _, it := range items {
		if it.stmt == "Unit | in * | where TargetID IS NOT NULL | browse by Target.Labels.Environment, Target.Labels.Region, Labels.Component, Resource" {
			found = true
		}
	}
	if !found {
		t.Errorf("target→units→resources preset missing: %+v", items)
	}
}

// stubResources answers Resource statements with resource rows joined to units.
func stubResources(t *testing.T) Runner {
	return func(ctx context.Context, st lang.Stmt, sess plan.Session) (tea.Msg, error) {
		sel := st.(*lang.SelectStmt)
		p, err := plan.Compile(sel, sess)
		if err != nil {
			return nil, err
		}
		rows := []cubclient.Row{
			{"Resource": map[string]any{"ResourceType": "apps/v1/Deployment", "ResourceName": "/cart/api", "ResourceID": "r1", "UnitID": "u1"}, "Unit": map[string]any{"Slug": "api", "Labels": map[string]any{"Cluster": "c1"}}, "Space": map[string]any{"Slug": "cart-c1"}},
			{"Resource": map[string]any{"ResourceType": "v1/Service", "ResourceName": "/cart/api", "ResourceID": "r2", "UnitID": "u1"}, "Unit": map[string]any{"Slug": "api", "Labels": map[string]any{"Cluster": "c1"}}, "Space": map[string]any{"Slug": "cart-c1"}},
			{"Resource": map[string]any{"ResourceType": "v1/Namespace", "ResourceName": "/cart", "ResourceID": "r3", "UnitID": "u2"}, "Unit": map[string]any{"Slug": "namespace", "Labels": map[string]any{"Cluster": "c2"}}, "Space": map[string]any{"Slug": "cart-c2"}},
		}
		res, err := exec.Local(p, rows)
		if err != nil {
			return nil, err
		}
		return resultMsg{stmt: sel, plan: p, res: res}, nil
	}
}

func TestBrowseResourcesDirectly(t *testing.T) {
	var m tea.Model = New(planSession(), stubResources(t), nil, nil)
	m, _ = m.Update(tea.WindowSizeMsg{Width: 160, Height: 40})
	mm := m.(Model)
	mm.chooserOpen, mm.focus = false, focusCmd
	m = typeText(mm, "Resource | in * | browse by Unit.Labels.Cluster")
	m = press(m, "enter")
	mm = m.(Model)
	if mm.browse == nil || mm.browse.hop {
		t.Fatalf("browse: %+v status=%q", mm.browse, mm.status)
	}
	panes := mm.browse.panes()
	if len(panes) != 2 || len(panes[0]) != 2 || len(panes[1]) != 2 || !strings.HasPrefix(panes[1][0].label, "apps/v1/Deployment cart/api  ·api") {
		t.Errorf("panes: %+v", panes)
	}
	if v := m.View().Content; !strings.Contains(v, "Resources (2)") || !strings.Contains(v, "Unit·Cluster (2)") {
		t.Errorf("view:\n%s", v)
	}
}

// With the sample saying Component lives on spaces, unit presets reach it via Space.Labels.
func TestPresetsFollowLabelPlacement(t *testing.T) {
	m := New(plan.Session{}, nil, nil, nil)
	m.live = liveWith(map[string]map[string]int{"Space": {"Component": 50, "Variant": 50}, "Unit": {"Owner": 200, "Region": 150}}, map[string]int{"Space": 55, "Unit": 220})
	items := m.presets()
	if !strings.Contains(items[0].stmt, "browse by Space.Labels.Component, Space.Labels.Variant") {
		t.Errorf("first preset: %s", items[0].stmt)
	}
	for _, it := range items {
		if strings.Contains(it.stmt, "Labels.Cluster") || strings.Contains(it.stmt, "Labels.Department") {
			t.Errorf("preset uses a label the org lacks: %s", it.stmt)
		}
	}
}

func liveWith(keys map[string]map[string]int, totals map[string]int) *catalog.Live {
	return catalog.LiveWith(keys, totals)
}
