package rollout

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// ReleaseOutcome is what publishing one space did.
type ReleaseOutcome struct {
	Space      Space
	ReleaseID  string
	ReleaseNum string
	Message    string // the server's note when no new release was needed
	Err        string
	Skipped    string
	WaitedFor  time.Duration // how long the awaiting/triggers gate held the publish
	StillGated []string      // units whose gate did not clear in time (the publish was not attempted)
}

// How long a publish waits for the triggers a promotion enqueued to finish.
// The server refuses a release while any bundled unit carries an ApplyGate,
// and `awaiting/triggers` is the transient one every write leaves behind.
var (
	TriggerWait     = 90 * time.Second
	triggerPollStep = 2 * time.Second
)

const awaitingTriggers = "awaiting/triggers"

// ReleaseStage publishes a release of every space of a stage that has taken
// the change and has a release target, pinned to the change order's end tag
// and published for the change order, so the server records the release on
// it and advances its Stage: `cub release publish --revision
// ChangeOrder:<slug> <space>` per space. A class base has no target and is
// skipped; a space that has released already is skipped too. Each publish
// first waits for the awaiting/triggers gate to clear on the space's units,
// as the CLI's --wait does after a promote. A stage's ReleasePrerequisites
// are the server's to enforce; its refusal is reported per space.
func ReleaseStage(ctx context.Context, c Client, r *Rollout, stage int) ([]ReleaseOutcome, error) {
	if stage <= 0 || stage >= len(r.Stages) {
		return nil, fmt.Errorf("no such stage")
	}
	if r.Order.EndTagID == "" {
		return nil, fmt.Errorf("change order %s has no end tag to pin a release to", r.Order.Slug)
	}
	var out []ReleaseOutcome
	for _, sp := range r.Stages[stage].Spaces {
		o := ReleaseOutcome{Space: sp}
		switch {
		case sp.ID == r.Order.SpaceID:
			o.Skipped = "the space the change order was created in"
		case !sp.Releasable:
			o.Skipped = "no release target; nothing releases this space"
		case !sp.Taken:
			o.Skipped = "has not taken the change; promote first"
		case sp.Released:
			o.Skipped = "already released this change"
		}
		if o.Skipped != "" {
			out = append(out, o)
			continue
		}
		started := time.Now()
		gated, err := waitForTriggers(ctx, c, sp.ID)
		o.WaitedFor = time.Since(started).Truncate(100 * time.Millisecond)
		if err != nil {
			o.Err = err.Error()
			out = append(out, o)
			continue
		}
		if len(gated) > 0 {
			o.StillGated = gated
			o.Err = fmt.Sprintf("%s still on %s after %s; the publish was not attempted", strings.Join(gated, ", "), awaitingTriggers, TriggerWait)
			out = append(out, o)
			continue
		}
		body := ReleaseRequest(r)
		_, res, err := c.Send(ctx, http.MethodPost, "/space/"+sp.ID+"/release", nil, "application/json", body)
		if err != nil {
			o.Err = err.Error()
			out = append(out, o)
			continue
		}
		var resp map[string]any
		_ = json.Unmarshal(res, &resp)
		rel := own(resp, "Release")
		o.ReleaseID = str(rel["ReleaseID"])
		o.ReleaseNum = str(rel["ReleaseNum"])
		o.Message = str(resp["Message"])
		out = append(out, o)
	}
	return out, nil
}

// ReleaseRequest is the body each publish sends: the end tag to pin to and
// the change order the release is published for.
func ReleaseRequest(r *Rollout) string {
	b, _ := json.Marshal(map[string]string{"TagID": r.Order.EndTagID, "ChangeOrderID": r.Order.ID})
	return string(b)
}

// waitForTriggers polls the space's units until none carries the
// awaiting/triggers gate, or TriggerWait passes; it returns the units still
// gated then.
func waitForTriggers(ctx context.Context, c Client, spaceID string) ([]string, error) {
	deadline := time.Now().Add(TriggerWait)
	for {
		rows, err := c.List(ctx, "/unit", url.Values{"where": {fmt.Sprintf("SpaceID = '%s'", spaceID)}, "select": {"UnitID,Slug,ApplyGates"}})
		if err != nil {
			return nil, fmt.Errorf("checking apply gates: %w", err)
		}
		var gated []string
		for _, row := range rows {
			u := own(row, "Unit")
			if gates, ok := u["ApplyGates"].(map[string]any); ok {
				if _, waiting := gates[awaitingTriggers]; waiting {
					gated = append(gated, str(u["Slug"]))
				}
			}
		}
		if len(gated) == 0 {
			return nil, nil
		}
		if time.Now().After(deadline) {
			return gated, nil
		}
		select {
		case <-ctx.Done():
			return gated, ctx.Err()
		case <-time.After(triggerPollStep):
		}
	}
}

// ReleaseCommands are the CLI lines a release of this stage stands for, one
// per space that would be published.
func ReleaseCommands(r *Rollout, stage int) []string {
	if stage <= 0 || stage >= len(r.Stages) {
		return nil
	}
	var out []string
	for _, sp := range r.Stages[stage].Spaces {
		if sp.ID == r.Order.SpaceID || !sp.Releasable || !sp.Taken || sp.Released {
			continue
		}
		out = append(out, fmt.Sprintf("cub release publish --revision ChangeOrder:%s %s", r.Order.Ref(), sp.Slug))
	}
	return out
}

// AfterPromote is the reading with the spaces a promotion just landed in
// marked as taken, so a release that follows the promote in one action does
// not skip them: the server's ResolvedSpaceIDs would say the same once
// re-read, and B has not re-read yet.
func AfterPromote(r *Rollout, promoted []Outcome) *Rollout {
	if len(promoted) == 0 {
		return r
	}
	landed := map[string]bool{}
	for _, o := range promoted {
		if o.Action == "Promote" && o.Err == "" && len(o.Errors) == 0 {
			landed[o.Space.ID] = true
		}
	}
	cp := *r
	cp.Stages = make([]StageState, len(r.Stages))
	for i, st := range r.Stages {
		cp.Stages[i] = st
		cp.Stages[i].Spaces = append([]Space(nil), st.Spaces...)
		for j := range cp.Stages[i].Spaces {
			if landed[cp.Stages[i].Spaces[j].ID] {
				cp.Stages[i].Spaces[j].Taken = true
			}
		}
	}
	return &cp
}
