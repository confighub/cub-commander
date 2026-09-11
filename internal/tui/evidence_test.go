package tui

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/confighub/cub-commander/internal/cubclient"
	"github.com/confighub/cub-commander/internal/scout"
)

func evidenceRow() cubclient.Row {
	return cubclient.Row{"Resource": map[string]any{"ResourceID": "r1", "UnitID": "u1", "SpaceID": "s1", "TargetID": "t1", "ResourceType": "apps/v1/Deployment", "ResourceName": "team-a/api"}}
}

func evidenceModel(t *testing.T) Model {
	t.Helper()
	m := New(planSession(), stubRunner(t), nil, nil)
	m.width, m.height, m.chooserOpen = 100, 32, false
	m.layout()
	m.scoutConfig = scout.Config{Bindings: map[string]string{"t1": "test-cluster"}}
	m.openDetailRow(evidenceRow())
	return m
}

func evidenceKey(m Model, s string) (Model, tea.Cmd) {
	next, cmd := m.Update(tea.KeyPressMsg{Code: rune(s[0]), Text: s})
	return next.(Model), cmd
}

func recordedSnapshot(t *testing.T, req scout.Request) scout.Snapshot {
	t.Helper()
	body, err := os.ReadFile("../scout/testdata/observation.json")
	if err != nil {
		t.Fatal(err)
	}
	snap, err := scout.Decode(body, req, time.Date(2026, 9, 11, 12, 0, 1, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	return snap
}

func TestEvidenceExplicitActionReuseRefreshAndFailure(t *testing.T) {
	m := evidenceModel(t)
	calls := 0
	m.evidenceLoader = func(ctx context.Context, req scout.Request) (scout.Snapshot, error) {
		calls++
		if calls > 1 {
			return scout.Snapshot{}, errors.New("read failed")
		}
		return recordedSnapshot(t, req), nil
	}
	if m.det.evidence != nil || calls != 0 {
		t.Fatal("eager read")
	}
	m, cmd := evidenceKey(m, "3")
	if cmd == nil {
		t.Fatal("no read")
	}
	loaded, _ := m.Update(cmd())
	m = loaded.(Model)
	if calls != 1 || m.det.evidence.snapshot == nil {
		t.Fatal("no snapshot")
	}
	for _, key := range []string{"1", "3", "3", "e"} {
		m, cmd = evidenceKey(m, key)
		if cmd != nil {
			t.Fatalf("%s caused work", key)
		}
	}
	if calls != 1 || m.det.tab != 2 {
		t.Fatal("revisit read or edit escaped panel")
	}
	if !strings.Contains(m.evidenceBody(time.Date(2026, 9, 11, 12, 0, 15, 0, time.UTC)), "STALE snapshot") {
		t.Fatal("expiry not visible")
	}
	if !strings.Contains(m.evidenceBody(time.Date(2026, 9, 11, 12, 0, 1, 0, time.UTC)), "Captured snapshot") {
		t.Fatal("capture not visible")
	}
	m, cmd = evidenceKey(m, "r")
	if m.det.evidence.snapshot != nil || cmd == nil {
		t.Fatal("refresh retained old success")
	}
	loaded, _ = m.Update(cmd())
	m = loaded.(Model)
	if calls != 2 || m.det.evidence.snapshot != nil || !strings.Contains(m.evidenceBody(time.Now()), "Evidence unavailable") {
		t.Fatal("failure reused success")
	}
}

func TestEvidenceMissingBindingDoesNoWork(t *testing.T) {
	m := evidenceModel(t)
	m.scoutConfig.Bindings = nil
	m.evidenceLoader = func(context.Context, scout.Request) (scout.Snapshot, error) {
		t.Fatal("called provider")
		return scout.Snapshot{}, nil
	}
	m, cmd := evidenceKey(m, "3")
	if cmd != nil || m.det.evidence.err == nil {
		t.Fatal("missing binding accepted")
	}
}

func TestEvidenceCancellationAndLateResponse(t *testing.T) {
	for _, navigation := range []string{"metadata", "selection", "refresh", "quit", "chooser"} {
		t.Run(navigation, func(t *testing.T) {
			m := evidenceModel(t)
			m.evidenceLoader = func(ctx context.Context, req scout.Request) (scout.Snapshot, error) {
				if ctx.Err() == nil {
					t.Fatal("old context not cancelled")
				}
				return recordedSnapshot(t, req), nil
			}
			m, oldCmd := evidenceKey(m, "3")
			old := m.det.evidence
			switch navigation {
			case "metadata":
				m, _ = evidenceKey(m, "1")
			case "selection":
				row := evidenceRow()
				row["Resource"].(map[string]any)["TargetID"] = "t2"
				m.openDetailRow(row)
			case "refresh":
				m, _ = evidenceKey(m, "r")
			case "quit":
				m, _ = evidenceKey(m, "q")
			case "chooser":
				model, _ := m.Update(tea.KeyPressMsg{Code: 'b', Mod: tea.ModCtrl})
				m = model.(Model)
			}
			model, _ := m.Update(oldCmd())
			m = model.(Model)
			if old.loading || old.snapshot != nil {
				t.Fatal("late response accepted")
			}
			if m.det.evidence != nil {
				m.det.evidence.stop()
			}
		})
	}
}

func TestEvidenceSameNameDifferentIdentityDiscardsSnapshot(t *testing.T) {
	for _, key := range []string{"ResourceID", "SpaceID", "UnitID", "TargetID", "ResourceType", "ResourceName"} {
		t.Run(key, func(t *testing.T) {
			m := evidenceModel(t)
			m.evidenceLoader = func(_ context.Context, req scout.Request) (scout.Snapshot, error) {
				return recordedSnapshot(t, req), nil
			}
			m, cmd := evidenceKey(m, "3")
			model, _ := m.Update(cmd())
			m = model.(Model)
			row := evidenceRow()
			row["Resource"].(map[string]any)[key] = "other"
			m.openDetailRow(row)
			if m.det.evidence != nil {
				t.Fatal("evidence crossed selected identity")
			}
		})
	}
}

func TestEvidenceViewports(t *testing.T) {
	for _, width := range []int{40, 80, 120} {
		m := evidenceModel(t)
		m.width = width
		m.layout()
		req, err := scout.Resolve(evidenceRow(), m.scoutConfig.Bindings)
		if err != nil {
			t.Fatal(err)
		}
		snap := recordedSnapshot(t, req)
		snap.ObservedAt, snap.ExpiresAt = time.Unix(1, 0), time.Unix(16, 0)
		m.det.tab = 2
		m.det.evidence = &evidenceState{request: req, snapshot: &snap}
		m.renderDetail()
		for _, line := range strings.Split(m.evidenceBody(time.Now()), "\n") {
			if ansi.StringWidth(line) > width {
				t.Fatalf("%d columns overflow: %q", width, line)
			}
		}
		if !strings.Contains(ansi.Strip(m.detailView()), "3 Evidence") {
			t.Fatal("evidence tab hidden")
		}
		if !strings.Contains(ansi.Strip(m.View().Content), "snapshot") {
			t.Fatal("empty panel")
		}
		m.detail.GotoBottom()
		if !strings.Contains(ansi.Strip(m.View().Content), "STALE") {
			t.Fatal("freshness hidden while scrolling evidence")
		}
	}
}

func TestEvidenceCopyableCommand(t *testing.T) {
	words := []string{"/a path/scout", "explain", "Deployment/api", "--kube-context", "ctx'; $(touch sentinel)" + strings.Repeat("-long", 25), "--namespace", "", "--format", "json"}
	for _, width := range []int{40, 80, 120} {
		command := shellLines(words, width)
		for _, line := range strings.Split(command, "\n") {
			if ansi.StringWidth(line) > width {
				t.Fatalf("overflow %d: %q", width, line)
			}
		}
		// set -- treats all words as data. No generated command is executed.
		output, err := exec.Command("sh", "-c", "set -- "+command+"\nprintf '%s\\n' \"$@\"").Output()
		got := strings.Split(strings.TrimSuffix(string(output), "\n"), "\n")
		if err != nil || !reflect.DeepEqual(got, words) {
			t.Fatalf("%v\n%s\ngot %q", err, command, got)
		}
	}
}
