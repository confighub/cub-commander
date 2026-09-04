package tui

import (
	"context"
	"fmt"
	"net/url"
	"sort"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/confighub/cub-commander/internal/cubclient"
	"github.com/confighub/cub-commander/internal/exec"
	"github.com/confighub/cub-commander/internal/lang"
)

// Browse renders a statement's `browse by` axes as Finder columns over the
// fetched rows: one pane per axis with distinct values and counts, then a
// pane of the matching rows. Selections are UI state until ^G commits them as
// where steps, which keeps browsing snappy on a large org.

type paneItem struct {
	label string
	value any // nil for "(none)"
	n     int
	row   cubclient.Row // entity pane only
}

type browseState struct {
	axes []lang.Ref
	rows []cubclient.Row
	cur  []int // cursor per pane; len(axes)+2 (the last is the Resources hop)
	pane int   // active pane
	ev   *evalAdapter
	hop  bool // a Resources pane follows the Units pane
	res  map[string][]cubclient.Row
	pend map[string]bool
}

// unionCap bounds how many units' resources the Resources pane unions when
// the cursor sits left of the Units pane; past it, step into Units.
const unionCap = 400

// hopPane is the index of the Resources pane.
func (b *browseState) hopPane() int { return len(b.axes) + 1 }

// Preview pane modes, to the right of the entity pane.
const (
	previewOff = iota
	previewResources
	previewFields
)

// resourcesMsg carries the resources of a batch of units.
type resourcesMsg struct {
	unitIDs []string
	rows    []cubclient.Row
	err     error
}

// unitsWanted lists the units whose resources the Resources pane shows: the
// highlighted unit when the cursor is on Units or Resources, else every unit
// in the Units pane (the whole selection), capped.
func (b *browseState) unitsWanted(panes [][]paneItem) (ids []string, capped bool) {
	last := len(b.axes)
	if last >= len(panes) || len(panes[last]) == 0 {
		return nil, false
	}
	uid := func(it paneItem) string {
		own, _ := it.row["Unit"].(map[string]any)
		id, _ := own["UnitID"].(string)
		return id
	}
	if b.pane >= last {
		return []string{uid(panes[last][b.cur[last]])}, false
	}
	if len(panes[last]) > unionCap {
		return nil, true
	}
	for _, it := range panes[last] {
		if id := uid(it); id != "" {
			ids = append(ids, id)
		}
	}
	return ids, false
}

// selectedRow is the row under the cursor in the entity pane, if any.
func (b *browseState) selectedRow(panes [][]paneItem) cubclient.Row {
	last := len(b.axes)
	if last >= len(panes) || len(panes[last]) == 0 || b.cur[last] >= len(panes[last]) {
		return nil
	}
	return panes[last][b.cur[last]].row
}

// resourceFetch loads the resources the Resources pane (or the preview) needs
// and does not have: one org-wide call per batch of units, selecting only
// type and name so no resource data comes down.
func (m *Model) resourceFetch(panes [][]paneItem) tea.Cmd {
	if m.fetcher == nil || m.browse == nil || m.browse.ev.entity != "Unit" {
		return nil
	}
	if m.previewMode != previewResources && !m.browse.hop {
		return nil
	}
	ids, _ := m.browse.unitsWanted(panes)
	var missing []string
	for _, id := range ids {
		if _, ok := m.resCache[id]; ok || m.resPending[id] {
			continue
		}
		m.resPending[id] = true
		missing = append(missing, id)
	}
	if len(missing) == 0 {
		return nil
	}
	const batch = 100
	var cmds []tea.Cmd
	for i := 0; i < len(missing); i += batch {
		chunk := missing[i:min(i+batch, len(missing))]
		fetch := m.fetcher
		cmds = append(cmds, func() tea.Msg {
			quoted := make([]string, len(chunk))
			for j, id := range chunk {
				quoted[j] = "'" + id + "'"
			}
			q := url.Values{
				"where":  {"UnitID IN (" + strings.Join(quoted, ", ") + ")"},
				"select": {"ResourceType,ResourceName,ResourceID,UnitID,SpaceID,UnitSlug"},
			}
			rows, err := fetch(context.Background(), "/resource", q)
			return resourcesMsg{unitIDs: chunk, rows: rows, err: err}
		})
	}
	return tea.Batch(cmds...)
}

// resourceDetail opens a resource; the pane rows carry no data, so it is
// fetched for the one resource first.
func (m Model) resourceDetail(row cubclient.Row) (tea.Model, tea.Cmd) {
	res, _ := row["Resource"].(map[string]any)
	if _, has := res["Data"]; has || m.fetcher == nil {
		m.openDetailRow(row)
		return m, nil
	}
	sid, _ := res["SpaceID"].(string)
	uid, _ := res["UnitID"].(string)
	rid, _ := res["ResourceID"].(string)
	fetch := m.fetcher
	m.setStatus("loading resource…", false)
	return m, func() tea.Msg {
		rows, err := fetch(context.Background(), "/space/"+sid+"/unit/"+uid+"/resource", url.Values{"where": {"ResourceID = '" + rid + "'"}})
		if err != nil {
			return errMsg{err: err}
		}
		if len(rows) == 0 {
			return errMsg{err: fmt.Errorf("resource not found")}
		}
		return detailMsg{row: rows[0]}
	}
}

type detailMsg struct{ row cubclient.Row }

// previewLines renders the preview pane for the selected row.
func (m *Model) previewLines(panes [][]paneItem, width int) (string, []string) {
	row := m.browse.selectedRow(panes)
	if row == nil {
		return "Preview", []string{dimStyle.Render(" (no row)")}
	}
	switch m.previewMode {
	case previewResources:
		own, _ := row["Unit"].(map[string]any)
		uid, _ := own["UnitID"].(string)
		rows, ok := m.resCache[uid]
		if !ok {
			return "Resources", []string{dimStyle.Render(" loading…")}
		}
		_ = width
		var lines []string
		for _, r := range rows {
			res, _ := r["Resource"].(map[string]any)
			t, _ := res["ResourceType"].(string)
			n, _ := res["ResourceName"].(string)
			lines = append(lines, " "+chipStyle.Render(t)+" "+strings.TrimPrefix(n, "/"))
		}
		sort.Strings(lines)
		if len(lines) == 0 {
			lines = []string{dimStyle.Render(" (no resources)")}
		}
		return fmt.Sprintf("Resources (%d)", len(rows)), lines
	case previewFields:
		var lines []string
		for _, l := range strings.Split(strings.TrimRight(renderRow(row), "\n"), "\n") {
			lines = append(lines, " "+l)
		}
		return "Fields", lines
	}
	return "", nil
}

// plural names the entity pane after what it holds.
func plural(entity string) string {
	switch entity {
	case "BridgeWorker":
		return "Workers"
	case "UnitEvent", "UnitAction", "ChangeSet", "ChangeOrder", "Release":
		return entity + "s"
	}
	if strings.HasSuffix(entity, "y") {
		return entity[:len(entity)-1] + "ies"
	}
	return entity + "s"
}

type evalAdapter struct{ entity string }

func (e *evalAdapter) value(r lang.Ref, row cubclient.Row) any {
	return exec.Value(e.entity, r, row)
}

func (m *Model) startBrowse(axes []lang.Ref, rows []cubclient.Row) {
	hop := false
	if n := len(axes); n > 0 && axes[n-1].Path == "Resource" {
		axes, hop = axes[:n-1], true
	}
	m.browse = &browseState{axes: axes, rows: rows, cur: make([]int, len(axes)+2), ev: &evalAdapter{entity: m.plan.Entity.Name}, hop: hop && m.plan.Entity.Name == "Unit", res: m.resCache, pend: m.resPending}
	m.mode = modeBrowse
	m.focus = focusMain
}

// panes computes every pane given the current cursors.
func (b *browseState) panes() [][]paneItem {
	out := make([][]paneItem, 0, len(b.axes)+1)
	rows := b.rows
	for i, ax := range b.axes {
		groups := map[string]*paneItem{}
		for _, r := range rows {
			v := b.ev.value(ax, r)
			key := exec.Format(v)
			it, ok := groups[key]
			if !ok {
				label := key
				if v == nil || key == "" {
					label, v = "(none)", nil
				}
				it = &paneItem{label: label, value: v}
				groups[key] = it
			}
			it.n++
		}
		items := make([]paneItem, 0, len(groups))
		for _, it := range groups {
			items = append(items, *it)
		}
		sort.Slice(items, func(x, y int) bool {
			if items[x].value == nil {
				return false
			}
			if items[y].value == nil {
				return true
			}
			return items[x].label < items[y].label
		})
		out = append(out, items)
		if b.cur[i] >= len(items) {
			b.cur[i] = max(0, len(items)-1)
		}
		if len(items) == 0 {
			rows = nil
			continue
		}
		sel := items[b.cur[i]]
		var kept []cubclient.Row
		for _, r := range rows {
			v := b.ev.value(ax, r)
			if (v == nil && sel.value == nil) || (v != nil && sel.value != nil && exec.Format(v) == exec.Format(sel.value)) {
				kept = append(kept, r)
			}
		}
		rows = kept
	}
	// Entity pane.
	ent := b.ev.entity
	items := make([]paneItem, 0, len(rows))
	for _, r := range rows {
		items = append(items, paneItem{label: rowLabel(ent, r), row: r, n: 1})
	}
	sort.Slice(items, func(x, y int) bool { return items[x].label < items[y].label })
	out = append(out, items)
	last := len(b.axes)
	if b.cur[last] >= len(items) {
		b.cur[last] = max(0, len(items)-1)
	}
	if b.hop {
		var ritems []paneItem
		ids, _ := b.unitsWanted(out)
		single := b.pane >= last
		for _, id := range ids {
			for _, r := range b.res[id] {
				res, _ := r["Resource"].(map[string]any)
				t, _ := res["ResourceType"].(string)
				n, _ := res["ResourceName"].(string)
				label := t + " " + strings.TrimPrefix(n, "/")
				if !single {
					if us, _ := res["UnitSlug"].(string); us != "" {
						label += "  ·" + us
					}
				}
				ritems = append(ritems, paneItem{label: label, row: r, n: 1})
			}
		}
		sort.Slice(ritems, func(x, y int) bool { return ritems[x].label < ritems[y].label })
		out = append(out, ritems)
		if b.cur[last+1] >= len(ritems) {
			b.cur[last+1] = max(0, len(ritems)-1)
		}
	}
	return out
}

// hopStatus describes the Resources pane when it has nothing to list yet.
func (b *browseState) hopStatus(panes [][]paneItem) string {
	ids, capped := b.unitsWanted(panes)
	if capped {
		return fmt.Sprintf("more than %d units selected; step into Units to see resources", unionCap)
	}
	for _, id := range ids {
		if b.pend[id] {
			return "loading…"
		}
	}
	if len(ids) == 0 {
		return "(no units)"
	}
	return "(no resources)"
}

// rowLabel names an entity row in a pane: space/slug for space-resident
// entities, `type name ·unit` for a resource, the slug otherwise.
func rowLabel(ent string, r cubclient.Row) string {
	own, _ := r[ent].(map[string]any)
	if ent == "Resource" {
		t, _ := own["ResourceType"].(string)
		n, _ := own["ResourceName"].(string)
		label := t + " " + strings.TrimPrefix(n, "/")
		if u, ok := r["Unit"].(map[string]any); ok {
			if us, _ := u["Slug"].(string); us != "" {
				label += "  ·" + us
			}
		}
		return label
	}
	slug, _ := own["Slug"].(string)
	if slug == "" {
		for _, k := range []string{"Username", "RevisionNum", "ResourceName"} {
			if v, ok := own[k]; ok {
				slug = exec.Format(v)
				break
			}
		}
	}
	if sp, ok := r["Space"].(map[string]any); ok && ent != "Space" {
		if ss, _ := sp["Slug"].(string); ss != "" {
			return ss + "/" + slug
		}
	}
	return slug
}

// selections returns the where terms for the panes up to the active one.
func (b *browseState) selections(panes [][]paneItem) []lang.Cmp {
	var terms []lang.Cmp
	for i := 0; i <= b.pane && i < len(b.axes); i++ {
		if len(panes[i]) == 0 {
			continue
		}
		it := panes[i][b.cur[i]]
		if it.value == nil {
			terms = append(terms, lang.Cmp{Left: b.axes[i], Op: "IS NULL"})
			continue
		}
		var right lang.Expr
		switch v := it.value.(type) {
		case float64:
			right = lang.Lit{Kind: lang.LitNumber, N: int64(v)}
		case bool:
			right = lang.Lit{Kind: lang.LitBool, B: v}
		default:
			right = lang.Lit{Kind: lang.LitString, S: exec.Format(v)}
		}
		terms = append(terms, lang.Cmp{Left: b.axes[i], Op: "=", Right: right})
	}
	return terms
}

func (m Model) browseKey(k tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	b := m.browse
	if b == nil {
		return m, nil
	}
	panes := b.panes()
	s := k.String()
	switch s {
	case "p":
		m.previewMode = (m.previewMode + 1) % 3
		m.setStatus(map[int]string{previewOff: "preview off", previewResources: "preview: resources of the selected unit", previewFields: "preview: fields of the selected row"}[m.previewMode], false)
		return m, m.resourceFetch(panes)
	case "left", "h":
		if b.pane > 0 {
			b.pane--
		}
	case "r":
		// Toggle the Resources pane; → from Units turns it on as well.
		b.hop = !b.hop && b.ev.entity == "Unit"
		if !b.hop && b.pane > len(b.axes) {
			b.pane = len(b.axes)
		}
		m.setBrowseStatement()
		return m, m.resourceFetch(b.panes())
	case "right", "l", "tab":
		switch {
		case b.pane < len(b.axes):
			b.pane++
		case b.pane == len(b.axes) && b.ev.entity == "Unit":
			if !b.hop {
				b.hop = true
				m.setBrowseStatement()
			}
			b.pane = b.hopPane()
			return m, m.resourceFetch(b.panes())
		}
	case "up", "k":
		if b.cur[b.pane] > 0 {
			b.cur[b.pane]--
			b.resetRight()
		}
	case "down", "j":
		if b.pane < len(panes) && b.cur[b.pane] < len(panes[b.pane])-1 {
			b.cur[b.pane]++
			b.resetRight()
		}
	case "pgup":
		b.cur[b.pane] = max(0, b.cur[b.pane]-10)
		b.resetRight()
	case "pgdown":
		if b.pane < len(panes) {
			b.cur[b.pane] = max(0, min(len(panes[b.pane])-1, b.cur[b.pane]+10))
			b.resetRight()
		}
	case "enter":
		if b.pane < len(b.axes) {
			b.pane++
		} else if (b.pane == b.hopPane() || b.ev.entity == "Resource") && b.pane < len(panes) && len(panes[b.pane]) > 0 {
			return m.resourceDetail(panes[b.pane][b.cur[b.pane]].row)
		} else if b.pane < len(panes) && len(panes[b.pane]) > 0 {
			m.openDetailRow(panes[b.pane][b.cur[b.pane]].row)
		}
	case "g", "ctrl+g":
		return m.commitBrowse(panes)
	case "m":
		m.markCurrent()
		return m, nil
	case "M":
		m.marks = nil
		m.setStatus("marks cleared", false)
		return m, nil
	case "d":
		return m.diffMarks()
	case "b", "ctrl+b":
		m.openChooser()
	case "q":
		return m, tea.Quit
	case "?":
		m.helpOpen = true
	}
	return m, m.resourceFetch(b.panes())
}

// setBrowseStatement rewrites the command area to show the current browse
// path, including the Resources hop, without running anything.
func (m *Model) setBrowseStatement() {
	if m.stmt == nil || m.browse == nil {
		return
	}
	st := *m.stmt
	st.Browse = append([]lang.Ref(nil), m.browse.axes...)
	if m.browse.hop {
		st.Browse = append(st.Browse, lang.Ref{Path: "Resource"})
	}
	m.stmt = &st
	m.cmd.SetValue(lang.StmtString(&st))
	m.cmd.MoveToEnd()
	m.layout()
}

func (b *browseState) resetRight() {
	for i := b.pane + 1; i < len(b.cur); i++ {
		b.cur[i] = 0
	}
}

// commitBrowse turns the selections into where steps and shows the grid.
func (m Model) commitBrowse(panes [][]paneItem) (tea.Model, tea.Cmd) {
	if m.stmt == nil || m.browse == nil {
		return m, nil
	}
	st := *m.stmt
	for _, t := range m.browse.selections(panes) {
		st = addTerm(st, t)
	}
	st.Browse = nil
	return m.rewrite(&st)
}

// browseView renders the panes side by side.
func (m Model) browseView() string {
	b := m.browse
	w, h := m.mainWidth(), m.mainHeight()
	if b == nil {
		return ""
	}
	panes := b.panes()
	n := len(panes)
	preview := m.previewMode != previewOff && !(m.previewMode == previewResources && b.hop)
	if preview {
		n++
	}
	pw := (w - (n - 1)) / n
	if pw < 12 {
		pw = 12
	}
	var cols []string
	for i, items := range panes {
		title := plural(b.ev.entity)
		if i < len(b.axes) {
			title = axisTitle(b.axes[i])
		} else if i == b.hopPane() {
			title = "Resources"
		}
		title = fmt.Sprintf("%s (%d)", title, len(items))
		if i == b.pane {
			title = focusStyle.Render(title)
		} else {
			title = titleStyle.Render(title)
		}
		lines := []string{lipgloss.NewStyle().MaxWidth(pw).Render(title)}
		body := h - 1
		start := 0
		if b.cur[i] >= body {
			start = b.cur[i] - body + 1
		}
		for j := start; j < len(items) && j < start+body; j++ {
			it := items[j]
			count := ""
			if i < len(b.axes) {
				count = fmt.Sprintf(" %d", it.n)
			}
			avail := pw - len(count) - 1
			label := it.label
			if len(label) > avail && avail > 1 {
				label = label[:avail-1] + "…"
			}
			line := fmt.Sprintf("%-*s%s", avail, label, dimStyle.Render(count))
			switch {
			case j == b.cur[i] && i == b.pane:
				line = keyStyle.Render(fmt.Sprintf("%-*s%s", avail, label, count))
			case j == b.cur[i] && i < b.pane:
				line = chipStyle.Render(fmt.Sprintf("%-*s%s", avail, label, count))
			}
			lines = append(lines, " "+line)
		}
		if len(items) == 0 {
			if i == b.hopPane() {
				lines = append(lines, dimStyle.Render(" "+b.hopStatus(panes)))
			} else {
				lines = append(lines, dimStyle.Render(" (empty)"))
			}
		}
		col := lipgloss.NewStyle().Width(pw).Height(h).MaxWidth(pw).MaxHeight(h).Render(strings.Join(lines, "\n"))
		cols = append(cols, col)
	}
	if preview {
		title, lines := m.previewLines(panes, pw)
		all := append([]string{lipgloss.NewStyle().MaxWidth(pw).Render(titleStyle.Render(title))}, lines...)
		if len(all) > h {
			all = all[:h]
		}
		cols = append(cols, lipgloss.NewStyle().Width(pw).Height(h).MaxWidth(pw).MaxHeight(h).Render(strings.Join(all, "\n")))
	}
	sep := dimStyle.Render(strings.Repeat("▏\n", h))
	var parts []string
	for i, c := range cols {
		if i > 0 {
			parts = append(parts, sep)
		}
		parts = append(parts, c)
	}
	return lipgloss.JoinHorizontal(lipgloss.Top, parts...)
}

func axisTitle(r lang.Ref) string {
	segs := strings.Split(r.Path, ".")
	switch {
	case len(segs) == 2 && segs[0] == "Labels":
		return segs[1]
	case len(segs) == 3 && segs[1] == "Labels":
		return segs[0] + "·" + segs[2]
	case len(segs) == 2 && segs[1] == "Slug":
		return segs[0]
	}
	return r.Path
}

// ---- chooser: the gentle start. Presets come from the org's label keys.

type chooserItem struct {
	label string
	stmt  string
}

func (m *Model) openChooser() {
	m.chooserOpen = true
	m.chooserCursor = 0
	m.focus = focusMain
	m.chooserItems = m.presets()
}

// presets builds the chooser from where labels actually live: a key on
// most units is used as Labels.k; one that lives on spaces is reached as
// Space.Labels.k (Component and Variant usually do), and a key neither
// carries is skipped. Before the sample lands, unit labels are assumed.
func (m *Model) presets() []chooserItem {
	sampled := m.live != nil && m.live.Total("Space") > 0
	// axis returns the path for key k when browsing entity ent, or "".
	axis := func(ent, k string) string {
		if !sampled {
			return "Labels." + k
		}
		own := m.live.Coverage(ent, k)
		space := m.live.Coverage("Space", k)
		switch ent {
		case "Space":
			if own > 0 {
				return "Labels." + k
			}
		case "Unit":
			if own >= 0.5 {
				return "Labels." + k
			}
			if space > 0 {
				return "Space.Labels." + k
			}
			if own > 0 {
				return "Labels." + k
			}
		case "Resource":
			if m.live.Coverage("Unit", k) >= 0.5 {
				return "Unit.Labels." + k
			}
			if space > 0 {
				return "Space.Labels." + k
			}
		case "Target":
			if own > 0 {
				return "Labels." + k
			}
		}
		return ""
	}
	path := func(ent string, keys ...string) []string {
		var out []string
		for _, k := range keys {
			if a := axis(ent, k); a != "" {
				out = append(out, a)
			}
		}
		return out
	}
	var items []chooserItem
	add := func(entity, label string, axes []string, extra ...string) {
		if len(axes) == 0 {
			return
		}
		axes = append(axes, extra...)
		items = append(items, chooserItem{label: fmt.Sprintf("%-8s by %s", entity, label), stmt: fmt.Sprintf("%s | in * | browse by %s", entity, strings.Join(axes, ", "))})
	}
	add("Unit", "Component → Variant → Units", path("Unit", "Component", "Variant"))
	add("Unit", "Component → Variant → Units → Resources", path("Unit", "Component", "Variant"), "Resource")
	add("Unit", "Component → Environment → Region → Cluster", path("Unit", "Component", "Environment", "Region", "Cluster"))
	add("Unit", "Environment → Region → Cluster → Component", path("Unit", "Environment", "Region", "Cluster", "Component"))
	add("Unit", "Layer → Component → Variant", path("Unit", "Layer", "Component", "Variant"))
	add("Unit", "Owner → Component → Variant", path("Unit", "Owner", "Component", "Variant"))
	add("Unit", "Department → Component → Environment", path("Unit", "Department", "Component", "Environment"))
	add("Unit", "Space", []string{"Space.Slug"})
	add("Unit", "Target", []string{"Target.Slug"})
	add("Resource", "Component → Variant → Resources", path("Resource", "Component", "Variant"))
	add("Resource", "Component → Environment → Region → Cluster → Resources", path("Resource", "Component", "Environment", "Region", "Cluster"))
	add("Space", "Component → Variant", path("Space", "Component", "Variant"))
	add("Space", "Layer → Component → Variant", path("Space", "Layer", "Component", "Variant"))
	add("Space", "Environment → Region → Cluster", path("Space", "Environment", "Region", "Cluster"))
	add("Space", "Owner → Component", path("Space", "Owner", "Component"))
	if axes := path("Target", "Environment", "Region"); len(axes) == 2 {
		items = append(items, chooserItem{
			label: "Target   by Environment → Region → Component → Units → Resources",
			stmt:  "Unit | in * | where TargetID IS NOT NULL | browse by Target.Labels.Environment, Target.Labels.Region, " + firstNonEmpty(axis("Unit", "Component"), "Space.Labels.Component") + ", Resource",
		})
		items = append(items, chooserItem{
			label: "Target   by Environment → Region → Component → Units",
			stmt:  "Unit | in * | where TargetID IS NOT NULL | browse by Target.Labels.Environment, Target.Labels.Region, " + firstNonEmpty(axis("Unit", "Component"), "Space.Labels.Component"),
		})
	}
	items = append(items, chooserItem{label: "Target   by Target → Units", stmt: "Unit | in * | where TargetID IS NOT NULL | browse by Target.Slug"})
	items = append(items,
		chooserItem{label: "Unit     raw list (default columns)", stmt: "Unit | in * | limit 500"},
		chooserItem{label: "Space    raw list", stmt: "Space"},
		chooserItem{label: "Target   raw list", stmt: "Target | in *"},
		chooserItem{label: "Custom… (type the axes; Tab completes label keys)", stmt: ""},
	)
	// Drop duplicates a sparse org can produce.
	seen := map[string]bool{}
	var out []chooserItem
	for _, it := range items {
		if it.stmt != "" && seen[it.stmt] {
			continue
		}
		seen[it.stmt] = true
		out = append(out, it)
	}
	return out
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func (m Model) chooserKey(k tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch k.String() {
	case "up", "k":
		if m.chooserCursor > 0 {
			m.chooserCursor--
		}
	case "down", "j":
		if m.chooserCursor < len(m.chooserItems)-1 {
			m.chooserCursor++
		}
	case "enter":
		it := m.chooserItems[m.chooserCursor]
		m.chooserOpen = false
		if it.stmt == "" {
			m.cmd.SetValue("Unit | in * | browse by ")
			m.cmd.MoveToEnd()
			m.focus = focusCmd
			m.layout()
			return m, nil
		}
		m.cmd.SetValue(it.stmt)
		m.cmd.MoveToEnd()
		m.layout()
		return m.execute(it.stmt)
	case "esc":
		if m.result != nil || m.browse != nil {
			m.chooserOpen = false
		}
	case "q":
		return m, tea.Quit
	case "?":
		m.helpOpen = true
	}
	return m, nil
}

func (m Model) chooserView() string {
	var b strings.Builder
	b.WriteString(titleStyle.Render(" Browse by") + dimStyle.Render("   ↑↓ choose · ⏎ open · or type a statement below · ^/ help") + "\n\n")
	for i, it := range m.chooserItems {
		line := "   " + it.label
		if i == m.chooserCursor {
			line = " " + keyStyle.Render("▸ "+it.label)
		}
		b.WriteString(line + "\n")
	}
	if m.live != nil && len(m.live.Spaces()) == 0 {
		b.WriteString("\n" + dimStyle.Render("   sampling the org's labels…"))
	}
	b.WriteString("\n" + dimStyle.Render("   In browse: ←→ panes · ↑↓ values · ⏎ open a row · r resources pane · p preview · g grid with these filters"))
	b.WriteString("\n" + dimStyle.Render("   Diff: m marks the selection as A, m again marks B, d diffs the like units across them · b back here"))
	return b.String()
}
