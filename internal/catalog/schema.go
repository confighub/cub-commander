package catalog

import (
	"regexp"
	"sort"
	"strings"

	"github.com/confighub/sdk/core/openapi"
)

// Attr describes one attribute of an entity as the OpenAPI spec and the
// server's where grammar see it.
type Attr struct {
	Name       string
	Type       string // string, uuid, time, integer, boolean, map, array, object
	Enum       []string
	Desc       string
	Filterable bool
}

// Attributes returns the attributes of an entity, sorted by name.
func Attributes(entity string) []Attr {
	e, ok := Lookup(entity)
	if !ok {
		return nil
	}
	schema, err := openapi.LookupSchema(e.Name)
	if err != nil || schema == nil {
		return nil
	}
	filt := map[string]bool{}
	for _, f := range filterable[e.Name] {
		filt[f] = true
	}
	var out []Attr
	for _, name := range schema.PropertyNames() {
		p := schema.Properties[name]
		a := Attr{Name: name, Desc: firstSentence(p.Description), Filterable: filt[name]}
		a.Type = typeOf(p, name)
		if p.Ref != "" {
			if rs, err := openapi.LookupSchema(p.RefName()); err == nil && rs != nil && len(rs.Enum) > 0 {
				a.Enum = rs.Enum
				a.Type = "string"
			}
		} else if len(p.Enum) > 0 {
			a.Enum = p.Enum
		} else if vals := prosEnum(p.Description); len(vals) > 0 {
			a.Enum = vals
		}
		out = append(out, a)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Attribute looks one up.
func Attribute(entity, name string) (Attr, bool) {
	for _, a := range Attributes(entity) {
		if a.Name == name {
			return a, true
		}
	}
	return Attr{}, false
}

func typeOf(p *openapi.Schema, name string) string {
	switch {
	case p == nil:
		return "?"
	case p.Format == "uuid" || strings.HasSuffix(name, "ID"):
		return "uuid"
	case p.Format == "date-time" || strings.HasSuffix(name, "At"):
		return "time"
	case p.Type == "object" && IsMapField(name):
		return "map"
	case p.Type == "object", p.Ref != "":
		return "object"
	case p.Type == "":
		return "?"
	}
	return p.Type
}

var validValuesRe = regexp.MustCompile(`Valid values are ([^.]+)\.`)

// prosEnum recovers an enumeration the spec states only in prose:
// "Valid values are A, B, and C."
func prosEnum(desc string) []string {
	m := validValuesRe.FindStringSubmatch(desc)
	if m == nil {
		return nil
	}
	raw := strings.ReplaceAll(m[1], " and ", ", ")
	var out []string
	for _, v := range strings.Split(raw, ",") {
		v = strings.TrimSpace(v)
		if v != "" && !strings.Contains(v, " ") {
			out = append(out, v)
		}
	}
	return out
}

func firstSentence(s string) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\n", " "))
	if i := strings.Index(s, ". "); i > 0 {
		return s[:i+1]
	}
	return s
}

// Operators valid for an attribute type, per the server grammar.
func Operators(t string) []string {
	switch t {
	case "string", "time":
		return []string{"=", "!=", "<", ">", "<=", ">=", "LIKE", "NOT LIKE", "ILIKE", "~", "~*", "!~", "!~*", "IN (", "NOT IN (", "IS NULL", "IS NOT NULL"}
	case "integer", "number":
		return []string{"=", "!=", "<", ">", "<=", ">=", "IN (", "NOT IN (", "IS NULL", "IS NOT NULL"}
	case "uuid":
		return []string{"=", "!=", "IS NULL", "IS NOT NULL"}
	case "boolean":
		return []string{"=", "!=", "IS TRUE", "IS FALSE"}
	case "array":
		return []string{"?"}
	case "map":
		return []string{".", "?", "IS NULL", "IS NOT NULL"}
	}
	return []string{"=", "!="}
}

// JoinEntity maps a join prefix to the entity it reaches.
func JoinEntity(prefix string) string {
	prefix = strings.TrimSuffix(prefix, "*")
	switch prefix {
	case "Space", "UpstreamSpace", "ToSpace", "FromSpace":
		return "Space"
	case "Unit", "UpstreamUnit", "FromUnit", "ToUnit":
		return "Unit"
	case "Target", "ReleaseTarget":
		return "Target"
	case "HeadRevision", "LastReleasedRevision", "Revision":
		return "Revision"
	case "HeadMutation":
		return "Mutation"
	case "Filter", "TriggerFilter", "AttributeFilter", "UnitFilter":
		return "Filter"
	case "Tag", "Tags", "StartTag", "EndTag", "RestoreTag":
		return "Tag"
	case "Invocation", "TransformInvocation":
		return "Invocation"
	case "ApprovedBy", "User":
		return "User"
	case "FromLink":
		return "Link"
	case "Triggers":
		return "Trigger"
	case "Attributes":
		return "Attribute"
	case "Releases":
		return "Release"
	case "ChangeOrders":
		return "ChangeOrder"
	}
	if _, ok := Lookup(prefix); ok {
		return prefix
	}
	return ""
}
