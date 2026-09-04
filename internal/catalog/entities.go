// Package catalog knows what the server exposes: entity types, their REST
// paths, the key they use in an extended row, and their joins. In M1 this is a
// static table; live sampling (labels, values, slugs) arrives in M4.
package catalog

import "strings"

type Entity struct {
	Name        string   // PascalCase as in the API: Unit, Space, BridgeWorker
	CLI         string   // cub command group: unit, space, worker
	OrgPath     string   // org-wide list path under /api
	SpaceScoped bool     // has a /space/{id}/... form
	SpacePath   string   // path segment under /space/{id}/
	Joins       []string // joinable prefixes; a trailing * marks list-valued
	IncludeIDs  []string // include= fields the cub CLI sends by default
	DefaultCols []string
	IDField     string
}

var entities = []Entity{
	{Name: "Unit", CLI: "unit", OrgPath: "/unit", SpaceScoped: true, SpacePath: "unit",
		Joins:       []string{"Organization", "Space", "Target", "ChangeSet", "UpstreamUnit", "UpstreamSpace", "ApprovedBy*", "FromLink*", "BridgeWorker", "HeadRevision", "LastReleasedRevision", "HeadMutation", "UnitEvent"},
		IncludeIDs:  []string{"UnitEventID", "TargetID", "UpstreamUnitID", "SpaceID", "FromLinkID", "BridgeWorkerID", "ChangeSetID"},
		DefaultCols: []string{"Slug", "Space.Slug", "Space.Labels.*", "Labels.*", "Target.Slug", "HeadRevisionNum", "LastReleasedRevisionNum"},
		IDField:     "UnitID"},
	{Name: "Space", CLI: "space", OrgPath: "/space",
		Joins:       []string{"Organization", "TriggerFilter", "Triggers*", "AttributeFilter", "Attributes*", "ReleaseTarget"},
		DefaultCols: []string{"Slug", "Labels.*", "ReleaseTarget.Slug"},
		IDField:     "SpaceID"},
	{Name: "Target", CLI: "target", OrgPath: "/target", SpaceScoped: true, SpacePath: "target",
		Joins:       []string{"Organization", "Space", "BridgeWorker", "TriggerFilter", "Triggers*"},
		IncludeIDs:  []string{"SpaceID", "BridgeWorkerID"},
		DefaultCols: []string{"Slug", "Space.Slug", "Labels.*", "BridgeWorker.Slug", "ProviderType"},
		IDField:     "TargetID"},
	{Name: "Revision", CLI: "revision", OrgPath: "/revision",
		Joins:       []string{"Organization", "Space", "Unit", "User", "ChangeSet", "Tags*", "ChangeOrders*", "Releases*"},
		IncludeIDs:  []string{"SpaceID", "UnitID", "UserID", "ChangeSetID"},
		DefaultCols: []string{"RevisionNum", "Unit.Slug", "Space.Slug", "CreatedAt", "User.Username", "Source", "Description"},
		IDField:     "RevisionID"},
	{Name: "Link", CLI: "link", OrgPath: "/link", SpaceScoped: true, SpacePath: "link",
		Joins:       []string{"Organization", "Space", "FromUnit", "ToUnit", "ToSpace", "TransformInvocation"},
		IncludeIDs:  []string{"SpaceID", "FromUnitID", "ToUnitID", "ToSpaceID"},
		DefaultCols: []string{"Slug", "Space.Slug", "FromUnit.Slug", "ToUnit.Slug", "ToSpace.Slug", "UpdateType", "AutoUpdate"},
		IDField:     "LinkID"},
	{Name: "Filter", CLI: "filter", OrgPath: "/filter", SpaceScoped: true, SpacePath: "filter",
		Joins:       []string{"Organization", "Space", "FromSpace"},
		IncludeIDs:  []string{"SpaceID", "FromSpaceID"},
		DefaultCols: []string{"Slug", "Space.Slug", "From", "Where", "WhereData", "ResourceType"},
		IDField:     "FilterID"},
	{Name: "View", CLI: "view", OrgPath: "/view", SpaceScoped: true, SpacePath: "view",
		Joins:       []string{"Organization", "Space", "Filter"},
		IncludeIDs:  []string{"SpaceID", "FilterID"},
		DefaultCols: []string{"Slug", "Space.Slug", "Of", "Filter.Slug", "GroupBy", "OrderBy"},
		IDField:     "ViewID"},
	{Name: "Tag", CLI: "tag", OrgPath: "/tag", SpaceScoped: true, SpacePath: "tag",
		Joins:       []string{"Organization", "Space", "ChangeSet"},
		IncludeIDs:  []string{"SpaceID", "ChangeSetID"},
		DefaultCols: []string{"Slug", "Space.Slug", "ChangeSet.Slug", "CreatedAt"},
		IDField:     "TagID"},
	{Name: "Trigger", CLI: "trigger", OrgPath: "/trigger", SpaceScoped: true, SpacePath: "trigger",
		Joins:       []string{"Organization", "Space", "BridgeWorker", "Invocation", "UnitFilter"},
		IncludeIDs:  []string{"SpaceID", "BridgeWorkerID", "InvocationID"},
		DefaultCols: []string{"Slug", "Space.Slug", "Event", "ToolchainType", "FunctionName", "Disabled"},
		IDField:     "TriggerID"},
	{Name: "Invocation", CLI: "invocation", OrgPath: "/invocation", SpaceScoped: true, SpacePath: "invocation",
		Joins:       []string{"Organization", "Space", "BridgeWorker"},
		IncludeIDs:  []string{"SpaceID"},
		DefaultCols: []string{"Slug", "Space.Slug", "ToolchainType"},
		IDField:     "InvocationID"},
	{Name: "ChangeSet", CLI: "changeset", OrgPath: "/change_set", SpaceScoped: true, SpacePath: "change_set",
		Joins:       []string{"Organization", "Space", "StartTag", "EndTag"},
		IncludeIDs:  []string{"SpaceID", "StartTagID", "EndTagID"},
		DefaultCols: []string{"Slug", "Space.Slug", "State", "StartTag.Slug", "EndTag.Slug"},
		IDField:     "ChangeSetID"},
	{Name: "ChangeOrder", CLI: "changeorder", OrgPath: "/change_order", SpaceScoped: true, SpacePath: "change_order",
		Joins:       []string{"Organization", "Space", "StartTag", "EndTag", "RestoreTag"},
		IncludeIDs:  []string{"SpaceID"},
		DefaultCols: []string{"Slug", "Space.Slug", "State"},
		IDField:     "ChangeOrderID"},
	{Name: "Release", CLI: "release", OrgPath: "/release", SpaceScoped: true, SpacePath: "release",
		Joins:       []string{"Organization", "Space", "Tag"},
		IncludeIDs:  []string{"SpaceID", "TagID"},
		DefaultCols: []string{"ReleaseNum", "Space.Slug", "Tag.Slug", "UnitCount", "Published", "CreatedAt"},
		IDField:     "ReleaseID"},
	{Name: "BridgeWorker", CLI: "worker", OrgPath: "/bridge_worker", SpaceScoped: true, SpacePath: "bridge_worker",
		Joins:       []string{"Organization", "Space"},
		IncludeIDs:  []string{"SpaceID"},
		DefaultCols: []string{"Slug", "Space.Slug", "Condition", "LastSeenAt"},
		IDField:     "BridgeWorkerID"},
	{Name: "Attribute", CLI: "attribute", OrgPath: "/attribute", SpaceScoped: true, SpacePath: "attribute",
		Joins:       []string{"Organization", "Space"},
		IncludeIDs:  []string{"SpaceID"},
		DefaultCols: []string{"Slug", "Space.Slug", "ToolchainType", "DataType"},
		IDField:     "AttributeID"},
	{Name: "UnitEvent", CLI: "unit-event", OrgPath: "/unit_event",
		Joins:       []string{"Organization", "Space", "Unit"},
		IncludeIDs:  []string{"SpaceID", "UnitID"},
		DefaultCols: []string{"UnitEventNum", "Unit.Slug", "Space.Slug", "Action", "Status", "Result", "CreatedAt"},
		IDField:     "UnitEventID"},
	{Name: "UnitAction", CLI: "unit-action", OrgPath: "/unit_action",
		Joins:       []string{"Organization", "Space", "Unit"},
		IncludeIDs:  []string{"SpaceID", "UnitID"},
		DefaultCols: []string{"UnitActionNum", "Unit.Slug", "Space.Slug", "Action", "Status", "CreatedAt"},
		IDField:     "UnitActionID"},
	{Name: "User", CLI: "user", OrgPath: "/user",
		Joins:       []string{"Organization"},
		DefaultCols: []string{"Username", "DisplayName", "Email"},
		IDField:     "UserID"},
	{Name: "Organization", CLI: "organization", OrgPath: "/organization",
		DefaultCols: []string{"Slug", "DisplayName", "EmailDomain"},
		IDField:     "OrganizationID"},
	{Name: "Resource", CLI: "resource", OrgPath: "/resource",
		Joins:       []string{"Organization", "Space", "Unit", "Target"},
		IncludeIDs:  []string{"SpaceID", "UnitID", "TargetID"},
		DefaultCols: []string{"ResourceName", "ResourceType", "Unit.Slug", "Space.Slug"},
		IDField:     "ResourceID"},
}

var aliases = map[string]string{"worker": "BridgeWorker", "workers": "BridgeWorker", "units": "Unit", "spaces": "Space", "targets": "Target", "revisions": "Revision", "links": "Link", "filters": "Filter", "views": "View", "tags": "Tag", "triggers": "Trigger", "changesets": "ChangeSet", "releases": "Release", "users": "User"}

// Lookup finds an entity by name, case-insensitively, accepting a few plurals and aliases.
func Lookup(name string) (Entity, bool) {
	if a, ok := aliases[strings.ToLower(name)]; ok {
		name = a
	}
	for _, e := range entities {
		if strings.EqualFold(e.Name, name) || strings.EqualFold(e.CLI, name) {
			return e, true
		}
	}
	return Entity{}, false
}

func All() []Entity { return entities }

// JoinNames returns the join prefixes without the list marker.
func (e Entity) JoinNames() []string {
	out := make([]string, 0, len(e.Joins))
	for _, j := range e.Joins {
		out = append(out, strings.TrimSuffix(j, "*"))
	}
	return out
}

// IsJoin reports whether the first segment of a path is a join prefix on this entity.
func (e Entity) IsJoin(first string) bool {
	if strings.EqualFold(first, e.Name) {
		return false
	}
	for _, j := range e.JoinNames() {
		if j == first {
			return true
		}
	}
	return false
}

// mapFields are map-valued attributes; a path under them is a key, not a subfield.
var mapFields = map[string]bool{"Labels": true, "Annotations": true, "Values": true, "ApplyGates": true, "DeleteGates": true, "DestroyGates": true, "Tags": true, "Releases": true, "Facts": true, "Permissions": true, "Data": true}

func IsMapField(f string) bool { return mapFields[f] }
