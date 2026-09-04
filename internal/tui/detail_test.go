package tui

import (
	"context"
	"os"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/confighub/cub-commander/internal/cubclient"
)

func TestDetailTabsAndEdit(t *testing.T) {
	var m tea.Model = New(planSession(), stubRunner(t), nil, nil)
	m, _ = m.Update(tea.WindowSizeMsg{Width: 140, Height: 40})
	mm := m.(Model)
	mm.chooserOpen, mm.focus = false, focusCmd
	head := "replicas: 1\nimage: v1\n"
	saved := ""
	conflictOnce := true
	mm.dataLoader = func(ctx context.Context, row cubclient.Row) (string, string, error) { return head, "hash-1", nil }
	mm.dataSaver = func(ctx context.Context, row cubclient.Row, text, ifMatch string) (int, error) {
		if ifMatch != "hash-1" {
			t.Errorf("If-Match %q", ifMatch)
		}
		if conflictOnce {
			conflictOnce = false
			return 0, &cubclient.ConflictError{Message: "config data changed"}
		}
		saved = text
		return 18, nil
	}
	m = typeText(mm, "Unit | in *")
	m = press(m, "enter", "shift+tab", "enter")
	mm = m.(Model)
	if mm.mode != modeDetail || mm.det == nil || mm.det.tab != 0 {
		t.Fatalf("detail: mode=%v det=%+v", mm.mode, mm.det)
	}
	if v := m.View().Content; !strings.Contains(v, "1 Metadata") || !strings.Contains(v, "2 Data") || !strings.Contains(v, "HeadRevisionNum: 17") {
		t.Errorf("metadata tab:\n%s", v)
	}
	// Data tab loads through the loader.
	var cmd tea.Cmd
	m, cmd = mm.Update(tea.KeyPressMsg{Code: '2', Text: "2"})
	if cmd == nil {
		t.Fatal("no load")
	}
	m, _ = m.Update(cmd())
	mm = m.(Model)
	if !mm.det.loaded || !strings.Contains(m.View().Content, "replicas: 1") || !strings.Contains(m.View().Content, "hash hash-1") {
		t.Fatalf("data tab:\n%s", m.View().Content)
	}
	// Simulate the editor: write a changed file and deliver editedMsg.
	f, _ := os.CreateTemp("", "edit-*.yaml")
	f.WriteString("replicas: 2\nimage: v1\n")
	f.Close()
	m, cmd = mm.Update(editedMsg{path: f.Name()})
	if cmd == nil {
		t.Fatal("no save")
	}
	msg := cmd()
	m, cmd = m.Update(msg) // first save conflicts
	mm = m.(Model)
	if !mm.statusErr || !strings.Contains(mm.status, "conflict") || mm.det.draft == "" {
		t.Fatalf("conflict handling: %q draft=%q", mm.status, mm.det.draft)
	}
	if cmd != nil { // reload of the head
		m, _ = m.Update(cmd())
	}
	// e now reopens the draft: check the temp file content the editor would get.
	mm = m.(Model)
	if mm.det.draft != "replicas: 2\nimage: v1\n" || !strings.Contains(m.View().Content, "unsaved draft") {
		t.Errorf("draft kept: %q", mm.det.draft)
	}
	// Second attempt succeeds and reports the revision.
	f2, _ := os.CreateTemp("", "edit-*.yaml")
	f2.WriteString(mm.det.draft)
	f2.Close()
	m, cmd = mm.Update(editedMsg{path: f2.Name()})
	m, _ = m.Update(cmd())
	mm = m.(Model)
	if saved != "replicas: 2\nimage: v1\n" || !strings.Contains(mm.status, "revision 18") || mm.det.draft != "" {
		t.Errorf("save: saved=%q status=%q draft=%q", saved, mm.status, mm.det.draft)
	}
	// Unchanged edit saves nothing.
	f3, _ := os.CreateTemp("", "edit-*.yaml")
	f3.WriteString(head)
	f3.Close()
	mm.det.loaded, mm.det.data = true, head
	m, cmd = mm.Update(editedMsg{path: f3.Name()})
	if cmd != nil || !strings.Contains(m.(Model).status, "no changes") {
		t.Errorf("unchanged: %q", m.(Model).status)
	}
	// Esc returns to the grid.
	m = press(m, "esc")
	if m.(Model).mode != modeResults {
		t.Errorf("esc: %v", m.(Model).mode)
	}
}
