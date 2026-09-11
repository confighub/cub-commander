package scout

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/confighub/cub-commander/internal/cubclient"
)

func testRow() cubclient.Row {
	return cubclient.Row{"Resource": map[string]any{"ResourceID": "r1", "SpaceID": "s1", "UnitID": "u1", "TargetID": "t1", "ResourceType": "apps/v1/Deployment", "ResourceName": "team-a/api"}}
}

func testRequest(t *testing.T) Request {
	t.Helper()
	r, err := Resolve(testRow(), map[string]string{"t1": "test-cluster"})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func fixture(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/observation.json")
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestBindings(t *testing.T) {
	for _, values := range [][]string{{""}, {"target"}, {"=ctx"}, {"t="}, {"t=ctx", "t=ctx"}, {"t=ctx", "t=other"}, {"t=ctx\n"}, {" t=ctx"}, {"t=ctx\x1b"}} {
		if _, err := ParseBindings(values); err == nil {
			t.Errorf("accepted %q", values)
		}
	}
	bindings, err := ParseBindings([]string{"t=ctx=with spaces", "u=another"})
	if err != nil || bindings["t"] != "ctx=with spaces" {
		t.Fatalf("%v %v", bindings, err)
	}
}

func TestResolveExactOwnIdentity(t *testing.T) {
	for _, key := range []string{"ResourceID", "SpaceID", "UnitID", "TargetID", "ResourceType", "ResourceName"} {
		t.Run("missing-"+key, func(t *testing.T) {
			row := testRow()
			delete(row["Resource"].(map[string]any), key)
			row["Unit"] = map[string]any{"UnitID": "u1", "SpaceID": "s1", "TargetID": "t1"}
			row["Target"] = map[string]any{"TargetID": "t1", "Slug": "test-cluster"}
			if _, err := Resolve(row, map[string]string{"t1": "test-cluster"}); err == nil {
				t.Fatal("inferred missing identity")
			}
		})
	}
	for _, name := range []string{"api", "team-a/api/status", "team-a/../api", "team-a/api;touch", "team-a/", "team.a/api"} {
		row := testRow()
		row["Resource"].(map[string]any)["ResourceName"] = name
		if _, err := Resolve(row, map[string]string{"t1": "ctx"}); err == nil {
			t.Errorf("accepted %q", name)
		}
	}
	for _, typ := range []string{"Deployment", "apps/v1/Deployment/status", "v1/Secret", "v1/secret", "apps/v1/Deployment;echo"} {
		row := testRow()
		row["Resource"].(map[string]any)["ResourceType"] = typ
		if _, err := Resolve(row, map[string]string{"t1": "ctx"}); err == nil {
			t.Errorf("accepted %q", typ)
		}
	}
	if _, err := Resolve(testRow(), nil); err == nil {
		t.Fatal("used implicit context")
	}
	row := testRow()
	row["Resource"].(map[string]any)["ResourceType"] = "v1/Namespace"
	row["Resource"].(map[string]any)["ResourceName"] = "/team-a"
	r, err := Resolve(row, map[string]string{"t1": "ctx"})
	if err != nil || r.Resource.Namespace != "" || r.Resource.APIVersion != "v1" {
		t.Fatalf("cluster scoped: %+v %v", r, err)
	}
}

func TestCommandHasOneAllowlistedOperation(t *testing.T) {
	r := testRequest(t)
	r.Context = "ctx'; $(touch sentinel)"
	c := Config{}
	bin, args := c.Command(r)
	want := []string{"scout", "explain", "Deployment/api", "--bounded", "--api-version", "apps/v1", "--kube-context", r.Context, "--namespace", "team-a", "--format", "json"}
	if bin != "cub" || !reflect.DeepEqual(args, want) {
		t.Fatalf("%q %q", bin, args)
	}
	if !strings.Contains(c.CommandText(r), "'ctx'\"'\"'; $(touch sentinel)'") {
		t.Fatal(c.CommandText(r))
	}
	c.Binary = "/path with spaces/cub-scout"
	bin, args = c.Command(r)
	if bin != c.Binary || !reflect.DeepEqual(args, want[1:]) {
		t.Fatalf("%q %q", bin, args)
	}
}

func TestDecode(t *testing.T) {
	r := testRequest(t)
	now := time.Date(2026, 9, 11, 12, 0, 1, 0, time.UTC)
	b := fixture(t)
	snap, err := Decode(b, r, now)
	if err != nil || !snap.Available || !strings.Contains(snap.JSON, "9007199254740993") || !strings.Contains(snap.JSON, "observed-space") {
		t.Fatalf("%+v %v", snap, err)
	}
	// Expired evidence remains a historical snapshot, not an automatic refresh.
	if _, err := Decode(b, r, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		name   string
		change func(map[string]any, map[string]any)
	}{
		{"missing-envelope", func(m, r map[string]any) { delete(m, "resourceRead") }},
		{"missing-available", func(m, r map[string]any) { delete(r, "available") }},
		{"context", func(m, r map[string]any) { r["context"] = "other-cluster" }},
		{"case-ambiguous-context", func(m, r map[string]any) { r["Context"] = "test-cluster" }},
		{"namespace", func(m, r map[string]any) { r["resource"].(map[string]any)["namespace"] = "other" }},
		{"api", func(m, r map[string]any) { r["resource"].(map[string]any)["apiVersion"] = "extensions/v1beta1" }},
		{"kind", func(m, r map[string]any) { r["resource"].(map[string]any)["kind"] = "StatefulSet" }},
		{"name", func(m, r map[string]any) { r["resource"].(map[string]any)["name"] = "other" }},
		{"no-counts", func(m, r map[string]any) { delete(r, "reads") }},
		{"budget", func(m, r map[string]any) { r["reads"].(map[string]any)["object"] = 2 }},
		{"negative", func(m, r map[string]any) { r["reads"].(map[string]any)["object"] = -1 }},
		{"fake-hit", func(m, r map[string]any) { r["cache"] = "hit" }},
		{"no-observed", func(m, r map[string]any) { delete(r, "observedAt") }},
		{"future", func(m, r map[string]any) { r["observedAt"] = "2026-09-11T12:00:05Z" }},
		{"long-ttl", func(m, r map[string]any) { r["expiresAt"] = "2026-09-11T12:01:00Z" }},
		{"inverted-ttl", func(m, r map[string]any) { r["expiresAt"] = "2026-09-11T11:59:59Z" }},
		{"unavailable-freshness", func(m, r map[string]any) { r["available"] = false }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var m map[string]any
			_ = json.Unmarshal(b, &m)
			tt.change(m, m["resourceRead"].(map[string]any))
			bad, _ := json.Marshal(m)
			if _, err := Decode(bad, r, now); err == nil {
				t.Fatal("accepted invalid evidence")
			}
		})
	}
	for _, body := range []string{"null", "{}", "[]", "not JSON", string(b) + "{}", strings.Replace(string(b), `"context": "test-cluster"`, `"context": "wrong", "context": "test-cluster"`, 1)} {
		if _, err := Decode([]byte(body), r, now); err == nil {
			t.Errorf("accepted %s", body)
		}
	}
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	read := m["resourceRead"].(map[string]any)
	read["available"], read["observedAt"], read["expiresAt"] = false, "0001-01-01T00:00:00Z", "0001-01-01T00:00:00Z"
	m["notes"] = []string{"partial \x1b[31m untrusted"}
	m["data"] = "DO_NOT_DISPLAY_RAW_PAYLOAD"
	partial, _ := json.Marshal(m)
	snap, err = Decode(partial, r, now)
	if err != nil || snap.Available || strings.Contains(snap.JSON, "\x1b") || strings.Contains(snap.JSON, "DO_NOT_DISPLAY") || strings.Contains(snap.JSON, `"owner"`) {
		t.Fatalf("%+v %v", snap, err)
	}
}

// The test executable doubles as a real subprocess, with no shell dependency.
func TestMain(m *testing.M) {
	if os.Getenv("COMMANDER_SCOUT_TEST_HELPER") == "1" {
		switch os.Args[1] {
		case "args":
			_ = json.NewEncoder(os.Stdout).Encode(os.Args[2:])
		case "fail":
			fmt.Fprint(os.Stderr, "PRIVATE_TOKEN_DO_NOT_RENDER")
			os.Exit(1)
		case "large":
			fmt.Fprint(os.Stdout, strings.Repeat("x", maxOutput+1))
		case "large-stderr":
			fmt.Fprint(os.Stderr, strings.Repeat("x", (64<<10)+1))
		case "sleep":
			time.Sleep(30 * time.Second)
		case "explain":
			if os.Getenv("COMMANDER_SCOUT_TEST_SLEEP") == "1" {
				time.Sleep(30 * time.Second)
			}
			fmt.Fprint(os.Stdout, os.Getenv("COMMANDER_SCOUT_TEST_RESPONSE"))
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestSubprocessBoundary(t *testing.T) {
	t.Setenv("COMMANDER_SCOUT_TEST_HELPER", "1")
	bin, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"ctx'; $(touch sentinel)", "", "a b"}
	body, err := run(context.Background(), bin, append([]string{"args"}, want...))
	var got []string
	_ = json.Unmarshal(body, &got)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("%q %v", body, err)
	}
	for _, mode := range []string{"fail", "large", "large-stderr", "sleep"} {
		t.Run(mode, func(t *testing.T) {
			timeout := 3 * time.Second
			if mode == "sleep" {
				timeout = 200 * time.Millisecond
			}
			ctx, cancel := context.WithTimeout(context.Background(), timeout)
			defer cancel()
			start := time.Now()
			_, err := run(ctx, bin, []string{mode})
			if err == nil || strings.Contains(err.Error(), "PRIVATE_TOKEN") || time.Since(start) > 3*time.Second {
				t.Fatalf("%v after %s", err, time.Since(start))
			}
			if strings.HasPrefix(mode, "large") && !strings.Contains(err.Error(), "output limit") {
				t.Fatal(err)
			}
		})
	}
	t.Setenv("COMMANDER_SCOUT_TEST_RESPONSE", strings.ReplaceAll(string(fixture(t)), "2026-09-11", "2000-01-01"))
	snap, err := (Config{Binary: bin}).Load(context.Background(), testRequest(t))
	if err != nil || !snap.Available {
		t.Fatalf("real process decoder: %+v %v", snap, err)
	}
	if _, err := (Config{Binary: bin}).Load(context.Background(), Request{}); err == nil {
		t.Fatal("invalid request started provider")
	}
}

func TestProcessSessionClose(t *testing.T) {
	t.Setenv("COMMANDER_SCOUT_TEST_HELPER", "1")
	t.Setenv("COMMANDER_SCOUT_TEST_SLEEP", "1")
	bin, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	s := NewProcessSession(Config{Binary: bin})
	var wg sync.WaitGroup
	r := testRequest(t)
	for range 3 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := s.Load(context.Background(), r); err == nil {
				t.Error("closed read succeeded")
			}
		}()
	}
	start := time.Now()
	s.Close()
	wg.Wait()
	if time.Since(start) > 3*time.Second {
		t.Fatal("close did not bound shutdown")
	}
	if _, err := s.Load(context.Background(), r); err == nil {
		t.Fatal("started read after close")
	}
}
