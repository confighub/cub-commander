package tui

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/confighub/sdk/cubutil/blame"

	"github.com/confighub/cub-commander/internal/cubclient"
)

// The Blame tab of a unit's (or a resource's) detail: one line per field of the
// configuration, saying what set the value there, who, where and when -- after following
// a value that arrived from upstream, through merges and links, to where it was set. It
// is `cub unit blame`: the server computes it, and both read it through the SDK's blame
// package, so the two cannot disagree. ↑↓ choose a field; Enter shows everything recorded about
// it (`cub unit path`) in the text view, and h does the same with every change the unit
// recorded to it. Esc returns to the list with the field still chosen.

// BlameResult is a unit's blame: its fields, and how to read one field's full record.
// Path is a function rather than data because the record is read only when a field is
// opened, and asked for as the unit and field the list was read for.
type BlameResult struct {
	Fields []*blame.Field
	Path   func(ctx context.Context, f *blame.Field, history bool) (*blame.PathReport, error)
}

// BlameLoader blames the unit behind a detail row.
type BlameLoader func(ctx context.Context, row cubclient.Row) (*BlameResult, error)

type blameState struct {
	result  *BlameResult
	fields  []*blame.Field // the fields the tab lists: the unit's, or one resource's
	cursor  int
	loading bool
}

type blameMsg struct {
	unitID string
	res    *BlameResult
	err    error
}

type pathMsg struct {
	unitID string
	report *blame.PathReport
	err    error
}

const blameTab = 2

// blameLoad blames the unit behind the detail, once.
func (m *Model) blameLoad() tea.Cmd {
	d := m.det
	if d == nil || !d.editable() || m.blameLoader == nil {
		return nil
	}
	if d.blame == nil {
		d.blame = &blameState{}
	}
	if d.blame.result != nil || d.blame.loading {
		return nil
	}
	d.blame.loading = true
	row, load := d.unitRow, m.blameLoader
	id := unitID(row)
	return func() tea.Msg {
		res, err := load(context.Background(), row)
		return blameMsg{unitID: id, res: res, err: err}
	}
}

// blameLoaded stores a unit's blame. A resource's detail lists only that resource's fields.
func (m *Model) blameLoaded(msg blameMsg) {
	d := m.det
	if d == nil || d.unitKey() != msg.unitID || d.blame == nil {
		return
	}
	d.blame.loading = false
	if msg.err != nil {
		m.setStatus("blame: "+msg.err.Error(), true)
		m.renderDetail()
		return
	}
	d.blame.result = msg.res
	// The server leaves out the pseudo-fields that hold YAML comments unless asked for them.
	d.blame.fields = msg.res.Fields
	if d.entity == "Resource" {
		d.blame.fields = resourceFields(d.row, d.blame.fields)
	}
	d.blame.cursor = min(d.blame.cursor, max(len(d.blame.fields)-1, 0))
	m.renderDetail()
}

// resourceFields keeps the fields of the resource a Resource row names.
func resourceFields(row cubclient.Row, fields []*blame.Field) []*blame.Field {
	res, _ := row["Resource"].(map[string]any)
	resourceType, _ := res["ResourceType"].(string)
	resourceName, _ := res["ResourceName"].(string)
	var kept []*blame.Field
	for _, f := range fields {
		if f.ResourceType == resourceType && f.ResourceName == resourceName {
			kept = append(kept, f)
		}
	}
	return kept
}

// blameKey handles the keys the Blame tab owns, and reports whether it did.
func (m Model) blameKey(k tea.KeyPressMsg) (tea.Model, tea.Cmd, bool) {
	d := m.det
	b := d.blame
	if b == nil || len(b.fields) == 0 {
		return m, nil, false
	}
	switch k.String() {
	case "up", "k":
		b.cursor = max(b.cursor-1, 0)
	case "down", "j":
		b.cursor = min(b.cursor+1, len(b.fields)-1)
	case "pgup":
		b.cursor = max(b.cursor-m.detail.Height(), 0)
	case "pgdown":
		b.cursor = min(b.cursor+m.detail.Height(), len(b.fields)-1)
	case "home", "g":
		b.cursor = 0
	case "end", "G":
		b.cursor = len(b.fields) - 1
	case "enter":
		return m, m.pathLoad(false), true
	case "h":
		return m, m.pathLoad(true), true
	default:
		return m, nil, false
	}
	m.renderDetail()
	return m, nil, true
}

// pathLoad reads the chosen field's full record.
func (m *Model) pathLoad(history bool) tea.Cmd {
	d := m.det
	b := d.blame
	if b == nil || b.result == nil || b.result.Path == nil || len(b.fields) == 0 {
		return nil
	}
	f := b.fields[b.cursor]
	id, read := d.unitKey(), b.result.Path
	if history {
		m.setStatus("reading the record of "+f.Path+" and every change to it…", false)
	} else {
		m.setStatus("reading the record of "+f.Path+"…", false)
	}
	return func() tea.Msg {
		report, err := read(context.Background(), f, history)
		return pathMsg{unitID: id, report: report, err: err}
	}
}

func (m *Model) pathLoaded(msg pathMsg) {
	d := m.det
	if d == nil || d.unitKey() != msg.unitID {
		return
	}
	if msg.err != nil {
		m.setStatus("path: "+msg.err.Error(), true)
		return
	}
	m.setStatus("", false)
	ref := ""
	if msg.report.Field != nil {
		ref = msg.report.Field.Ref
	}
	m.showText("Path "+ref, renderPathReport(msg.report))
}

// blameBody renders the field list, grouped by resource, with the chosen field marked, and
// returns the line the chosen field is on so the viewport can keep it in view.
func (m *Model) blameBody(width int) (string, int) {
	d := m.det
	b := d.blame
	switch {
	case b == nil || b.loading:
		return dimStyle.Render("blaming…"), 0
	case b.result == nil:
		return dimStyle.Render("no blame (R reloads)"), 0
	case len(b.fields) == 0:
		return dimStyle.Render("no fields"), 0
	}

	// Fixed columns, and the path gets what is left. A path is cut from the left, since
	// its end is the part that says which field it is.
	const refW, flagsW, setByW, userW, whereW, revW, whenW = 7, 5, 24, 20, 20, 4, 8
	pathW := max(width-(refW+flagsW+setByW+userW+whereW+revW+whenW+2*7)-1, 20)
	row := func(ref, flags, path, setBy, user, where, rev, when string) string {
		return fmt.Sprintf("%-*s  %-*s  %-*s  %-*s  %-*s  %-*s  %*s  %s",
			refW, ref, flagsW, flags, pathW, cutLeft(path, pathW), setByW, cut(setBy, setByW),
			userW, cut(user, userW), whereW, cut(where, whereW), revW, rev, when)
	}

	var lines []string
	lines = append(lines, dimStyle.Render(row("REF", "FLAGS", "PATH", "SET BY", "USER", "WHERE", "REV", "WHEN")))
	cursorLine, resource := 0, ""
	for i, f := range b.fields {
		if key := f.ResourceType + " " + f.ResourceName; key != resource {
			resource = key
			lines = append(lines, focusStyle.Render(key))
		}
		setBy, user, where, rev, when := "", "", "", "", ""
		if o := blame.OriginOf(f); o != nil {
			setBy, user, where, when = o.SetBy, o.User, o.SpaceSlug, ago(o.When)
			if o.RevisionNum != 0 {
				rev = fmt.Sprintf("%d", o.RevisionNum)
			}
		}
		line := row(f.Ref, blame.Letters(f), f.Path, setBy, user, where, rev, when)
		if i == b.cursor {
			cursorLine = len(lines)
			line = barStyle.Render(line)
		}
		lines = append(lines, line)
	}
	if legend := blame.FlagsUsed(b.fields); len(legend) > 0 {
		lines = append(lines, "")
		for _, flag := range legend {
			lines = append(lines, dimStyle.Render(flag.Letter+"  "+flag.Meaning))
		}
	}
	return strings.Join(lines, "\n"), cursorLine
}

// renderPathReport is `cub unit path` as text: the field as it stands, then each change from
// this unit out to where the value was set, then, when it was read, every change the unit
// recorded to the field.
func renderPathReport(r *blame.PathReport) string {
	var b strings.Builder
	f := r.Field
	if f == nil {
		f = &blame.Field{}
	}
	line := func(indent int, label, value string) {
		if value == "" {
			return
		}
		if label != "" {
			label += ":"
		}
		for i, text := range strings.Split(strings.TrimRight(value, "\n"), "\n") {
			fmt.Fprintf(&b, "%s%-14s %s\n", strings.Repeat(" ", indent), label, text)
			if i == 0 {
				label = ""
			}
		}
	}
	line(0, "Unit", f.SpaceSlug+"/"+f.UnitSlug)
	line(0, "Resource", f.ResourceType+" "+f.ResourceName)
	line(0, "Path", f.Path)
	line(0, "Ref", f.Ref)
	line(0, "Flags", blame.Letters(f))
	line(0, "Value", f.Value)
	line(0, "Released", r.ReleaseMessage)
	if f.Protected {
		line(0, "Protected", "yes: a local override, which a merge from upstream leaves alone")
	}
	line(0, "Guards", blame.FormatGuards(f.Guards))
	kinds := make([]string, 0, len(r.Annotations))
	for kind := range r.Annotations {
		kinds = append(kinds, kind)
	}
	sort.Strings(kinds)
	for _, kind := range kinds {
		line(0, kind, blame.FormatGuards(r.Annotations[kind]))
	}
	for _, attribute := range f.Needed {
		line(0, "Needed", blame.FormatNeeded(attribute))
	}
	for _, attribute := range f.Provided {
		line(0, "Provided", blame.FormatProvided(attribute))
	}
	for _, conflict := range f.Conflicts {
		text := conflict.Reason
		if conflict.Source != nil && conflict.Source.Value != "" {
			text += fmt.Sprintf("; upstream wrote %q", strings.TrimSpace(conflict.Source.Value))
		}
		// The report names the upstream unit a conflict refers to by ID.
		if upstream := r.Names[conflict.UnitID.String()]; upstream != "" {
			text += "; from " + upstream
		}
		line(0, "Conflict", text)
	}
	for _, validation := range f.Validation {
		line(0, "Validation", blame.FormatValidation(validation))
	}
	for _, finding := range f.Findings {
		line(0, "Finding", strings.TrimSpace(finding.Reason+" "+finding.Details))
	}
	if c := r.ConfigComments; c != nil {
		line(0, "Comment above", c.Head)
		line(0, "Line comment", c.Line)
		line(0, "Comment below", c.Foot)
	}
	if thread := r.ReviewThread; thread != nil && len(thread.Comments) > 0 {
		heading := "Review thread"
		if r.ReviewThreads > 1 {
			heading = fmt.Sprintf("Latest of %d review threads", r.ReviewThreads)
		}
		if name := r.Names[thread.ChangeOrderID.String()]; name != "" {
			heading += ", in the review of ChangeOrder " + name
		}
		b.WriteString("\n" + heading + ":\n")
		for _, comment := range thread.Comments {
			// The report leaves out authors it cannot read.
			author := r.Names[comment.UserID.String()]
			if author == "" {
				author = "unknown user"
			}
			fmt.Fprintf(&b, "\n  %s, revision %d, %s\n", author, comment.RevisionNum, ago(comment.CreatedAt))
			for _, text := range strings.Split(strings.TrimRight(comment.Text, "\n"), "\n") {
				b.WriteString("    " + text + "\n")
			}
		}
	}

	b.WriteString("\n")
	if len(r.Changes) == 0 {
		b.WriteString("No recorded change: the value has been there since the unit was created.\n")
	} else {
		b.WriteString("Changes, from this unit out to where the value was set:\n")
	}
	for _, change := range r.Changes {
		o := change.Origin
		if o == nil {
			continue
		}
		fmt.Fprintf(&b, "\n  %s/%s revision %d, mutation %d\n", o.SpaceSlug, o.UnitSlug, o.RevisionNum, o.MutationNum)
		line(4, "Set by", o.SetBy)
		if o.Function != "" && o.Function != o.SetBy {
			line(4, "Function", o.Function)
		}
		if o.LinkUpstreamRevisionNum != 0 {
			line(4, "Read from", fmt.Sprintf("%s, revision %d", o.LinkUpstream, o.LinkUpstreamRevisionNum))
		}
		line(4, "Operation", o.Source)
		line(4, "Description", o.Description)
		line(4, "User", o.User)
		if !o.When.IsZero() {
			line(4, "When", fmt.Sprintf("%s (%s)", o.When.UTC().Format(time.DateTime+" MST"), ago(o.When)))
		}
		line(4, "ChangeOrders", strings.Join(o.ChangeOrders, ", "))
		line(4, "Releases", strings.Join(o.Releases, ", "))
		line(4, "Tags", strings.Join(o.Tags, ", "))
	}

	if r.History != nil {
		b.WriteString("\nHistory of the field in this unit, oldest first:\n\n")
		for _, change := range r.History {
			o := change.Origin
			if o == nil {
				continue
			}
			fmt.Fprintf(&b, "  rev %-5d %-7s %-32s %-28s %-24s %s\n", o.RevisionNum, change.MutationType,
				cut(strings.ReplaceAll(change.Value, "\n", " "), 32), cut(o.SetBy, 28), cut(o.User, 24), ago(o.When))
		}
	}
	return b.String()
}

// ago renders a timestamp as how long ago, the way a reader asks about it.
func ago(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}

// cut shortens text to width, marking the cut at the end.
func cut(text string, width int) string {
	r := []rune(text)
	if len(r) <= width {
		return text
	}
	if width <= 1 {
		return string(r[:width])
	}
	return string(r[:width-1]) + "…"
}

// cutLeft shortens text to width, keeping its end and marking the cut at the start.
func cutLeft(text string, width int) string {
	r := []rune(text)
	if len(r) <= width {
		return text
	}
	if width <= 1 {
		return string(r[len(r)-width:])
	}
	return "…" + string(r[len(r)-width+1:])
}
