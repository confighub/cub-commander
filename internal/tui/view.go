package tui

import (
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/confighub/cub-commander/internal/lang"
)

var (
	barStyle         = lipgloss.NewStyle().Reverse(true)
	keyStyle         = lipgloss.NewStyle().Reverse(true).Bold(true)
	chipStyle        = lipgloss.NewStyle().Foreground(lipgloss.Color("6"))
	pendingChipStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("4"))
	markStyle        = lipgloss.NewStyle().Foreground(lipgloss.Color("5")).Bold(true)
	localChipStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("3"))
	dimStyle         = lipgloss.NewStyle().Faint(true)
	errStyle         = lipgloss.NewStyle().Foreground(lipgloss.Color("1")).Bold(true)
	okStyle          = lipgloss.NewStyle().Foreground(lipgloss.Color("2"))
	focusStyle       = lipgloss.NewStyle().Foreground(lipgloss.Color("4")).Bold(true)
	titleStyle       = lipgloss.NewStyle().Bold(true)
	drawerStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("3"))
)

const chromeRows = 1 + 1 + 1 + 1 + 1 // top bar, chips, command title, status, key bar

func (m *Model) cmdHeight() int {
	h := m.cmd.Height()
	if h < 3 {
		h = 3
	}
	return h
}

func (m *Model) mainHeight() int {
	h := m.height - chromeRows - m.cmdHeight()
	if h < 3 {
		h = 3
	}
	return h
}

func (m *Model) drawerWidth() int {
	if !m.drawerOpen {
		return 0
	}
	w := m.width * 2 / 5
	if w < 30 {
		w = 30
	}
	return w
}

func (m *Model) mainWidth() int {
	w := m.width - m.drawerWidth()
	if w < 20 {
		w = 20
	}
	return w
}

// layout resizes the components to the current window.
func (m *Model) layout() {
	if m.width == 0 {
		return
	}
	m.cmd.SetWidth(m.width)
	m.cmd.MaxHeight = max(3, m.height/3)
	mh := m.mainHeight()
	mw := m.mainWidth()
	m.tbl.SetWidth(mw)
	m.tbl.SetHeight(mh)
	m.detail.SetWidth(mw)
	m.detail.SetHeight(mh - 1)
	m.text.SetWidth(mw)
	m.text.SetHeight(mh)
	m.fillTable()
}

func (m Model) View() tea.View {
	if m.width == 0 {
		return tea.NewView("loading…")
	}
	var b strings.Builder
	b.WriteString(m.topBar() + "\n")
	b.WriteString(m.chipsRow() + "\n")
	main := m.mainView()
	if m.helpOpen {
		main = lipgloss.Place(m.mainWidth(), m.mainHeight(), lipgloss.Left, lipgloss.Top, helpText)
	}
	if m.drawerOpen {
		main = lipgloss.JoinHorizontal(lipgloss.Top, main, m.drawerView())
	}
	if m.popupOpen && len(m.popup) > 0 {
		main = m.overlayPopup(main)
	}
	b.WriteString(main + "\n")
	if m.mode == modeDetail {
		// the detail header takes one line of the main area
		main = lipgloss.NewStyle().MaxHeight(m.mainHeight()).Render(main)
	}
	b.WriteString(m.cmdTitle() + "\n")
	b.WriteString(m.cmd.View() + "\n")
	b.WriteString(m.statusLine() + "\n")
	b.WriteString(m.keyBar())

	v := tea.NewView(b.String())
	v.AltScreen = true
	v.KeyboardEnhancements = tea.KeyboardEnhancements{ReportAlternateKeys: true}
	if m.focus == focusCmd && !m.drawerOpen && !m.helpOpen {
		if c := m.cmd.Cursor(); c != nil {
			c.Y += 2 + m.mainHeight() + 1
			v.Cursor = c
		}
	}
	return v
}

func (m Model) topBar() string {
	scope := m.sess.Space
	if scope == "" || scope == "*" {
		scope = "*"
	}
	modeName := map[mode]string{modeResults: "Results", modeDetail: "Detail", modeText: m.textTitle, modeBrowse: "Browse", modeDiff: "Diff"}[m.mode]
	if m.chooserOpen {
		modeName = "Browse by"
	}
	left := fmt.Sprintf(" %s · %s · USE %s · %s", m.ctxName, m.server, scope, modeName)
	right := ""
	if m.result != nil {
		if m.result.ServerRows != len(m.result.Rows) {
			right = fmt.Sprintf("server %d → %d rows ", m.result.ServerRows, len(m.result.Rows))
		} else {
			right = fmt.Sprintf("%d rows ", len(m.result.Rows))
		}
	}
	if m.running {
		right = "running… " + right
	}
	pad := m.width - lipgloss.Width(left) - lipgloss.Width(right)
	if pad < 1 {
		pad = 1
	}
	return barStyle.Render(left + strings.Repeat(" ", pad) + right)
}

func (m Model) chipsRow() string {
	var parts []string
	hasLocal := false
	for i, mk := range m.marks {
		parts = append(parts, markStyle.Render(fmt.Sprintf("%c: %s", 'A'+i, mk.label)))
	}
	if m.mode == modeBrowse && m.browse != nil {
		for _, t := range m.browse.selections(m.browse.panes()) {
			parts = append(parts, pendingChipStyle.Render("["+lang.ExprString(t)+"]"))
		}
	}
	if m.stmt == nil || len(m.stmt.Filters) == 0 {
		if len(parts) > 0 {
			hint := "  (browse selections · g makes them where steps)"
			if len(m.marks) > 0 {
				hint = "  (m marks A then B · d diffs them · M clears)"
			}
			return lipgloss.NewStyle().MaxWidth(m.width).Render(" " + strings.Join(parts, " ") + dimStyle.Render(hint))
		}
		if m.mode == modeBrowse {
			return dimStyle.Render(" no where steps yet · pick values in the panes")
		}
		return dimStyle.Render(" no where steps · press f on a cell to filter by its value")
	}
	var fixed []string
	for i, f := range m.stmt.Filters {
		style := chipStyle
		pushed := m.plan != nil && i < len(m.plan.Pushed) && m.plan.Pushed[i]
		if !pushed {
			style = localChipStyle
			hasLocal = true
		}
		for _, t := range lang.Conjuncts(f.Expr) {
			fixed = append(fixed, style.Render("["+lang.ExprString(t)+"]"))
		}
	}
	parts = append(fixed, parts...)
	hint := "  (- removes last)"
	if hasLocal {
		hint = "  (- removes last · yellow = evaluated locally, ^X says why)"
	}
	return lipgloss.NewStyle().MaxWidth(m.width).Render(" " + strings.Join(parts, " ") + dimStyle.Render(hint))
}

func (m Model) mainView() string {
	w, h := m.mainWidth(), m.mainHeight()
	var body string
	if m.running {
		return lipgloss.NewStyle().Width(w).Height(h).MaxWidth(w).MaxHeight(h).Render(m.loadingView())
	}
	if m.chooserOpen {
		return lipgloss.NewStyle().Width(w).Height(h).MaxWidth(w).MaxHeight(h).Render(m.chooserView())
	}
	switch m.mode {
	case modeBrowse:
		return m.browseView()
	case modeDiff:
		return m.diffView()
	case modeResults:
		if m.result == nil {
			body = dimStyle.Render("no results yet")
		} else if len(m.result.Rows) == 0 {
			body = dimStyle.Render("0 rows")
		} else {
			body = m.tbl.View()
		}
	case modeDetail:
		body = m.detailView()
	default:
		body = m.text.View()
	}
	return lipgloss.NewStyle().Width(w).Height(h).MaxWidth(w).MaxHeight(h).Render(body)
}

// loadingView replaces the main area while a statement runs, so a stale
// screen never looks like the answer.
func (m Model) loadingView() string {
	elapsed := time.Since(m.runStart).Truncate(100 * time.Millisecond)
	dots := strings.Repeat("·", int(elapsed/(300*time.Millisecond))%4)
	var b strings.Builder
	b.WriteString("\n\n")
	b.WriteString(titleStyle.Render("   Running") + " " + dimStyle.Render(fmt.Sprintf("%s  %.1fs", dots, elapsed.Seconds())) + "\n\n")
	for _, line := range strings.Split(strings.TrimSpace(m.runningSrc), "\n") {
		b.WriteString("   " + chipStyle.Render(line) + "\n")
	}
	b.WriteString("\n" + dimStyle.Render("   an org-wide list is one call; a large org takes a few seconds"))
	return b.String()
}

func (m Model) drawerView() string {
	w, h := m.drawerWidth(), m.mainHeight()
	var b strings.Builder
	b.WriteString(titleStyle.Render(" History") + dimStyle.Render("  type to search · ⏎ load · ⌥⏎ run · Esc close") + "\n")
	b.WriteString(" > " + m.drawerFilter + "▏\n")
	shown := h - 2
	start := 0
	if m.drawerCursor >= shown {
		start = m.drawerCursor - shown + 1
	}
	for i := start; i < len(m.drawerItems) && i < start+shown; i++ {
		e := m.drawerItems[i]
		line := strings.Join(strings.Fields(e.Stmt), " ")
		meta := fmt.Sprintf("%d ", e.Rows)
		if e.Err != "" {
			meta = "err "
		}
		avail := w - 3 - len(meta)
		if avail < 5 {
			avail = 5
		}
		if len(line) > avail {
			line = line[:avail-1] + "…"
		}
		prefix := "  "
		if i == m.drawerCursor {
			prefix = "▸ "
			line = focusStyle.Render(line)
		}
		b.WriteString(prefix + dimStyle.Render(meta) + line + "\n")
	}
	if len(m.drawerItems) == 0 {
		b.WriteString(dimStyle.Render("  nothing yet"))
	}
	return lipgloss.NewStyle().Width(w).Height(h).MaxWidth(w).MaxHeight(h).Render(drawerStyle.Render("▏") + strings.ReplaceAll(strings.TrimRight(b.String(), "\n"), "\n", "\n"+drawerStyle.Render("▏")))
}

// overlayPopup draws the completion list over the bottom rows of the main area.
func (m Model) overlayPopup(main string) string {
	lines := strings.Split(main, "\n")
	const maxRows = 8
	n := len(m.popup)
	if n > maxRows {
		n = maxRows
	}
	first := 0
	if m.popupSel >= maxRows {
		first = m.popupSel - maxRows + 1
	}
	width := 0
	for _, c := range m.popup {
		if w := len(c.Text) + 2 + len(c.Detail); w > width {
			width = w
		}
	}
	if width > m.width-4 {
		width = m.width - 4
	}
	// Column under the cursor's segment, roughly: the segment start within its line.
	v := m.cmd.Value()
	lineStart := strings.LastIndex(v[:min(m.popupStart, len(v))], "\n") + 1
	x := m.popupStart - lineStart
	if x+width+2 > m.width {
		x = max(0, m.width-width-2)
	}
	var box []string
	for i := first; i < first+n && i < len(m.popup); i++ {
		c := m.popup[i]
		text := fmt.Sprintf("%-*s", width-len(c.Detail)-2, c.Text) + "  " + dimStyle.Render(c.Detail)
		if i == m.popupSel {
			text = keyStyle.Render(fmt.Sprintf("%-*s", width, c.Text+"  "+c.Detail))
		}
		box = append(box, strings.Repeat(" ", x)+"▏"+text)
	}
	if len(m.popup) > n {
		box = append(box, strings.Repeat(" ", x)+dimStyle.Render(fmt.Sprintf("▏… %d more (Tab cycles)", len(m.popup)-n)))
	}
	start := len(lines) - len(box)
	if start < 0 {
		start = 0
	}
	for i, b := range box {
		if start+i < len(lines) {
			lines[start+i] = lipgloss.NewStyle().MaxWidth(m.mainWidth()).Render(b)
		}
	}
	return strings.Join(lines, "\n")
}

func (m Model) cmdTitle() string {
	label := " command"
	if m.focus == focusCmd {
		label = focusStyle.Render(" command")
	}
	hint := "Tab complete · ⏎ run when complete · ⌥⏎ run · ⇧⏎ newline · ↑↓ history · ^R search"
	if !m.kitty {
		hint = "Tab complete · ⏎ run when complete · ⌥⏎ run · ↑↓ history · ^R search"
	}
	pad := m.width - lipgloss.Width(label) - lipgloss.Width(hint) - 1
	if pad < 1 {
		pad = 1
	}
	return label + strings.Repeat("─", pad) + dimStyle.Render(hint) + " "
}

func (m Model) statusLine() string {
	s := m.status
	if s == "" {
		s = " "
	}
	if m.statusErr {
		return lipgloss.NewStyle().MaxWidth(m.width).Render(errStyle.Render(" ✗ " + s))
	}
	return lipgloss.NewStyle().MaxWidth(m.width).Render(okStyle.Render(" ") + s)
}

func (m Model) keyBar() string {
	keys := []struct{ k, label string }{
		{"^B", "browse"}, {"^G", "grid"}, {"^O", "open row"}, {"^R", "history"}, {"^X", "explain"}, {"^/", "help"}, {"^Q", "quit"},
		{"⇧Tab", "focus"}, {"f", "filter"}, {"o", "order"}, {"s/t/u/d/r/l", "pivot"},
	}
	var parts []string
	for _, k := range keys {
		parts = append(parts, keyStyle.Render(k.k)+" "+k.label)
	}
	line := " " + strings.Join(parts, "  ")
	return lipgloss.NewStyle().MaxWidth(m.width).Render(line)
}

const helpText = `cub commander — terminal lab for the ConfigHub data model

A statement is a pipeline: an entity, then steps. Pipes are optional; every step starts
with a keyword. Type it in the command area and press Enter.

  Unit | in * | where Labels.Environment = 'prod' | columns Slug, Space.Slug, Target.Slug
  Unit | where HeadRevisionNum > LastReleasedRevisionNum | order by UpdatedAt desc | limit 20
  Unit | in * | columns Labels.Environment as env, COUNT(*) as n | group by env | order by n desc
  Space | where Labels.Component = 'checkout'        Revision | where UnitID = '…' | order by RevisionNum desc

Steps: in <space|*>, where <expr>, columns <a, b as x, …>, browse by <axes>, diff <A> vs <B> [by …],
group by, order by, limit. columns Labels.* expands to one column per label key.

Diff: Unit | where Labels.Component = 'cart' | diff Labels.Environment = 'dev' vs Labels.Environment = 'prod'
compares like units across the two sides. The selector may sit at any level: from a Space,
Target or Resource browse the marks are lifted to the units inside (Space.Labels.Variant = …).
Units are (paired by slug, component, and the label dimensions both
sides share; DataHash decides same/differ; data is fetched only for the pair on screen). In browse,
m marks the selection as A, m again marks B, d runs the diff. In the diff view: ↑↓ pairs, n/p next
and previous differing pair, = hides identical pairs, PgUp/PgDn scroll, ⏎ opens the full diff.
The leading where steps go to the server (AND only). A step the server cannot take (OR, NOT,
a function result, an alias) runs locally, and so does everything after it; those chips show
in yellow and ^X explains each stage with the equivalent cub command.
SQL still works: SELECT … FROM Unit IN * WHERE … HAVING … ORDER BY … LIMIT n.

  EXPLAIN <statement>    USE prod-eu    USE *    SHOW ENTITIES    SHOW COLUMNS FROM Unit
  SHOW LABELS FROM Space    SHOW VALUES OF Labels.Environment    DESCRIBE Link.UpdateType

Scope: statements run org-wide (USE *) until narrowed with USE <space> or in <space>.

Global keys (no F-keys; they fight macOS)
  ^B  browse by…  ^G  results grid   ^O  open the selected row    ^R  history
  ^X  explain     ^/  help           ^Q  quit    ⇧Tab move focus   Esc  back / close

Browse (Unit | in * | browse by Labels.Component, Labels.Environment): one pane per axis with
counts, then the matching Units, then a Resources pane (r toggles it; → from Units opens it;
"…, Resource" in the path) showing the resources of everything under the selection, or of
the highlighted unit once you step into Units. ←→ panes, ↑↓ values, ⏎ open a row, p adds a preview pane on
the right (the selected unit's resources, then the row's fields, then off), g turns the
selections into where steps and shows the grid, b returns to the chooser.

Detail (Enter on a row): tabs 1 Metadata · 2 Data (←→ switch). On Data: e opens $EDITOR on the
unit's configuration and, when you save and exit, posts it as a new revision, conditional on
the DataHash you read (If-Match). A conflict reloads the head and keeps your edit for the next
e. R reloads. A resource opens the same way: its Data tab is its own document, and e edits
that document; the save writes the unit with the document replaced, under the unit's hash.
d lists the unit's revisions: ⏎ diffs the highlighted one against the current
data, m marks one and ⏎ on another diffs the two; Esc returns to the list, Esc again closes
it. The pivot keys
work here too: u on a space lists its units, s on a unit shows its space's units, r revisions.

Keys on a results row
  Enter  detail            f  add "where <column> = <value>" for the focused cell (←→ move)
  -      drop last chip    o  order by the focused column (again to flip)
  s space  t target  u upstream  d downstreams  r revisions  l links

Command area: Tab completes (entities, steps, attributes, joins, label keys and values,
operators; Tab again cycles, ⏎ accepts, Esc closes), ⏎ runs a complete statement (else
newline), ⌥⏎ always runs, ↑↓ walk history, ^L clears. ^A/^E line start/end and the other
readline chords work. In the results, ? also opens this help and q quits.`
