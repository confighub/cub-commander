package catalog

import (
	"context"
	"net/url"
	"sort"
	"sync"

	"github.com/confighub/cub-commander/internal/cubclient"
)

// Live is what the catalog learns from the org itself: label keys and values
// with counts, Values keys, and slugs. Sampled once per session, refreshed on
// demand.
type Live struct {
	mu        sync.RWMutex
	labelKeys map[string]map[string]int            // entity → key → count
	labelVals map[string]map[string]map[string]int // entity → key → value → count
	valueKeys map[string]int                       // Unit Values keys
	spaces    []string
	totals    map[string]int // entity → rows sampled
}

type Count struct {
	Name string
	N    int
}

// Text is the name; exists so label values and keys share a shape.
func (c Count) Text() string { return c.Name }

func NewLive() *Live {
	return &Live{labelKeys: map[string]map[string]int{}, labelVals: map[string]map[string]map[string]int{}, valueKeys: map[string]int{}, totals: map[string]int{}}
}

// Sample fetches labels for Unit, Space and Target org-wide.
func (l *Live) Sample(ctx context.Context, c *cubclient.Client) error {
	for _, ent := range []string{"Space", "Unit", "Target"} {
		e, _ := Lookup(ent)
		sel := "Slug,Labels"
		if ent == "Unit" {
			sel = "Slug,Labels,Values"
		}
		rows, err := c.List(ctx, e.OrgPath, url.Values{"select": {sel}})
		if err != nil {
			return err
		}
		l.mu.Lock()
		keys := map[string]int{}
		vals := map[string]map[string]int{}
		for _, r := range rows {
			own, _ := r[ent].(map[string]any)
			if own == nil {
				continue
			}
			if ent == "Space" {
				if s, _ := own["Slug"].(string); s != "" {
					l.spaces = append(l.spaces, s)
				}
			}
			if labels, ok := own["Labels"].(map[string]any); ok {
				for k, v := range labels {
					keys[k]++
					if vals[k] == nil {
						vals[k] = map[string]int{}
					}
					if s, ok := v.(string); ok {
						vals[k][s]++
					}
				}
			}
			if values, ok := own["Values"].(map[string]any); ok {
				for k := range values {
					l.valueKeys[k]++
				}
			}
		}
		l.labelKeys[ent] = keys
		l.labelVals[ent] = vals
		l.totals[ent] = len(rows)
		sort.Strings(l.spaces)
		l.mu.Unlock()
	}
	return nil
}

func sorted(m map[string]int) []Count {
	out := make([]Count, 0, len(m))
	for k, n := range m {
		out = append(out, Count{k, n})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].N != out[j].N {
			return out[i].N > out[j].N
		}
		return out[i].Name < out[j].Name
	})
	return out
}

func (l *Live) LabelKeys(entity string) []Count {
	if l == nil {
		return nil
	}
	l.mu.RLock()
	defer l.mu.RUnlock()
	return sorted(l.labelKeys[entity])
}

func (l *Live) LabelValues(entity, key string) []Count {
	if l == nil {
		return nil
	}
	l.mu.RLock()
	defer l.mu.RUnlock()
	return sorted(l.labelVals[entity][key])
}

func (l *Live) ValueKeys() []Count {
	if l == nil {
		return nil
	}
	l.mu.RLock()
	defer l.mu.RUnlock()
	return sorted(l.valueKeys)
}

// Total is the number of rows sampled for an entity (0 before sampling).
func (l *Live) Total(entity string) int {
	if l == nil {
		return 0
	}
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.totals[entity]
}

// Coverage is the fraction of an entity's rows carrying a label key.
func (l *Live) Coverage(entity, key string) float64 {
	if l == nil {
		return 0
	}
	l.mu.RLock()
	defer l.mu.RUnlock()
	t := l.totals[entity]
	if t == 0 {
		return 0
	}
	return float64(l.labelKeys[entity][key]) / float64(t)
}

func (l *Live) Spaces() []string {
	if l == nil {
		return nil
	}
	l.mu.RLock()
	defer l.mu.RUnlock()
	return append([]string(nil), l.spaces...)
}
