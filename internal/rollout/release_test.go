package rollout

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/confighub/cub-commander/internal/cubclient"
)

func TestReleaseStage(t *testing.T) {
	c := ChapterOne()
	r := order(t, c, "co-cm")
	// test2 has taken the change but not released it (pretend)
	for i := range r.Stages[3].Spaces {
		if r.Stages[3].Spaces[i].Slug == "cert-manager-us-east-test2" {
			r.Stages[3].Spaces[i].Released = false
		}
	}
	out, err := ReleaseStage(context.Background(), c, r, 3)
	if err != nil || len(out) != 2 {
		t.Fatalf("%v %+v", err, out)
	}
	if out[0].Skipped != "already released this change" || out[1].ReleaseID != "rel-1" || out[1].Err != "" {
		t.Errorf("%+v", out)
	}
	if len(c.Posts) != 1 || c.Posts[0] != `/space/cm-test2/release {"TagID":"tag-cm-end"}` {
		t.Errorf("posts: %v", c.Posts)
	}
	if got := ReleaseCommands(r, 3); len(got) != 1 || got[0] != "cub release publish --revision ChangeOrder:cert-manager-base/cert-manager-1-17-0 cert-manager-us-east-test2" {
		t.Errorf("%v", got)
	}
	// class bases have no target: nothing to publish, said per space
	out, _ = ReleaseStage(context.Background(), c, r, 1)
	for _, o := range out {
		if !strings.Contains(o.Skipped, "no release target") {
			t.Errorf("%+v", o)
		}
	}
	if got := ReleaseCommands(r, 1); len(got) != 0 {
		t.Errorf("class bases produced publish lines: %v", got)
	}
}

func TestReleaseWaitsForTriggers(t *testing.T) {
	c := ChapterOne()
	r := order(t, c, "co-cm")
	r.Stages[3].Spaces[1].Released = false
	c.Rows["/unit"] = append(c.Rows["/unit"], cubclient.Row{"Unit": map[string]any{
		"UnitID": "u-t2-ctl", "SpaceID": "cm-test2", "Slug": "controller", "ApplyGates": map[string]any{"awaiting/triggers": true},
	}})
	saved := TriggerWait
	TriggerWait, triggerPollStep = 30*time.Millisecond, 5*time.Millisecond
	defer func() { TriggerWait = saved; triggerPollStep = 2 * time.Second }()
	out, err := ReleaseStage(context.Background(), c, r, 3)
	if err != nil {
		t.Fatal(err)
	}
	o := out[1]
	if len(o.StillGated) != 1 || o.StillGated[0] != "controller" || !strings.Contains(o.Err, "awaiting/triggers") || len(c.Posts) != 0 {
		t.Errorf("%+v posts %v", o, c.Posts)
	}
}
