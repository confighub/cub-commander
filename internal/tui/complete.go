package tui

import (
	"fmt"
	"sort"
	"strings"

	"github.com/confighub/cub-commander/internal/catalog"
	"github.com/confighub/cub-commander/internal/lang"
)

// Candidate is one completion.
type Candidate struct {
	Text   string // inserted in place of the partial segment
	Detail string
	Space  bool // append a space after inserting
	Quote  bool // close a string literal after inserting
}

// Completer produces context-aware candidates for the text before the cursor.
type Completer struct {
	Live *catalog.Live
}

var stmtKeywords = []string{"EXPLAIN", "USE", "SHOW", "DESCRIBE", "SELECT"}
var stepKeywords = []string{"where", "columns", "browse by", "in", "group by", "order by", "limit"}
var showWhat = []string{"ENTITIES", "COLUMNS FROM", "JOINS FROM", "LABELS FROM", "VALUES OF", "SPACES"}

// Complete returns the offset of the segment being completed and the candidates.
func (c *Completer) Complete(text string) (int, []Candidate) {
	// The partial word: identifier characters back from the cursor, or the
	// body of an unterminated string literal.
	inString := strings.Count(text, "'")%2 == 1
	start := len(text)
	if inString {
		start = strings.LastIndex(text, "'") + 1
	} else {
		for start > 0 && isWordByte(text[start-1]) {
			start--
		}
	}
	word := text[start:]
	head := text[:start]
	if inString {
		head = text[:start-1] // drop the opening quote so the head lexes
	}

	// Tokens of the current statement before the word.
	if i := strings.LastIndex(head, ";"); i >= 0 {
		head = head[i+1:]
	}
	toks, err := lang.Lex(head)
	if err != nil {
		return start, nil
	}
	toks = toks[:len(toks)-1] // drop EOF
	entity := statementEntity(toks)
	pipeline := len(toks) > 0 && !toks[0].Is("SELECT") && !toks[0].Is("EXPLAIN") || (len(toks) > 1 && toks[0].Is("EXPLAIN") && !toks[1].Is("SELECT"))
	assumed := false
	if entity == "" && len(toks) > 0 && toks[0].Is("SELECT") {
		entity, assumed = "Unit", true // select list before FROM: assume the common case
	}
	prev := func(n int) lang.Token {
		if len(toks) < n {
			return lang.Token{}
		}
		return toks[len(toks)-n]
	}
	p1, p2 := prev(1), prev(2)

	var cands []Candidate
	add := func(text, detail string, space bool) {
		cands = append(cands, Candidate{Text: text, Detail: detail, Space: space})
	}

	switch {
	case inString:
		// A value for the attribute two tokens back: Labels.K = '…, Labels.K IN ('…
		ref := p2
		if p1.Is("(") || p1.Is(",") {
			// inside an IN list: walk back to the IN and its attribute
			for i := len(toks) - 1; i >= 0; i-- {
				if toks[i].Is("IN") && i > 0 {
					ref = toks[i-1]
					break
				}
			}
		}
		for _, v := range c.valuesFor(entity, ref.Text) {
			cands = append(cands, Candidate{Text: v.Text, Detail: v.Detail, Quote: true})
		}
		start, cands = filterCands(start, word, cands)
		return start, cands

	case len(toks) == 0 || (len(toks) == 1 && toks[0].Is("EXPLAIN")):
		for _, e := range catalog.All() {
			add(e.Name, e.CLI+" list", true)
		}
		for _, k := range stmtKeywords {
			add(k, "statement", true)
		}

	case p1.Is("|"):
		for _, k := range stepKeywords {
			add(k, "step", true)
		}

	case p1.Is("SHOW"):
		for _, k := range showWhat {
			add(k, "", true)
		}
	case p1.Is("FROM") && len(toks) >= 2 && toks[0].Is("SHOW"):
		for _, e := range catalog.All() {
			add(e.Name, "entity", false)
		}
	case p1.Is("OF") && len(toks) >= 2 && toks[0].Is("SHOW"):
		add("Labels.", "label key", false)
		for _, k := range c.Live.LabelKeys("Unit") {
			add("Labels."+k.Name, fmt.Sprintf("%d units", k.N), false)
		}

	case p1.Is("FROM"):
		for _, e := range catalog.All() {
			add(e.Name, e.CLI+" list", true)
		}

	case p1.Is("USE"), p1.Is("IN") && entity != "" && (p2.Is("|") || (p2.Kind == lang.IDENT && strings.EqualFold(p2.Text, entity))):
		add("*", "whole org", true)
		for _, s := range c.Live.Spaces() {
			add(s, "space", true)
		}

	case pipeline && len(toks) == 1 && p1.Kind == lang.IDENT && entity != "":
		for _, k := range stepKeywords {
			add("| "+k, "step", true)
		}
	case pipeline && (p1.Is("*") || p1.Kind == lang.IDENT) && p2.Is("IN"):
		for _, k := range stepKeywords {
			if k != "in" {
				add("| "+k, "step", true)
			}
		}
	case p1.Kind == lang.IDENT && p2.Is("FROM"), (p1.Is("*") || p1.Kind == lang.IDENT) && p2.Is("IN") && hasToken(toks, "FROM") && !hasToken(toks, "WHERE"):
		if p2.Is("FROM") {
			add("IN", "scope: a space or *", true)
		}
		for _, k := range clauseKeywordsAfter(toks) {
			add(k, "", true)
		}

	case p1.Is("ORDER") || p1.Is("GROUP") || p1.Is("BROWSE"):
		add("BY", "", true)
	case p1.Is("IS"):
		for _, k := range []string{"NULL", "NOT NULL", "TRUE", "FALSE", "NOT TRUE", "NOT FALSE"} {
			add(k, "", true)
		}
	case p1.Is("NOT") && p2.Kind == lang.IDENT && isAttrToken(entity, p2.Text):
		add("IN (", "", false)
		add("LIKE", "", true)

	case p1.Kind == lang.OP || p1.Is("LIKE") || p1.Is("ILIKE"):
		for _, v := range c.valuesFor(entity, p2.Text) {
			cands = append(cands, Candidate{Text: "'" + v.Text + "'", Detail: v.Detail, Space: true})
		}
		if a, ok := c.attrOf(entity, p2.Text); ok && a.Type == "boolean" {
			add("true", "", true)
			add("false", "", true)
		}
		if len(cands) == 0 {
			add("'", "string literal", false)
		}
	case p1.Is("IN") || (p1.Is("(") && p2.Is("IN")):
		if p1.Is("IN") {
			add("(", "value list", false)
		}
		for _, v := range c.valuesFor(entity, p2.Text) {
			cands = append(cands, Candidate{Text: "'" + v.Text + "'", Detail: v.Detail})
		}

	case p1.Kind == lang.IDENT && isAttrToken(entity, p1.Text) && isWhereish(p2):
		// After an attribute in a predicate: operators for its type.
		t := "string"
		if a, ok := c.attrOf(entity, p1.Text); ok {
			t = a.Type
		}
		if strings.Contains(p1.Text, "Labels.") || strings.Contains(p1.Text, "Annotations.") || strings.Contains(p1.Text, "Values.") {
			t = "string"
		}
		for _, op := range catalog.Operators(t) {
			if op != "." {
				add(op, t, !strings.HasSuffix(op, "("))
			}
		}

	case p1.Kind == lang.STRING || p1.Kind == lang.NUMBER || p1.Is("TRUE") || p1.Is("FALSE") || p1.Is(")") || p1.Is("NULL") || p1.Is("DESC") || p1.Is("ASC") || (p1.Kind == lang.IDENT && isAttrToken(entity, p1.Text) && !isWhereish(p2)):
		// After a complete term or a column.
		inColumns := lastStep(toks) == "columns" || lastStep(toks) == "select"
		switch {
		case pipeline && inColumns:
			add(",", "next column", true)
			add("as", "alias", true)
			for _, k := range stepKeywords {
				if k != "in" && k != "columns" {
					add("| "+k, "step", true)
				}
			}
		case pipeline:
			if lastStep(toks) == "where" {
				add("AND", "", true)
				add("OR", "runs locally", true)
			}
			if lastStep(toks) == "order" {
				add("desc", "", true)
				add(",", "", true)
			}
			for _, k := range stepKeywords {
				if k != "in" {
					add("| "+k, "step", true)
				}
			}
		case !hasToken(toks, "FROM"):
			add("FROM", "", true)
			add("AS", "alias", true)
			add(",", "next column", true)
		default:
			for _, k := range clauseKeywordsAfter(toks) {
				add(k, "", true)
			}
		}

	default:
		// Attribute position: SELECT …, WHERE …, AND …, HAVING …, BY …, ( …
		if entity == "" {
			break
		}
		if assumed && p1.Is("SELECT") {
			add("*", "default columns", true)
		}
		cands = append(cands, c.attrCandidates(entity, word)...)
		if !strings.Contains(word, ".") {
			add("LEN(", "length of an array or map", false)
			if !isWhereish(p1) && p1.Kind != lang.PUNCT {
				// nothing else
			}
		}
		start, cands = filterSegment(start, word, cands)
		return start, cands
	}
	return filterCands(start, word, cands)
}

func isWordByte(c byte) bool {
	return c == '_' || c == '.' || c == '-' || c == '/' || c == '*' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
}

// lastStep names the most recent step keyword in a pipeline (where, columns,
// select, in, group, order, limit) or "".
func lastStep(toks []lang.Token) string {
	for i := len(toks) - 1; i >= 0; i-- {
		for _, k := range []string{"where", "columns", "select", "in", "browse", "group", "order", "limit", "having"} {
			if toks[i].Is(k) {
				return k
			}
		}
	}
	return ""
}

func hasToken(toks []lang.Token, kw string) bool {
	for _, t := range toks {
		if t.Is(kw) {
			return true
		}
	}
	return false
}

func isWhereish(t lang.Token) bool {
	return t.Is("WHERE") || t.Is("AND") || t.Is("OR") || t.Is("NOT") || t.Is("HAVING") || t.Is("(")
}

// isColumnsish: a token after which an attribute list begins.
func isColumnsish(t lang.Token) bool {
	return t.Is("columns") || t.Is("SELECT") || t.Is(",") || t.Is("BY")
}

func clauseKeywordsAfter(toks []lang.Token) []string {
	var out []string
	seen := map[string]bool{}
	for _, t := range toks {
		for _, k := range []string{"WHERE", "HAVING", "GROUP", "ORDER", "LIMIT"} {
			if t.Is(k) {
				seen[k] = true
			}
		}
	}
	if seen["WHERE"] || seen["HAVING"] {
		out = append(out, "AND")
	}
	if seen["HAVING"] {
		out = append(out, "OR")
	}
	for _, k := range []string{"WHERE", "HAVING", "GROUP BY", "ORDER BY", "LIMIT"} {
		key := strings.Fields(k)[0]
		if !seen[key] {
			out = append(out, k)
		}
	}
	if seen["ORDER"] {
		out = append(out, "DESC", "ASC")
	}
	return out
}

// statementEntity finds the entity a statement is about: after FROM, or the
// shorthand leading entity name.
func statementEntity(toks []lang.Token) string {
	for i, t := range toks {
		if t.Is("FROM") && i+1 < len(toks) && toks[i+1].Kind == lang.IDENT {
			if e, ok := catalog.Lookup(toks[i+1].Text); ok {
				return e.Name
			}
			return ""
		}
	}
	if len(toks) > 0 && toks[0].Kind == lang.IDENT {
		if e, ok := catalog.Lookup(toks[0].Text); ok {
			return e.Name
		}
	}
	return ""
}

func isAttrToken(entity, text string) bool {
	if entity == "" || text == "" {
		return false
	}
	if strings.EqualFold(text, entity) {
		return false
	}
	first := strings.Split(text, ".")[0]
	if _, ok := catalog.Attribute(entity, first); ok {
		return true
	}
	if e, ok := catalog.Lookup(entity); ok && e.IsJoin(strings.TrimSuffix(first, "*")) {
		return true
	}
	return strings.HasPrefix(text, "LEN(")
}

// attrOf resolves a dotted path to its attribute, following one join.
func (c *Completer) attrOf(entity, path string) (catalog.Attr, bool) {
	segs := strings.Split(strings.TrimPrefix(path, "LEN("), ".")
	ent := entity
	if e, ok := catalog.Lookup(entity); ok && len(segs) > 1 && e.IsJoin(strings.TrimSuffix(segs[0], "*")) {
		ent = catalog.JoinEntity(segs[0])
		segs = segs[1:]
		if len(segs) > 0 && segs[0] == "*" {
			segs = segs[1:]
		}
	}
	if len(segs) == 0 {
		return catalog.Attr{}, false
	}
	return catalog.Attribute(ent, segs[0])
}

// valuesFor lists sampled values for a label path such as Labels.Env or Space.Labels.Env,
// or the enum values of an attribute.
func (c *Completer) valuesFor(entity, path string) []Candidate {
	segs := strings.Split(path, ".")
	ent := entity
	if e, ok := catalog.Lookup(entity); ok && len(segs) > 2 && e.IsJoin(segs[0]) {
		ent = catalog.JoinEntity(segs[0])
		segs = segs[1:]
	}
	var out []Candidate
	if len(segs) == 2 && segs[0] == "Labels" {
		for _, v := range c.Live.LabelValues(ent, segs[1]) {
			out = append(out, Candidate{Text: v.Text(), Detail: fmt.Sprintf("%d", v.N)})
		}
		return out
	}
	if a, ok := catalog.Attribute(ent, segs[0]); ok && len(a.Enum) > 0 {
		for _, e := range a.Enum {
			out = append(out, Candidate{Text: e, Detail: "enum"})
		}
	}
	return out
}

// attrCandidates completes a dotted attribute path against the entity.
func (c *Completer) attrCandidates(entity, word string) []Candidate {
	var out []Candidate
	e, ok := catalog.Lookup(entity)
	if !ok {
		return nil
	}
	segs := strings.Split(word, ".")
	prefix := segs[:len(segs)-1]
	ent := entity
	// Follow joins.
	for len(prefix) > 0 {
		p := prefix[0]
		if p == "*" {
			prefix = prefix[1:]
			continue
		}
		if catalog.IsMapField(p) {
			mapEnt := ent
			switch p {
			case "Labels", "Annotations":
				for _, k := range c.Live.LabelKeys(mapEnt) {
					out = append(out, Candidate{Text: k.Name, Detail: fmt.Sprintf("%d", k.N)})
				}
			case "Values":
				for _, k := range c.Live.ValueKeys() {
					out = append(out, Candidate{Text: k.Name, Detail: fmt.Sprintf("%d", k.N)})
				}
			}
			return out
		}
		if ee, ok := catalog.Lookup(ent); ok && ee.IsJoin(p) {
			ent = catalog.JoinEntity(p)
			e, _ = catalog.Lookup(ent)
			prefix = prefix[1:]
			continue
		}
		return nil
	}
	for _, a := range catalog.Attributes(ent) {
		d := a.Type
		if a.Filterable {
			d += " · filterable"
		}
		text := a.Name
		if a.Type == "map" {
			text += "."
		}
		out = append(out, Candidate{Text: text, Detail: d})
	}
	if e.Name == ent { // joins only from the root entity (one level)
		for _, j := range e.Joins {
			name := strings.TrimSuffix(j, "*")
			text := name + "."
			d := "join → " + catalog.JoinEntity(name)
			if strings.HasSuffix(j, "*") {
				text = name + ".*."
				d += " (list)"
			}
			out = append(out, Candidate{Text: text, Detail: d})
		}
	}
	return out
}

// filterSegment keeps candidates matching the last dotted segment of word and
// moves start to that segment.
func filterSegment(start int, word string, cands []Candidate) (int, []Candidate) {
	seg := word
	if i := strings.LastIndex(word, "."); i >= 0 {
		seg = word[i+1:]
		start += i + 1
	}
	return filterCands(start, seg, cands)
}

func filterCands(start int, partial string, cands []Candidate) (int, []Candidate) {
	low := strings.ToLower(partial)
	var out []Candidate
	seen := map[string]bool{}
	for _, c := range cands {
		if seen[c.Text] {
			continue
		}
		if strings.HasPrefix(strings.ToLower(c.Text), low) {
			seen[c.Text] = true
			out = append(out, c)
		}
	}
	// Then substring matches, for the "I know part of the name" case.
	if low != "" {
		for _, c := range cands {
			if !seen[c.Text] && strings.Contains(strings.ToLower(c.Text), low) {
				seen[c.Text] = true
				out = append(out, c)
			}
		}
	}
	return start, out
}

// commonPrefix of candidate texts, case-insensitive on comparison but
// returning the first candidate's casing.
func commonPrefix(cands []Candidate) string {
	if len(cands) == 0 {
		return ""
	}
	p := cands[0].Text
	for _, c := range cands[1:] {
		for !strings.HasPrefix(strings.ToLower(c.Text), strings.ToLower(p)) {
			p = p[:len(p)-1]
			if p == "" {
				return ""
			}
		}
	}
	return p
}

func sortedNames(m map[string]int) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
