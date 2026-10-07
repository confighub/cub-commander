package changeorder

import (
	"fmt"
	"sort"
	"strings"
)

// FieldChange is one path whose value differs between two configurations,
// as the server's ConfigDiff reports it: both values, in document order,
// matched by merge key rather than by position.
type FieldChange struct {
	Doc      string // "apps/v1/Deployment cert-manager/cert-manager"; "" when the resource has no identity
	Path     string // the display path: spec.template.spec.containers.?name=api.image
	Resolved string // the path as MutationSources record it (anchors included)
	Local    string // the path as Values keys it: spec.template.spec.containers[name=api].image
	Kind     string // Add, Delete, Update, Replace, Reorder, Rename; for a whole resource, Add or Delete with an empty Path
	Before   string
	After    string
	Patch    string // a unified diff, for multi-line string values
}

func (f FieldChange) String() string {
	p := f.Path
	if p == "" {
		p = "(resource)"
	}
	if f.Doc != "" {
		p = f.Doc + " " + p
	}
	switch {
	case f.Before == "":
		return "+ " + p + ": " + f.After
	case f.After == "":
		return "- " + p + ": " + f.Before
	}
	return p + ": " + f.Before + " → " + f.After
}

// Multiline says a side needs a block rather than a line.
func (f FieldChange) Multiline() bool {
	return strings.Contains(f.Before, "\n") || strings.Contains(f.After, "\n")
}

// ParseConfigDiff reads a ConfigDiff (the Diff of a unit_diff row or of a
// promote unit result) into field changes, in the server's order.
func ParseConfigDiff(v any) []FieldChange {
	m, _ := v.(map[string]any)
	if m == nil {
		return nil
	}
	var out []FieldChange
	for _, res := range list(m["Resources"]) {
		doc := docName(res["Resource"])
		kind := str(res["ChangeType"])
		if kind == "Add" || kind == "Delete" {
			out = append(out, FieldChange{Doc: doc, Kind: kind, Before: str(res["FromValue"]), After: str(res["ToValue"])})
			continue
		}
		for _, ch := range list(res["Changes"]) {
			out = append(out, FieldChange{
				Doc:      doc,
				Path:     str(ch["DisplayPath"]),
				Resolved: str(ch["Path"]),
				Local:    localPath(ch["Segments"]),
				Kind:     str(ch["ChangeType"]),
				Before:   str(ch["FromValue"]),
				After:    str(ch["ToValue"]),
				Patch:    str(ch["Patch"]),
			})
		}
	}
	return out
}

// docName renders a ResourceInfo the way Values keys documents:
// "apiVersion/Kind namespace/name", a cluster-scoped name without the slash.
func docName(v any) string {
	m, _ := v.(map[string]any)
	if m == nil {
		return ""
	}
	t, n := str(m["ResourceType"]), strings.TrimPrefix(str(m["ResourceName"]), "/")
	switch {
	case t == "" && n == "":
		return ""
	case n == "":
		return t
	}
	return t + " " + n
}

// localPath renders a change's segments the way flatten keys scalars: a
// merge-keyed element by its name, any other element by its index.
func localPath(v any) string {
	var b strings.Builder
	for _, seg := range list(v) {
		if f := str(seg["Field"]); f != "" {
			if b.Len() > 0 {
				b.WriteByte('.')
			}
			b.WriteString(f)
			continue
		}
		idx := num(seg["ToIndex"])
		if idx < 0 {
			idx = num(seg["FromIndex"])
		}
		named := ""
		for _, mk := range list(seg["MergeKeys"]) {
			if str(mk["Key"]) == "name" {
				named = str(mk["Value"])
			}
		}
		if named != "" {
			fmt.Fprintf(&b, "[name=%s]", named)
		} else {
			fmt.Fprintf(&b, "[%d]", idx)
		}
	}
	return b.String()
}

// Conflict is a path a write withheld: a protected or guarded value the
// merge did not overwrite, as a promote unit result reports it.
type Conflict struct {
	Doc, Path, Reason, Details string
}

// ParseConflicts reads a MutationConflictList.
func ParseConflicts(v any) []Conflict {
	var out []Conflict
	for _, c := range list(v) {
		out = append(out, Conflict{Doc: docName(c["Resource"]), Path: str(c["Path"]), Reason: str(c["Reason"]), Details: str(c["Details"])})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Doc+out[i].Path < out[j].Doc+out[j].Path })
	return out
}
