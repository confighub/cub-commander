package exec

import (
	"fmt"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// A unit's data is a YAML stream; a resource is one document in it. Docs
// splits the stream keeping every byte, so a document can be edited and the
// stream reassembled without touching the others.

type Doc struct {
	Text string // the document's text, exactly as in the stream
	Type string // apiVersion/kind
	Name string // namespace/name ("" namespace for cluster-scoped)
}

// Stream is a split unit data: documents and the separator lines between them.
type Stream struct {
	Docs []Doc
	seps []string // seps[i] precedes Docs[i]; "" when there is none
}

var sepLine = regexp.MustCompile(`(?m)^---[ \t]*\r?\n?`)

// SplitDocs splits YAML text into documents on `---` lines.
func SplitDocs(text string) *Stream {
	s := &Stream{}
	locs := sepLine.FindAllStringIndex(text, -1)
	prev := 0
	sep := ""
	for _, loc := range locs {
		if loc[0] > prev || sep != "" || len(s.Docs) > 0 {
			s.add(sep, text[prev:loc[0]])
		} else if loc[0] == prev && len(s.Docs) == 0 {
			// leading separator: no document before it
		}
		sep = text[loc[0]:loc[1]]
		prev = loc[1]
	}
	s.add(sep, text[prev:])
	return s
}

func (s *Stream) add(sep, body string) {
	if strings.TrimSpace(body) == "" && sep == "" {
		return
	}
	d := Doc{Text: body}
	var head struct {
		APIVersion string `yaml:"apiVersion"`
		Kind       string `yaml:"kind"`
		Metadata   struct {
			Name      string `yaml:"name"`
			Namespace string `yaml:"namespace"`
		} `yaml:"metadata"`
	}
	if yaml.Unmarshal([]byte(body), &head) == nil && head.Kind != "" {
		d.Type = head.APIVersion + "/" + head.Kind
		d.Name = head.Metadata.Namespace + "/" + head.Metadata.Name
	}
	s.Docs = append(s.Docs, d)
	s.seps = append(s.seps, sep)
}

// Join reassembles the stream.
func (s *Stream) Join() string {
	var b strings.Builder
	for i, d := range s.Docs {
		b.WriteString(s.seps[i])
		b.WriteString(d.Text)
	}
	return b.String()
}

// Find returns the index of the document with the given type and name.
func (s *Stream) Find(resType, resName string) (int, error) {
	for i, d := range s.Docs {
		if d.Type == resType && d.Name == resName {
			return i, nil
		}
	}
	return -1, fmt.Errorf("no document %s %s in the unit's data (%d documents)", resType, resName, len(s.Docs))
}

// Replace returns the stream text with document i replaced.
func (s *Stream) Replace(i int, text string) string {
	docs := append([]Doc(nil), s.Docs...)
	docs[i].Text = text
	t := &Stream{Docs: docs, seps: s.seps}
	return t.Join()
}
