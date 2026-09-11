// Package scout is the read-only, bounded JSON boundary to the Scout executable.
// It deliberately has no Kubernetes client or ConfigHub lookup of its own.
package scout

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"
	"unicode"

	"github.com/confighub/cub-commander/internal/cubclient"
)

type Resource struct {
	APIVersion string `json:"apiVersion"`
	Kind       string `json:"kind"`
	Namespace  string `json:"namespace"`
	Name       string `json:"name"`
}

// Request includes the intended-state row identity as well as the exact live scope.
// No joined row, label, origin annotation, or current kubectl context fills a gap.
type Request struct {
	ResourceID, SpaceID, UnitID, TargetID, Context string
	Resource                                       Resource
}

type Snapshot struct {
	Available             bool
	ObservedAt, ExpiresAt time.Time
	JSON                  string
}

type Loader func(context.Context, Request) (Snapshot, error)

type Config struct {
	Binary   string // empty uses the installed `cub scout` plugin
	Bindings map[string]string
}

func ParseBindings(values []string) (map[string]string, error) {
	bindings := make(map[string]string, len(values))
	for _, value := range values {
		id, ctx, ok := strings.Cut(value, "=")
		if !ok || !validText(id) || !validText(ctx) {
			return nil, errors.New("scout binding requires TARGET_ID=KUBE_CONTEXT with nonempty, control-free values")
		}
		if _, exists := bindings[id]; exists {
			return nil, errors.New("duplicate scout target binding")
		}
		bindings[id] = ctx
	}
	return bindings, nil
}

func validText(s string) bool {
	return s != "" && len(s) <= 512 && strings.TrimSpace(s) == s && !strings.ContainsFunc(s, unicode.IsControl)
}

var kindPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
var namePattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9.-]*[a-z0-9])?$`)
var versionPattern = regexp.MustCompile(`^([a-z0-9][a-z0-9.-]*/)?[a-z][a-z0-9]*$`)

func (r Request) Validate() error {
	for _, id := range []string{r.ResourceID, r.SpaceID, r.UnitID, r.TargetID, r.Context} {
		if !validText(id) {
			return errors.New("resource identity or explicit target binding is missing")
		}
	}
	ref := r.Resource
	if !versionPattern.MatchString(ref.APIVersion) || !kindPattern.MatchString(ref.Kind) ||
		strings.EqualFold(ref.Kind, "Secret") || !namePattern.MatchString(ref.Name) ||
		(ref.Namespace != "" && (!namePattern.MatchString(ref.Namespace) || strings.Contains(ref.Namespace, "."))) {
		return errors.New("unsupported exact resource identity; Secrets and subresources are excluded")
	}
	return nil
}

func Resolve(row cubclient.Row, bindings map[string]string) (Request, error) {
	res, _ := row["Resource"].(map[string]any)
	field := func(key string) string { s, _ := res[key].(string); return s }
	r := Request{ResourceID: field("ResourceID"), SpaceID: field("SpaceID"), UnitID: field("UnitID"), TargetID: field("TargetID")}
	r.Context = bindings[r.TargetID]
	typ, name := field("ResourceType"), field("ResourceName")
	api, kind, ok := strings.Cut(typ, "/")
	if strings.Contains(kind, "/") {
		group, rest, _ := strings.Cut(kind, "/")
		api, kind = api+"/"+group, rest
	}
	ns, obj, hasScope := strings.Cut(name, "/")
	if !ok || !hasScope {
		return r, errors.New("resource requires API version/kind and namespace/name (use /name for cluster scope)")
	}
	r.Resource = Resource{APIVersion: api, Kind: kind, Namespace: ns, Name: obj}
	if validText(r.TargetID) && r.Context == "" {
		return r, errors.New("no explicit kube-context binding for this TargetID; set --scout-binding TARGET_ID=KUBE_CONTEXT")
	}
	return r, r.Validate()
}

func (c Config) Command(r Request) (string, []string) {
	bin, args := c.Binary, []string{}
	if bin == "" {
		bin, args = "cub", []string{"scout"}
	}
	args = append(args, "explain", r.Resource.Kind+"/"+r.Resource.Name, "--bounded",
		"--api-version", r.Resource.APIVersion, "--kube-context", r.Context,
		"--namespace", r.Resource.Namespace, "--format", "json")
	return bin, args
}

// CommandText is a copyable POSIX command; it is never executed by a shell.
func (c Config) CommandText(r Request) string {
	bin, args := c.Command(r)
	words := append([]string{bin}, args...)
	for i, word := range words {
		words[i] = "'" + strings.ReplaceAll(word, "'", "'\"'\"'") + "'"
	}
	return strings.Join(words, " ")
}

func (c Config) Load(ctx context.Context, r Request) (Snapshot, error) {
	if err := r.Validate(); err != nil {
		return Snapshot{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	bin, args := c.Command(r)
	body, err := run(ctx, bin, args)
	if err != nil {
		return Snapshot{}, err
	}
	return Decode(body, r, time.Now())
}

// Decode accepts additive summary fields but displays only the bounded contract.
// A successful exit alone is not evidence: unavailable reads also exit zero.
func Decode(body []byte, req Request, now time.Time) (Snapshot, error) {
	if err := uniqueJSON(body); err != nil {
		return Snapshot{}, errors.New("invalid or ambiguous Scout JSON response")
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil {
		return Snapshot{}, errors.New("invalid Scout JSON response")
	}
	var read struct {
		Available  *bool     `json:"available"`
		Context    string    `json:"context"`
		Resource   Resource  `json:"resource"`
		ObservedAt time.Time `json:"observedAt"`
		ExpiresAt  time.Time `json:"expiresAt"`
		Cache      string    `json:"cache"`
		Reads      struct {
			Discovery *int `json:"discovery"`
			Object    *int `json:"object"`
		} `json:"reads"`
	}
	if err := json.Unmarshal(fields["resourceRead"], &read); err != nil || read.Available == nil {
		return Snapshot{}, errors.New("Scout provider lacks the bounded resourceRead contract; upgrade the provider")
	}
	if read.Context != req.Context || read.Resource != req.Resource {
		return Snapshot{}, errors.New("Scout response scope does not match the selected resource and explicit context")
	}
	d, o := read.Reads.Discovery, read.Reads.Object
	if d == nil || o == nil || *d < 0 || *d > 1 || *o < 0 || *o > 1 || *o > *d ||
		(read.Cache != "miss" && read.Cache != "refresh") {
		return Snapshot{}, errors.New("Scout response violates the single-process bounded read budget")
	}
	if *read.Available {
		if *d != 1 || *o != 1 || read.ObservedAt.IsZero() || !read.ExpiresAt.After(read.ObservedAt) ||
			read.ExpiresAt.Sub(read.ObservedAt) > 15*time.Second || read.ObservedAt.After(now.Add(time.Second)) {
			return Snapshot{}, errors.New("Scout observation has invalid freshness or read evidence")
		}
	} else if !read.ObservedAt.IsZero() || !read.ExpiresAt.IsZero() {
		return Snapshot{}, errors.New("unavailable Scout observation carries misleading freshness")
	}
	shown := make(map[string]json.RawMessage)
	for _, key := range []string{"resourceRead", "configHubOrigin", "omissions", "resource", "namespace", "owner", "source", "deployedVia", "health", "risks", "drift", "notes", "currentChange", "mutationCause", "mutationManager"} {
		if !*read.Available && key != "resourceRead" && key != "omissions" && key != "notes" {
			continue
		}
		if value, ok := fields[key]; ok {
			shown[key] = value
		}
	}
	// Round-trip through values to escape control characters even in arbitrary notes.
	var value any
	b, _ := json.Marshal(shown)
	decoder := json.NewDecoder(strings.NewReader(string(b)))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return Snapshot{}, fmt.Errorf("invalid evidence: %w", err)
	}
	formatted, _ := json.MarshalIndent(value, "", "  ")
	return Snapshot{Available: *read.Available, ObservedAt: read.ObservedAt, ExpiresAt: read.ExpiresAt, JSON: string(formatted)}, nil
}

// Duplicate keys must not make the checked scope differ from the rendered scope.
func uniqueJSON(body []byte) error {
	dec := json.NewDecoder(strings.NewReader(string(body)))
	dec.UseNumber()
	var walk func(int) error
	walk = func(depth int) error {
		if depth > 64 {
			return errors.New("JSON nesting exceeds limit")
		}
		token, err := dec.Token()
		if err != nil {
			return err
		}
		delim, ok := token.(json.Delim)
		if !ok {
			return nil
		}
		if delim != '{' && delim != '[' {
			return errors.New("unexpected delimiter")
		}
		keys := map[string]bool{}
		for dec.More() {
			if delim == '{' {
				key, err := dec.Token()
				if err != nil {
					return err
				}
				s, ok := key.(string)
				s = strings.ToLower(s) // encoding/json accepts case-insensitive struct field names
				if !ok || keys[s] {
					return errors.New("duplicate JSON key")
				}
				keys[s] = true
			}
			if err := walk(depth + 1); err != nil {
				return err
			}
		}
		_, err = dec.Token()
		return err
	}
	if err := walk(0); err != nil {
		return err
	}
	if _, err := dec.Token(); err != io.EOF {
		return errors.New("trailing JSON")
	}
	return nil
}
