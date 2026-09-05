package rollout

import (
	"bytes"
	"fmt"
	"io"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// FieldChange is one path whose value differs between two configurations.
// Before or After is empty when the path exists on one side only.
type FieldChange struct {
	Doc    string // apiVersion/kind name, "" for a document without them
	Path   string // spec.template.spec.containers[name=api].image
	Before string
	After  string
}

func (f FieldChange) String() string {
	p := f.Path
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

// Semantic compares two YAML streams as configuration rather than as text.
// It returns the field changes, and both sides re-encoded canonically (two
// space indent, sequences indented) so a text diff of them shows only what
// changed and not how a tool chose to lay the file out. FormattingOnly is
// true when the texts differ but no field does.
func Semantic(before, after string) (fields []FieldChange, normBefore, normAfter string, formattingOnly bool) {
	a, errA := parseDocs(before)
	b, errB := parseDocs(after)
	if errA != nil || errB != nil {
		// Not YAML (or not parseable): fall back to the text as it is.
		return nil, before, after, false
	}
	normBefore, normAfter = encodeDocs(a), encodeDocs(b)
	fields = diffDocs(a, b)
	formattingOnly = len(fields) == 0 && before != after
	return
}

type doc struct {
	key  string
	node *yaml.Node
}

func parseDocs(text string) ([]doc, error) {
	dec := yaml.NewDecoder(strings.NewReader(text))
	var out []doc
	for {
		var n yaml.Node
		err := dec.Decode(&n)
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		if n.Kind == 0 {
			continue
		}
		out = append(out, doc{key: docKey(&n), node: &n})
	}
	return out, nil
}

// docKey names a document by apiVersion/kind and namespace/name when it has
// them, which is how the two sides are paired.
func docKey(n *yaml.Node) string {
	root := n
	if root.Kind == yaml.DocumentNode && len(root.Content) > 0 {
		root = root.Content[0]
	}
	if root.Kind != yaml.MappingNode {
		return ""
	}
	get := func(m *yaml.Node, k string) *yaml.Node {
		for i := 0; i+1 < len(m.Content); i += 2 {
			if m.Content[i].Value == k {
				return m.Content[i+1]
			}
		}
		return nil
	}
	val := func(m *yaml.Node, k string) string {
		if v := get(m, k); v != nil && v.Kind == yaml.ScalarNode {
			return v.Value
		}
		return ""
	}
	key := val(root, "apiVersion")
	if kind := val(root, "kind"); kind != "" {
		key += "/" + kind
	}
	if meta := get(root, "metadata"); meta != nil && meta.Kind == yaml.MappingNode {
		name := val(meta, "name")
		if ns := val(meta, "namespace"); ns != "" {
			name = ns + "/" + name
		}
		if name != "" {
			key += " " + name
		}
	}
	return key
}

func encodeDocs(docs []doc) string {
	var b bytes.Buffer
	for i, d := range docs {
		if i > 0 {
			b.WriteString("---\n")
		}
		enc := yaml.NewEncoder(&b)
		enc.SetIndent(2)
		if err := enc.Encode(d.node); err != nil {
			b.WriteString("# " + err.Error() + "\n")
		}
		enc.Close()
	}
	return b.String()
}

// diffDocs pairs documents by key (falling back to position for unkeyed
// ones) and diffs the flattened scalars of each pair.
func diffDocs(a, b []doc) []FieldChange {
	var out []FieldChange
	usedB := map[int]bool{}
	pair := func(i int, da doc) (int, bool) {
		if da.key != "" {
			for j, db := range b {
				if !usedB[j] && db.key == da.key {
					return j, true
				}
			}
			return 0, false
		}
		if i < len(b) && !usedB[i] && b[i].key == "" {
			return i, true
		}
		return 0, false
	}
	for i, da := range a {
		j, ok := pair(i, da)
		if !ok {
			for p, v := range flatten(da.node) {
				out = append(out, FieldChange{Doc: da.key, Path: p, Before: v})
			}
			continue
		}
		usedB[j] = true
		fa, fb := flatten(da.node), flatten(b[j].node)
		paths := map[string]bool{}
		for p := range fa {
			paths[p] = true
		}
		for p := range fb {
			paths[p] = true
		}
		for p := range paths {
			if fa[p] != fb[p] {
				out = append(out, FieldChange{Doc: da.key, Path: p, Before: fa[p], After: fb[p]})
			}
		}
	}
	for j, db := range b {
		if usedB[j] {
			continue
		}
		for p, v := range flatten(db.node) {
			out = append(out, FieldChange{Doc: db.key, Path: p, After: v})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Doc != out[j].Doc {
			return out[i].Doc < out[j].Doc
		}
		return out[i].Path < out[j].Path
	})
	return out
}

// flatten maps every scalar's path to its value. A sequence item that is a
// mapping with a name is addressed as [name=x], the way the CLI's associative
// paths do, so a reordered list does not read as every field changing.
func flatten(n *yaml.Node) map[string]string {
	out := map[string]string{}
	var walk func(n *yaml.Node, path string)
	walk = func(n *yaml.Node, path string) {
		switch n.Kind {
		case yaml.DocumentNode:
			for _, c := range n.Content {
				walk(c, path)
			}
		case yaml.MappingNode:
			for i := 0; i+1 < len(n.Content); i += 2 {
				k := n.Content[i].Value
				p := k
				if path != "" {
					p = path + "." + k
				}
				walk(n.Content[i+1], p)
			}
		case yaml.SequenceNode:
			for i, c := range n.Content {
				idx := fmt.Sprintf("[%d]", i)
				if c.Kind == yaml.MappingNode {
					for j := 0; j+1 < len(c.Content); j += 2 {
						if c.Content[j].Value == "name" && c.Content[j+1].Kind == yaml.ScalarNode {
							idx = "[name=" + c.Content[j+1].Value + "]"
						}
					}
				}
				walk(c, path+idx)
			}
		case yaml.ScalarNode:
			out[path] = n.Value
		case yaml.AliasNode:
			if n.Alias != nil {
				walk(n.Alias, path)
			}
		}
	}
	walk(n, "")
	return out
}
