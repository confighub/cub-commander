package exec

import (
	"strings"
	"testing"

	"github.com/confighub/cub-commander/internal/cubclient"
	"github.com/confighub/cub-commander/internal/lang"
)

func unit(slug, space, hash string, labels map[string]any) cubclient.Row {
	return cubclient.Row{
		"Unit":  map[string]any{"Slug": slug, "UnitID": space + "/" + slug, "DataHash": hash, "Labels": labels},
		"Space": map[string]any{"Slug": space, "SpaceID": "s-" + space},
	}
}

func TestPairRowsAutoKeys(t *testing.T) {
	lab := func(env, region, cluster string) map[string]any {
		return map[string]any{"Component": "cart", "Environment": env, "Region": region, "Cluster": cluster}
	}
	a := []cubclient.Row{
		unit("api", "cart-us-east-dev1", "h1", lab("dev", "us-east", "us-east-dev1")),
		unit("api", "cart-eu-west-dev1", "h2", lab("dev", "eu-west", "eu-west-dev1")),
		unit("cache", "cart-us-east-dev1", "h3", lab("dev", "us-east", "us-east-dev1")),
	}
	b := []cubclient.Row{
		unit("api", "cart-us-east-prod1", "h1", lab("prod", "us-east", "us-east-prod1")),
		unit("api", "cart-eu-west-prod1", "h9", lab("prod", "eu-west", "eu-west-prod1")),
		unit("worker", "cart-us-east-prod1", "h4", lab("prod", "us-east", "us-east-prod1")),
	}
	res := PairRows("Unit", nil, a, b)
	keys := []string{}
	for _, k := range res.By {
		keys = append(keys, k.Path)
	}
	// Region is shared by both sides (same value set); Environment and Cluster are not.
	if got := strings.Join(keys, ","); got != "Slug,Labels.Component,Labels.Region" {
		t.Errorf("auto keys: %s", got)
	}
	want := map[string]string{"api / cart / us-east": "same", "api / cart / eu-west": "differ", "cache / cart / us-east": "only-a", "worker / cart / us-east": "only-b"}
	for _, p := range res.Pairs {
		if want[p.Key] != p.Status {
			t.Errorf("%s: want %s got %s", p.Key, want[p.Key], p.Status)
		}
	}
	if res.Counts["differ"] != 1 || res.Counts["same"] != 1 || len(res.Pairs) != 4 {
		t.Errorf("counts: %v", res.Counts)
	}
	// Explicit keys: by Slug only would be n:m, but refinement finds Region
	// and splits the api pair into per-region pairs.
	res = PairRows("Unit", []lang.Ref{{Path: "Slug"}}, a, b)
	got := map[string]string{}
	for _, p := range res.Pairs {
		got[p.Key] = p.Status
	}
	if got["api / us-east"] != "same" || got["api / eu-west"] != "differ" {
		t.Errorf("refined pairs: %v", got)
	}
}

func TestUnified(t *testing.T) {
	a := "a: 1\nb: 2\nc: 3\nd: 4\ne: 5\nf: 6\ng: 7\nh: 8\n"
	b := "a: 1\nb: 2\nc: 3\nd: 4\ne: 5\nf: 6\ng: 7\nh: 9\n"
	lines := Unified(a, b, 1)
	plus, minus := Changed(lines)
	if plus != 1 || minus != 1 {
		t.Errorf("changed: +%d -%d", plus, minus)
	}
	if lines[0].Kind != '@' || lines[len(lines)-1].Text != "h: 9" {
		t.Errorf("lines: %+v", lines)
	}
}

// Topology labels on the space only (the usual case): keys come from Space.Labels.
func TestPairRowsSpaceLabels(t *testing.T) {
	mk := func(slug, space, hash, comp, variant, region string) cubclient.Row {
		return cubclient.Row{
			"Unit":  map[string]any{"Slug": slug, "UnitID": space + "/" + slug, "DataHash": hash, "Labels": map[string]any{"Owner": "eng"}},
			"Space": map[string]any{"Slug": space, "SpaceID": "s-" + space, "Labels": map[string]any{"Component": comp, "Variant": variant, "Region": region}},
		}
	}
	a := []cubclient.Row{mk("api", "app-nonprod-east", "h1", "app", "nonprod", "us-east"), mk("api", "app-nonprod-west", "h2", "app", "nonprod", "us-west")}
	b := []cubclient.Row{mk("api", "app-prod-east", "h1", "app", "prod", "us-east"), mk("api", "app-prod-west", "h3", "app", "prod", "us-west")}
	res := PairRows("Unit", nil, a, b)
	keys := []string{}
	for _, k := range res.By {
		keys = append(keys, k.Path)
	}
	if got := strings.Join(keys, ","); got != "Slug,Space.Labels.Component,Space.Labels.Region" {
		t.Errorf("keys: %s", got)
	}
	got := map[string]string{}
	for _, p := range res.Pairs {
		got[p.Key] = p.Status
	}
	if got["api / app / us-east"] != "same" || got["api / app / us-west"] != "differ" {
		t.Errorf("pairs: %v", got)
	}
}
