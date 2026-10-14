package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/confighub/sdk/cubutil/blame"

	"github.com/confighub/cub-commander/internal/cubclient"
)

// stubBlame is a unit's blame with two resources, and records what Path was asked for.
type stubBlame struct {
	loads   int
	path    string
	history bool
}

func (s *stubBlame) loader() BlameLoader {
	return func(ctx context.Context, row cubclient.Row) (*BlameResult, error) {
		s.loads++
		when := time.Now().Add(-2 * time.Hour)
		upstream := &blame.Origin{SpaceSlug: "registry-facts", UnitSlug: "ui-image", RevisionNum: 81, SetBy: "UpdateUnit", User: "registrybot", When: when}
		here := &blame.Origin{SpaceSlug: "prod", UnitSlug: "web", RevisionNum: 31, SetBy: "link ui-stable-image",
			Function: "set-image-reference-by-uri", LinkUpstream: "registry-facts/ui-image", LinkUpstreamRevisionNum: 81, When: when}
		fields := []*blame.Field{
			{Ref: "1111111", ResourceType: "apps/v1/Deployment", ResourceName: "prod/web", Path: "spec.replicas", Value: "3", Flags: "W",
				Validation: []blame.Validation{{Gate: "prod/replicas/vet-cel", Warning: true, Details: []string{"replicas must be more than 3"}}},
				Chain:      []*blame.Origin{{SpaceSlug: "prod", UnitSlug: "web", RevisionNum: 4, SetBy: "set-replicas", User: "brian", When: when}}},
			{Ref: "2222222", ResourceType: "apps/v1/Deployment", ResourceName: "prod/web", Path: "spec.template.spec.containers.?name=web.image",
				Value: "ghcr.io/web:v1", Flags: "PL", Chain: []*blame.Origin{here, upstream}},
			{Ref: "3333333", ResourceType: "v1/Service", ResourceName: "prod/web", Path: "spec.type", Value: "ClusterIP"},
			{Ref: "4444444", ResourceType: "v1/Service", ResourceName: "prod/web", Path: "spec.$comment$head$type", Value: "a comment"},
		}
		return &BlameResult{
			Fields: fields,
			Path: func(ctx context.Context, f *blame.Field, history bool) (*blame.PathReport, error) {
				s.path, s.history = f.Path, history
				report := &blame.PathReport{Field: f, Released: "yes: the unit's last release is of revision 31"}
				for _, origin := range f.Chain {
					report.Changes = append(report.Changes, blame.PathChange{Origin: origin})
				}
				if history {
					report.History = []blame.PathChange{{Origin: here, MutationType: "Update", Value: "ghcr.io/web:v1"}}
				}
				return report, nil
			},
		}, nil
	}
}

// update sends a key and runs what it returns, as the program would.
func update(m tea.Model, k tea.KeyPressMsg) tea.Model {
	m, cmd := m.Update(k)
	return runCmd(m, cmd, 0)
}

func keyMsg(s string) tea.KeyPressMsg {
	switch s {
	case "enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	case "esc":
		return tea.KeyPressMsg{Code: tea.KeyEscape}
	case "down":
		return tea.KeyPressMsg{Code: tea.KeyDown}
	case "up":
		return tea.KeyPressMsg{Code: tea.KeyUp}
	case "right":
		return tea.KeyPressMsg{Code: tea.KeyRight}
	}
	return tea.KeyPressMsg{Code: rune(s[0]), Text: s}
}

func blameModel(t *testing.T, stub *stubBlame, row cubclient.Row) tea.Model {
	t.Helper()
	var m tea.Model = New(planSession(), stubRunner(t), nil, nil)
	m, _ = m.Update(tea.WindowSizeMsg{Width: 180, Height: 40})
	mm := m.(Model)
	mm.chooserOpen = false
	mm.blameLoader = stub.loader()
	mm.openDetailRow(row)
	return mm
}

func unitRow() cubclient.Row {
	return cubclient.Row{"Unit": map[string]any{"Slug": "web", "UnitID": "u1", "SpaceID": "s1"}}
}

// 3 opens the Blame tab: one line per field, grouped by resource, with what set it after
// following it upstream, and the legend for the flags that appear. Comment fields are left out.
func TestBlameTabListsFields(t *testing.T) {
	stub := &stubBlame{}
	m := blameModel(t, stub, unitRow())
	if !strings.Contains(m.View().Content, "3 Blame") {
		t.Fatalf("no Blame tab:\n%s", m.View().Content)
	}
	m = update(m, keyMsg("3"))
	if m.(Model).det.tab != blameTab || stub.loads != 1 {
		t.Fatalf("tab=%d loads=%d", m.(Model).det.tab, stub.loads)
	}
	v := m.View().Content
	for _, want := range []string{"apps/v1/Deployment prod/web", "v1/Service prod/web", "spec.replicas", "set-replicas",
		"UpdateUnit", "registry-facts", "registrybot", "P  protected", "W  validation warning"} {
		if !strings.Contains(v, want) {
			t.Errorf("Blame tab lacks %q:\n%s", want, v)
		}
	}
	if strings.Contains(v, "$comment$") {
		t.Errorf("a comment field is listed:\n%s", v)
	}

	// Switching away and back does not read it again; R does.
	m = update(m, keyMsg("1"))
	m = update(m, keyMsg("3"))
	if stub.loads != 1 {
		t.Errorf("the tab was read %d times", stub.loads)
	}
	m = update(m, keyMsg("R"))
	if stub.loads != 2 {
		t.Errorf("R did not reload: %d loads", stub.loads)
	}
}

// ↓ chooses the next field; Enter shows its record in the text view, and Esc comes back to
// the list with the same field chosen.
func TestBlameEnterOpensTheFieldRecord(t *testing.T) {
	stub := &stubBlame{}
	m := blameModel(t, stub, unitRow())
	m = update(m, keyMsg("3"))
	m = update(m, keyMsg("down"))
	if c := m.(Model).det.blame.cursor; c != 1 {
		t.Fatalf("cursor=%d", c)
	}
	m = update(m, keyMsg("enter"))
	mm := m.(Model)
	if mm.mode != modeText || stub.path != "spec.template.spec.containers.?name=web.image" || stub.history {
		t.Fatalf("mode=%v path=%q history=%v", mm.mode, stub.path, stub.history)
	}
	v := m.View().Content
	for _, want := range []string{"Set by:", "link ui-stable-image", "Read from:", "registry-facts/ui-image, revision 81", "Released:"} {
		if !strings.Contains(v, want) {
			t.Errorf("the record lacks %q:\n%s", want, v)
		}
	}
	m = update(m, keyMsg("esc"))
	mm = m.(Model)
	if mm.mode != modeDetail || mm.det.tab != blameTab || mm.det.blame.cursor != 1 {
		t.Fatalf("Esc: mode=%v tab=%d cursor=%d", mm.mode, mm.det.tab, mm.det.blame.cursor)
	}
}

// h reads the record with every change the unit recorded to the field.
func TestBlameHistory(t *testing.T) {
	stub := &stubBlame{}
	m := blameModel(t, stub, unitRow())
	m = update(m, keyMsg("3"))
	m = update(m, keyMsg("down"))
	m = update(m, keyMsg("h"))
	if !stub.history || !strings.Contains(m.View().Content, "History of the field") {
		t.Fatalf("history=%v:\n%s", stub.history, m.View().Content)
	}
}

// A resource's Blame tab lists only that resource's fields.
func TestBlameTabOfAResource(t *testing.T) {
	stub := &stubBlame{}
	row := cubclient.Row{
		"Resource": map[string]any{"UnitID": "u1", "SpaceID": "s1", "ResourceType": "v1/Service", "ResourceName": "prod/web"},
	}
	m := blameModel(t, stub, row)
	m = update(m, keyMsg("3"))
	v := m.View().Content
	if !strings.Contains(v, "spec.type") || strings.Contains(v, "spec.replicas") {
		t.Fatalf("the resource's tab should list only its own fields:\n%s", v)
	}
}

// A row with no data has no Blame tab.
func TestBlameNeedsData(t *testing.T) {
	stub := &stubBlame{}
	m := blameModel(t, stub, cubclient.Row{"Space": map[string]any{"Slug": "prod", "SpaceID": "s1"}})
	mm := m.(Model)
	mm.det.entity = "Space"
	m = update(mm, keyMsg("3"))
	if m.(Model).det.tab != 0 || stub.loads != 0 {
		t.Fatalf("tab=%d loads=%d", m.(Model).det.tab, stub.loads)
	}
}

// The record of a field a validating Trigger failed says which check and what it found.
func TestBlameRecordShowsFailedChecks(t *testing.T) {
	stub := &stubBlame{}
	m := blameModel(t, stub, unitRow())
	m = update(m, keyMsg("3"))
	m = update(m, keyMsg("enter"))
	if v := m.View().Content; !strings.Contains(v, "Validation:") || !strings.Contains(v, "warning prod/replicas/vet-cel: replicas must be more than 3") {
		t.Fatalf("the record lacks the failed check:\n%s", v)
	}
}
