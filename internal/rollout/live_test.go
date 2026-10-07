package rollout

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/confighub/cub-commander/internal/cubclient"
)

// TestLive reads one change order on a real server, read-only: the reading,
// the dry run of its next stage with diffs, and the change in its base and
// in one space that has taken it. Opt in with
//
//	COMMANDER_ROLLOUT_LIVE_ORDER=<space>/<slug> CUB_SERVER=… CUB_TOKEN=$(cub auth get-token) \
//	  go test ./internal/rollout -run TestLive -v
//
// It writes nothing: a dry run plans and evaluates gates without a write.
func TestLive(t *testing.T) {
	ref := os.Getenv("COMMANDER_ROLLOUT_LIVE_ORDER")
	if ref == "" {
		t.Skip("set COMMANDER_ROLLOUT_LIVE_ORDER=<space>/<slug> (and CUB_SERVER, CUB_TOKEN) to run against a server")
	}
	space, slug, ok := strings.Cut(ref, "/")
	if !ok {
		t.Fatalf("COMMANDER_ROLLOUT_LIVE_ORDER wants <space>/<slug>, got %q", ref)
	}
	c, err := cubclient.New()
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	rows, err := c.List(ctx, "/change_order", map[string][]string{"where": {fmt.Sprintf("Slug = '%s' AND Space.Slug = '%s'", slug, space)}, "include": {"SpaceID"}})
	if err != nil || len(rows) != 1 {
		t.Fatalf("change order %s: %v (%d rows)", ref, err, len(rows))
	}
	r, err := Load(ctx, c, NewCache(), rows[0])
	if err != nil {
		t.Fatal(err)
	}
	t.Log("\n" + r.Text())
	if r.Workflow == nil {
		t.Fatalf("no workflow on %s", ref)
	}
	if r.Next > 0 {
		p, err := PreviewStage(ctx, c, r, r.Next)
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("preview of %s: plan %s refused %v complete %v blockers %v", p.Stage, p.Plan, p.Refused, p.Complete, p.Blockers())
		for _, sp := range p.Spaces {
			t.Logf("  %s %s %s", sp.Space.Slug, sp.Action, sp.Reason)
			for _, u := range sp.Units {
				t.Logf("    %-20s %-10s %-10s fields %d kept %d err %q", u.Slug, u.Action, u.Reason, len(u.Fields), len(u.Kept), u.Err)
				for _, f := range u.Fields {
					t.Logf("      %s", f)
				}
				for _, k := range u.Kept {
					t.Logf("      KEPT %s: stays %s (upstream %s) protected %v %s", k.Path, k.Current, k.Upstream, k.Protected, k.Why)
				}
			}
		}
	}
	base, err := ChangeIn(ctx, c, r, r.Order.SpaceID)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("the ordered change in %s:", r.Order.SpaceSlug)
	for _, u := range base {
		t.Logf("  %-20s rev %d → %d touched %v fields %d", u.Slug, u.StartRev, u.EndRev, u.Touched, len(u.Fields))
		for _, f := range u.Fields {
			t.Logf("    %s", f)
		}
	}
	for _, st := range r.Stages[1:] {
		for _, sp := range st.Spaces {
			if !sp.Taken {
				continue
			}
			ch, err := ChangeIn(ctx, c, r, sp.ID)
			if err != nil {
				t.Fatal(err)
			}
			t.Logf("the change in %s (stage %s):", sp.Slug, st.Name)
			for _, u := range ch {
				t.Logf("  %-20s rev %d → %d touched %v fields %d kept %d", u.Slug, u.StartRev, u.EndRev, u.Touched, len(u.Fields), len(u.Kept))
			}
			return
		}
	}
}
