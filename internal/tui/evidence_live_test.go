package tui

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/confighub/cub-commander/internal/cubclient"
	"github.com/confighub/cub-commander/internal/lang"
	"github.com/confighub/cub-commander/internal/scout"
)

// Opt-in proof: mocked intended state, actual provider and existing live object.
// It neither creates resources nor requires ConfigHub authentication.
func TestEvidenceLive(t *testing.T) {
	bin := os.Getenv("COMMANDER_SCOUT_LIVE_BINARY")
	if bin == "" {
		t.Skip("set COMMANDER_SCOUT_LIVE_BINARY and explicit scope to opt in")
	}
	contextName := os.Getenv("COMMANDER_SCOUT_LIVE_CONTEXT")
	typ := os.Getenv("COMMANDER_SCOUT_LIVE_TYPE")
	name := os.Getenv("COMMANDER_SCOUT_LIVE_NAME")
	row := evidenceRow()
	res := row["Resource"].(map[string]any)
	res["ResourceType"], res["ResourceName"] = typ, name
	cfg := scout.Config{Binary: bin, Bindings: map[string]string{"t1": contextName}}
	if _, err := scout.Resolve(row, cfg.Bindings); err != nil {
		t.Fatal(err)
	}
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Method != "GET" || r.URL.Path != "/api/resource" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(400)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]cubclient.Row{row})
	}))
	defer server.Close()
	t.Setenv("CUB_SERVER", server.URL)
	t.Setenv("CUB_TOKEN", "offline-fixture-token")
	client, err := cubclient.New()
	if err != nil {
		t.Fatal(err)
	}
	runner := DefaultRunner(client, nil)
	stmts, err := lang.Parse("Resource | in *")
	if err != nil {
		t.Fatal(err)
	}
	msg, err := runner(context.Background(), stmts[0], planSession())
	if err != nil {
		t.Fatal(err)
	}
	m := evidenceModel(t)
	model, _ := m.Update(msg)
	m = model.(Model)
	if len(m.result.Raw) != 1 {
		t.Fatal("selected row missing")
	}
	m.scoutConfig = cfg
	calls := 0
	m.evidenceLoader = func(ctx context.Context, req scout.Request) (scout.Snapshot, error) {
		calls++
		return cfg.Load(ctx, req)
	}
	m.openDetailRow(m.result.Raw[0])
	m, cmd := evidenceKey(m, "3")
	if cmd == nil {
		t.Fatal("no provider command")
	}
	model, _ = m.Update(cmd())
	m = model.(Model)
	if m.det.evidence.err != nil {
		t.Fatal(m.det.evidence.err)
	}
	if m.det.evidence.snapshot == nil || !m.det.evidence.snapshot.Available {
		t.Fatalf("observation unavailable: %+v", m.det.evidence.snapshot)
	}
	for _, key := range []string{"1", "3", "1", "3"} {
		m, cmd = evidenceKey(m, key)
		if cmd != nil {
			t.Fatal("revisit performed work")
		}
	}
	m, cmd = evidenceKey(m, "r")
	model, _ = m.Update(cmd())
	m = model.(Model)
	if m.det.evidence.err != nil || m.det.evidence.snapshot == nil || !m.det.evidence.snapshot.Available {
		t.Fatal("refresh failed")
	}
	if requests.Load() != 1 || calls != 2 {
		t.Fatalf("requests: intended-state=%d provider=%d", requests.Load(), calls)
	}
	if !strings.Contains(m.View().Content, "snapshot") {
		t.Fatal("evidence not rendered")
	}
	t.Logf("mocked ConfigHub row; live %s %s in context %s; 1 intended-state GET, 2 provider calls, 0 on tab revisits", typ, name, contextName)
}
