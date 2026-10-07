package changeorder

import (
	"fmt"
	"io"
	"strings"

	"gopkg.in/yaml.v3"
)

// Values flattens a YAML stream to document key → path → scalar, keyed the
// way FieldChange.Local names paths, for looking a field up in a unit's
// current data. The server diffs; this only reads one side.

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
// them, the way the server's ResourceInfo names resources.
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

// flatten maps every scalar's path to its value. A sequence item that is a
// mapping with a name is addressed as [name=x], the rest by index.
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

// Values flattens a YAML stream to document key → path → scalar.
func Values(text string) (map[string]map[string]string, error) {
	docs, err := parseDocs(text)
	if err != nil {
		return nil, err
	}
	out := map[string]map[string]string{}
	for _, d := range docs {
		out[d.key] = flatten(d.node)
	}
	return out, nil
}
