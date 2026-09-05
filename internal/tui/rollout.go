package tui

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/confighub/cub-commander/internal/cubclient"
	"github.com/confighub/cub-commander/internal/lang"
	"github.com/confighub/cub-commander/internal/plan"
	"github.com/confighub/cub-commander/internal/rollout"
)

// Rollout mode shows one ChangeOrder as a rollout: the stage strip, the
// selected stage's spaces with their taken/released/healthy bits, the gates
// on the next hop in the CLI's words, and on the right the change itself --
// per unit, the revision the order's start tag marks against the one its end
// tag marks -- for the base or for any space that has taken it.
//
// The mode is entered by a statement (`ChangeOrder | … | rollout`), so
// history, EXPLAIN and the editor keep working; Enter on a ChangeOrder row
// in the grid rewrites to it. Esc restores the statement it came from.
type rolloutState struct {
	ro     *rollout.Rollout
	row    cubclient.Row
	stage  int  // index into ro.Stages
	space  int  // index into the selected stage's Spaces
	scroll int  // first body line shown in the right pane
	pane   int  // 0: the stage's spaces (↑↓ pick a space); 1: the diff (↑↓ scroll it). Tab toggles.
	raw    bool // w: diff the text as stored instead of the canonical re-encoding
	// dry runs per stage index, loaded on demand
	previews       map[int]*rollout.Preview
	previewPending map[int]bool
	previewErrs    map[int]string
	confirm        *confirmState // the write waiting for y
	report         string        // shown once the refresh after a write lands
	reportTitle    string
	// the change per space, keyed by SpaceID; loaded on demand
	changes map[string][]rollout.UnitChange
	pending map[string]bool
	errs    map[string]string
	// the statement the rollout was opened from, restored on Esc
	fromStmt *lang.SelectStmt
	fromPlan *plan.Plan
}

// ChangeLoader reads what a change order did in one space (rollout.Change).
type ChangeLoader func(ctx context.Context, o rollout.Order, spaceID string) ([]rollout.UnitChange, error)

type rolloutMsg struct {
	src  string
	stmt *lang.SelectStmt
	plan *plan.Plan
	ro   *rollout.Rollout
	row  cubclient.Row
}

type changeMsg struct {
	orderID, spaceID string
	changes          []rollout.UnitChange
	err              error
}

func (m *Model) rolloutLoaded(msg rolloutMsg) tea.Cmd {
	m.running = false
	m.chooserOpen = false
	rs := &rolloutState{ro: msg.ro, row: msg.row, changes: map[string][]rollout.UnitChange{}, pending: map[string]bool{}, errs: map[string]string{},
		previews: map[int]*rollout.Preview{}, previewPending: map[int]bool{}, previewErrs: map[int]string{}}
	var report, reportTitle string
	if m.roll != nil && m.mode == modeRollout {
		rs.fromStmt, rs.fromPlan = m.roll.fromStmt, m.roll.fromPlan
		// a refresh keeps the position; a write's report is shown now
		rs.stage, rs.space, rs.pane, rs.raw = m.roll.stage, m.roll.space, m.roll.pane, m.roll.raw
		report, reportTitle = m.roll.report, m.roll.reportTitle
	} else {
		rs.fromStmt, rs.fromPlan = m.stmt, m.plan
		rs.stage = max(msg.ro.Next, 0)
	}
	if msg.plan.Rollout != nil && msg.plan.Rollout.Stage != "" {
		for i, st := range msg.ro.Stages {
			if strings.EqualFold(st.Name, msg.plan.Rollout.Stage) {
				rs.stage = i
			}
		}
	}
	rs.clamp()
	m.roll = rs
	m.stmt, m.plan = msg.stmt, msg.plan
	m.mode = modeRollout
	m.focus = focusMain
	m.record(msg.src, len(msg.ro.Stages), "")
	m.setStatus(fmt.Sprintf("%s · %s", msg.ro.State, msg.ro.Blocker), false)
	cmd := tea.Batch(m.changeFetch(), m.previewFetch())
	if report != "" {
		m.showText(reportTitle, report)
	}
	return cmd
}

func (rs *rolloutState) clamp() {
	if rs.stage < 0 {
		rs.stage = 0
	}
	if rs.stage >= len(rs.ro.Stages) {
		rs.stage = len(rs.ro.Stages) - 1
	}
	n := len(rs.ro.Stages[rs.stage].Spaces)
	if rs.space >= n {
		rs.space = max(n-1, 0)
	}
	if rs.space < 0 {
		rs.space = 0
	}
}

func (rs *rolloutState) selectedStage() *rollout.StageState { return &rs.ro.Stages[rs.stage] }

func (rs *rolloutState) selectedSpace() *rollout.Space {
	st := rs.selectedStage()
	if rs.space < len(st.Spaces) {
		return &st.Spaces[rs.space]
	}
	return nil
}

// changeFetch loads the change for the selected space when it has taken the
// order (the base always has) and it is not loaded yet.
func (m *Model) changeFetch() tea.Cmd {
	rs := m.roll
	if rs == nil || m.changeLoader == nil {
		return nil
	}
	sp := rs.selectedSpace()
	if sp == nil || !sp.Taken || rs.pending[sp.ID] {
		return nil
	}
	if _, ok := rs.changes[sp.ID]; ok {
		return nil
	}
	if _, ok := rs.errs[sp.ID]; ok {
		return nil
	}
	rs.pending[sp.ID] = true
	loader, o, id := m.changeLoader, rs.ro.Order, sp.ID
	return func() tea.Msg {
		ch, err := loader(context.Background(), o, id)
		return changeMsg{orderID: o.ID, spaceID: id, changes: ch, err: err}
	}
}

func (m *Model) changeLoaded(msg changeMsg) {
	rs := m.roll
	if rs == nil || rs.ro.Order.ID != msg.orderID {
		return
	}
	delete(rs.pending, msg.spaceID)
	if msg.err != nil {
		rs.errs[msg.spaceID] = msg.err.Error()
		return
	}
	rs.changes[msg.spaceID] = msg.changes
}

func (m Model) rolloutKey(k tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	rs := m.roll
	if rs == nil {
		return m, nil
	}
	switch k.String() {
	case "left", "h":
		rs.stage--
		rs.space, rs.scroll = 0, 0
		rs.clamp()
		return m, tea.Batch(m.changeFetch(), m.previewFetch())
	case "right", "l":
		rs.stage++
		rs.space, rs.scroll = 0, 0
		rs.clamp()
		return m, tea.Batch(m.changeFetch(), m.previewFetch())
	case "P":
		return m.promoteRequest()
	case "L":
		return m.releaseRequest()
	case "B":
		return m.bothRequest()
	case "tab":
		rs.pane = 1 - rs.pane
		return m, nil
	case "up", "k":
		if rs.pane == 1 {
			rs.scroll = max(0, rs.scroll-1)
			return m, nil
		}
		rs.space--
		rs.scroll = 0
		rs.clamp()
		return m, tea.Batch(m.changeFetch(), m.previewFetch())
	case "down", "j":
		if rs.pane == 1 {
			rs.scroll++
			return m, nil
		}
		rs.space++
		rs.scroll = 0
		rs.clamp()
		return m, tea.Batch(m.changeFetch(), m.previewFetch())
	case "pgdown", " ":
		rs.pane = 1
		rs.scroll += m.rolloutPaneHeight() - 2
		return m, nil
	case "pgup":
		rs.pane = 1
		rs.scroll = max(0, rs.scroll-(m.rolloutPaneHeight()-2))
		return m, nil
	case "home":
		rs.scroll = 0
		return m, nil
	case "w":
		rs.raw = !rs.raw
		rs.scroll = 0
		if rs.raw {
			m.setStatus("diffing the text as stored (w returns to the field view)", false)
		} else {
			m.setStatus("diffing fields; layout changes hidden (w shows the raw text)", false)
		}
		return m, nil
	case "enter":
		title, body := m.rolloutChangeText(0)
		m.showText(title, body)
		return m, nil
	case "R":
		if m.stmt != nil {
			// a refresh re-reads everything, previews included
			rs.previews, rs.previewErrs = map[int]*rollout.Preview{}, map[int]string{}
			return m.execute(lang.StmtString(m.stmt))
		}
	case "s":
		if sp := rs.selectedSpace(); sp != nil {
			return m.rewrite(&lang.SelectStmt{Star: true, From: lang.Source{Entity: "Unit"}, Scope: &lang.Scope{Space: sp.Slug}})
		}
	case "i", "1":
		m.openDetailRow(rs.row)
		if m.det != nil {
			m.det.from = modeRollout
		}
		return m, nil
	case "?":
		m.helpOpen = true
		return m, nil
	case "q":
		return m, tea.Quit
	}
	return m, nil
}

// rolloutBack leaves the mode: the statement it was opened from comes back.
func (m *Model) rolloutBack() {
	if m.roll != nil && m.roll.fromStmt != nil {
		m.stmt, m.plan = m.roll.fromStmt, m.roll.fromPlan
		m.cmd.SetValue(lang.StmtString(m.stmt))
		m.cmd.MoveToEnd()
		m.layout()
	}
	if m.result != nil {
		m.mode = modeResults
		m.focus = focusMain
	} else {
		m.openChooser()
	}
}

// ---- view

var (
	goodStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("2"))
	badStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("1"))
	warnStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("3"))
)

func stateStyle(state string) lipgloss.Style {
	switch state {
	case rollout.StateReady, rollout.StateComplete:
		return goodStyle
	case rollout.StateDegraded, rollout.StateBlocked, rollout.StateAborted:
		return badStyle
	case rollout.StateProgressing:
		return warnStyle
	}
	return dimStyle
}

func (m Model) rolloutView() string {
	w, h := m.mainWidth(), m.mainHeight()
	rs := m.roll
	if rs == nil {
		return dimStyle.Render("no rollout open")
	}
	ro := rs.ro
	var b strings.Builder

	// header
	head := titleStyle.Render(" "+ro.Order.Slug) + "  " + ro.Order.Description
	right := stateStyle(ro.State).Render(ro.State)
	if ro.Workflow != nil {
		if ro.Completed {
			right += dimStyle.Render(" · completed")
		} else {
			right += dimStyle.Render(" · not completed")
		}
	}
	pad := w - lipgloss.Width(head) - lipgloss.Width(right) - 1
	if pad < 1 {
		pad = 1
	}
	b.WriteString(head + strings.Repeat(" ", pad) + right + "\n")
	sub := fmt.Sprintf(" state %s · base %s", ro.Order.State, ro.Order.SpaceSlug)
	if ro.Workflow != nil {
		toGo := 0
		for _, st := range ro.Stages[1:] {
			for _, sp := range st.Spaces {
				if !sp.Taken {
					toGo++
				}
			}
		}
		sub = fmt.Sprintf(" workflow %s · component %s · %d of %d spaces still to take it", ro.WorkflowRef, ro.Component, toGo, len(ro.Order.InScope)-1)
	}
	if ro.Err != "" {
		sub += "  " + errStyle.Render(ro.Err)
	}
	b.WriteString(dimStyle.Render(lipgloss.NewStyle().MaxWidth(w).Render(sub)) + "\n\n")

	// strip
	b.WriteString(m.rolloutStrip(w) + "\n")

	paneH := m.rolloutPaneHeight()
	if rs.confirm != nil {
		b.WriteString(m.confirmView(w, paneH))
		return lipgloss.NewStyle().Width(w).Height(h).MaxWidth(w).MaxHeight(h).Render(b.String())
	}
	lw := w * 2 / 5
	if lw < 30 {
		lw = min(30, w)
	}
	rw := w - lw - 1
	left := lipgloss.NewStyle().Width(lw).Height(paneH).MaxWidth(lw).MaxHeight(paneH).Render(m.rolloutLeft(lw, paneH))
	rightPane := lipgloss.NewStyle().Width(rw).Height(paneH).MaxWidth(rw).MaxHeight(paneH).Render(m.rolloutRight(rw, paneH))
	b.WriteString(lipgloss.JoinHorizontal(lipgloss.Top, left, " ", rightPane))
	return lipgloss.NewStyle().Width(w).Height(h).MaxWidth(w).MaxHeight(h).Render(b.String())
}

// rolloutPaneHeight is the height left for the two panes under the header
// and the strip (two header lines, a blank, five strip lines).
func (m Model) rolloutPaneHeight() int {
	return max(m.mainHeight()-8, 3)
}

// rolloutStrip draws the stages as columns: taken / released / healthy per
// stage, the selected stage highlighted, the next stage marked with the gate
// tally in the CLI's words.
func (m Model) rolloutStrip(w int) string {
	rs := m.roll
	ro := rs.ro
	names := []string{}
	for _, st := range ro.Stages {
		names = append(names, st.Name)
	}
	if ro.Workflow != nil {
		names = append(names, "final")
	}
	cw := 10
	for _, n := range names {
		if len(n)+2 > cw {
			cw = len(n) + 2
		}
	}
	cell := func(s string, style lipgloss.Style) string {
		return style.Render(fmt.Sprintf("%-*s", cw, s))
	}
	rows := [][]string{{"taken"}, {"released"}, {"healthy"}}
	header := []string{fmt.Sprintf("%-10s", "")}
	for i, st := range ro.Stages {
		style := lipgloss.NewStyle()
		if i == rs.stage {
			style = focusStyle
		}
		mark := " "
		if i == ro.Next {
			mark = "▲"
		}
		header = append(header, cell(mark+st.Name, style))
		n := len(st.Spaces)
		taken, released, healthy := st.Counts()
		if st.Source {
			rows[0] = append(rows[0], cell(" ●", goodStyle))
			rows[1] = append(rows[1], cell(" –", dimStyle))
			rows[2] = append(rows[2], cell(" –", dimStyle))
			continue
		}
		releasable := 0
		for _, sp := range st.Spaces {
			if sp.Releasable {
				releasable++
			}
		}
		rows[0] = append(rows[0], cell(fmt.Sprintf(" %d/%d", taken, n), countStyle(taken, n)))
		if releasable == 0 {
			rows[1] = append(rows[1], cell(" –", dimStyle))
			rows[2] = append(rows[2], cell(" –", dimStyle))
		} else {
			rows[1] = append(rows[1], cell(fmt.Sprintf(" %d/%d", released, releasable), countStyle(released, releasable)))
			rows[2] = append(rows[2], cell(fmt.Sprintf(" %d/%d", healthy, releasable), countStyle(healthy, releasable)))
		}
	}
	if ro.Workflow != nil {
		fin := " ·"
		style := dimStyle
		if ro.Completed {
			fin, style = " ✓", goodStyle
		}
		header = append(header, cell(" final", dimStyle))
		rows[0] = append(rows[0], cell(fin, style))
		rows[1] = append(rows[1], "")
		rows[2] = append(rows[2], "")
	}
	var b strings.Builder
	b.WriteString(" " + strings.Join(header, "") + "\n")
	for _, r := range rows {
		b.WriteString(" " + dimStyle.Render(fmt.Sprintf("%-10s", r[0])) + strings.Join(r[1:], "") + "\n")
	}
	// the next hop and its gates, one line
	ok, total := rollout.Tally(ro.Gates)
	switch {
	case ro.Workflow == nil:
		b.WriteString(" " + dimStyle.Render(ro.Blocker))
	case ro.Next > 0:
		line := fmt.Sprintf(" next: %s · gates %d of %d satisfied", ro.NextName(), ok, total)
		if ro.Blocker != rollout.NoBlocker {
			line += " · " + ro.Blocker
		} else {
			line += " · promote is open"
		}
		b.WriteString(lipgloss.NewStyle().MaxWidth(w).Render(line))
	default:
		line := fmt.Sprintf(" every stage has taken it · final %d of %d satisfied", ok, total)
		if !ro.Completed {
			line += " · " + ro.Blocker
		}
		b.WriteString(lipgloss.NewStyle().MaxWidth(w).Render(line))
	}
	return b.String()
}

func countStyle(n, of int) lipgloss.Style {
	switch {
	case of == 0 || n == 0:
		return dimStyle
	case n == of:
		return goodStyle
	}
	return warnStyle
}

// rolloutLeft lists the selected stage's spaces with their bits, then the
// gates: the next stage's entry gates when that stage is selected, else the
// stage's own prerequisites.
func (m Model) rolloutLeft(w, h int) string {
	rs := m.roll
	ro := rs.ro
	st := rs.selectedStage()
	var lines []string
	title := "stage: " + st.Name
	if st.Source {
		title = "source: " + st.Spaces[0].Slug
	}
	lines = append(lines, paneTitle(title, rs.pane == 0))
	bit := func(on bool, glyph string) string {
		if on {
			return goodStyle.Render(glyph)
		}
		return dimStyle.Render("·")
	}
	if st.Source {
		lines = append(lines, dimStyle.Render("the space the change was authored in"))
		if n := len(ro.Order.Skipped); n > 0 {
			lines = append(lines, dimStyle.Render(fmt.Sprintf("%d unit(s) carry no revisions of it (marked, not dropped)", n)))
		}
	} else if len(st.Spaces) == 0 {
		lines = append(lines, errStyle.Render("selects no Space"))
	} else {
		lines = append(lines, dimStyle.Render(fmt.Sprintf("%-3s %-3s %-3s  %s", "T", "R", "H", "space")))
		for i, sp := range st.Spaces {
			cur := "  "
			style := lipgloss.NewStyle()
			if i == rs.space {
				cur = "▸ "
				style = focusStyle
			}
			// healthy means: released this change, and the live status is good.
			// Before the release the live status describes the previous state.
			health := bit(sp.HealthyForChange(), "✓")
			if sp.Released && sp.Health.Present && !sp.Health.OK() {
				health = badStyle.Render("✗")
			}
			if !sp.Releasable {
				health = dimStyle.Render("–")
			}
			rel := bit(sp.Released, "✓")
			if !sp.Releasable {
				rel = dimStyle.Render("–")
			}
			lines = append(lines, cur+bit(sp.Taken, "✓")+"   "+rel+"   "+health+"    "+style.Render(sp.Slug))
		}
		if sp := rs.selectedSpace(); sp != nil && sp.Health.Present {
			obs := sp.Health.Status
			if sp.Health.ObservedAt != "" {
				obs += " at " + sp.Health.ObservedAt
			}
			if sp.Health.Message != "" {
				obs += " · " + sp.Health.Message
			}
			if !sp.Released {
				obs += " (before this change; not counted until released)"
			}
			lines = append(lines, dimStyle.Render(lipgloss.NewStyle().MaxWidth(w-2).Render("  live: "+obs)))
		}
	}
	lines = append(lines, "")
	if ro.Workflow != nil {
		switch {
		case rs.stage == ro.Next:
			ok, total := rollout.Tally(ro.Gates)
			lines = append(lines, titleStyle.Render(fmt.Sprintf("gates on %s: %d of %d satisfied", st.Name, ok, total)))
			for _, g := range ro.Gates {
				if g.OK {
					reason := g.Reason
					if reason == "" {
						reason = g.Name
					}
					lines = append(lines, " "+goodStyle.Render("✓")+" "+reason)
				} else {
					lines = append(lines, " "+badStyle.Render("✗")+" "+g.Reason)
				}
			}
		case !st.Source && rs.stage > 0:
			pre := strings.Join(st.Prerequisites, ", ")
			if pre == "" {
				pre = "none beyond taken"
			}
			lines = append(lines, dimStyle.Render("entry gates: taken, "+pre))
		}
	}
	return strings.Join(lines, "\n")
}

// rolloutRight shows the change for the selected space, scrolled by
// PgUp/PgDn, with a footer saying how much is out of view.
func (m Model) rolloutRight(w, h int) string {
	title, body := m.rolloutChangeText(w)
	lines := strings.Split(body, "\n")
	avail := h - 2 // title and footer
	if avail < 1 {
		avail = 1
	}
	rs := m.roll
	if rs.scroll > len(lines)-avail {
		rs.scroll = max(0, len(lines)-avail)
	}
	end := min(len(lines), rs.scroll+avail)
	shown := lines[rs.scroll:end]
	footer := ""
	switch {
	case rs.scroll > 0 && end < len(lines):
		footer = dimStyle.Render(fmt.Sprintf("… lines %d–%d of %d · PgUp/PgDn", rs.scroll+1, end, len(lines)))
	case end < len(lines):
		footer = dimStyle.Render(fmt.Sprintf("… %d more lines · ↓ scrolls, ⏎ opens the full diff", len(lines)-end))
	case rs.scroll > 0:
		footer = dimStyle.Render(fmt.Sprintf("… end · lines %d–%d of %d · PgUp", rs.scroll+1, end, len(lines)))
	}
	if footer != "" && rs.pane == 0 {
		footer += dimStyle.Render(" · Tab focuses this pane, then ↑↓ scroll")
	}
	return paneTitle(title, rs.pane == 1) + "\n" + strings.Join(shown, "\n") + "\n" + footer
}

// fieldLine renders one changed field: path, then old → new in diff colours.
func fieldLine(f rollout.FieldChange) string {
	p := f.Path
	if f.Doc != "" {
		p = dimStyle.Render(f.Doc+" ") + p
	}
	switch {
	case f.Before == "":
		return p + ": " + addStyle.Render(f.After) + dimStyle.Render("  (added)")
	case f.After == "":
		return p + ": " + delStyle.Render(f.Before) + dimStyle.Render("  (removed)")
	}
	return p + ": " + delStyle.Render(f.Before) + " → " + addStyle.Render(f.After)
}

// paneTitle marks the focused pane the way the command area is marked.
func paneTitle(s string, focused bool) string {
	if focused {
		return focusStyle.Render("▎" + s)
	}
	return titleStyle.Render(" " + s)
}

// rolloutChangeText renders the selected space's change: the diff per touched
// unit, then the untouched units. width 0 means unbounded (the text view).
func (m Model) rolloutChangeText(w int) (string, string) {
	rs := m.roll
	sp := rs.selectedSpace()
	if sp == nil {
		return "no space selected", ""
	}
	st := rs.selectedStage()
	switch {
	case !sp.Taken:
		return m.previewText(w)
	case rs.pending[sp.ID]:
		return "the change in " + sp.Slug, dimStyle.Render("loading…")
	}
	if e, ok := rs.errs[sp.ID]; ok {
		return "the change in " + sp.Slug, errStyle.Render(e)
	}
	changes, ok := rs.changes[sp.ID]
	if !ok {
		return "the change in " + sp.Slug, dimStyle.Render("…")
	}
	title := "the change in " + sp.Slug
	if st.Source {
		title = "the ordered change · " + sp.Slug
	}
	var out []string
	var untouched []string
	for _, u := range changes {
		if !u.Touched {
			untouched = append(untouched, u.Slug)
			continue
		}
		head := titleStyle.Render(u.Slug) + dimStyle.Render(fmt.Sprintf("  rev %d → %d", u.StartRev, u.EndRev))
		if u.Err != "" {
			out = append(out, head, errStyle.Render(u.Err))
			continue
		}
		labelA, labelB := fmt.Sprintf("%s @%d", u.Slug, u.StartRev), fmt.Sprintf("%s @%d", u.Slug, u.EndRev)
		if rs.raw {
			out = append(out, head+dimStyle.Render("  · raw text"), renderUnified(u.Before, u.After, labelA, labelB), "")
			continue
		}
		switch {
		case u.FormattingOnly:
			out = append(out, head+dimStyle.Render("  · layout only"), dimStyle.Render("no field changed; the text differs in indentation or quoting only (w shows it)"), "")
			continue
		case len(u.Fields) == 1:
			head += dimStyle.Render("  · 1 field")
		default:
			head += dimStyle.Render(fmt.Sprintf("  · %d fields", len(u.Fields)))
		}
		out = append(out, head)
		for _, f := range u.Fields {
			line := "  " + fieldLine(f)
			if w > 0 && lipgloss.Width(line) > w-2 {
				// too wide for the pane: path on one line, the values under it
				p := f.Path
				if f.Doc != "" {
					p = dimStyle.Render(f.Doc+" ") + p
				}
				out = append(out, "  "+p)
				if f.Before != "" {
					out = append(out, "    "+delStyle.Render("- "+f.Before))
				}
				if f.After != "" {
					out = append(out, "    "+addStyle.Render("+ "+f.After))
				}
				continue
			}
			out = append(out, line)
		}
		out = append(out, "", renderUnified(u.NormBefore, u.NormAfter, labelA, labelB), "")
	}
	if len(out) == 0 {
		out = append(out, dimStyle.Render("no unit changed here"))
	}
	if len(untouched) > 0 {
		out = append(out, dimStyle.Render("no change: "+strings.Join(untouched, ", ")))
	}
	body := strings.Join(out, "\n")
	if w > 0 {
		body = lipgloss.NewStyle().MaxWidth(w).Render(body)
	}
	return title, body
}

// RolloutRunner turns the list rows of a ChangeOrder statement into a
// rollout message (rollout step) or enriches them with the computed columns.
// Shared by the real runner and the test stubs.
func RolloutRunner(ctx context.Context, c rollout.Client, st *lang.SelectStmt, p *plan.Plan, rows []cubclient.Row) (tea.Msg, error) {
	cache := rollout.NewCache() // per run: live status must not go stale across refreshes
	if p.Rollout != nil {
		if len(rows) != 1 {
			return nil, fmt.Errorf("rollout opens one change order; the where steps matched %d", len(rows))
		}
		ro, err := rollout.Load(ctx, c, cache, rows[0])
		if err != nil {
			return nil, err
		}
		return rolloutMsg{stmt: st, plan: p, ro: ro, row: rows[0]}, nil
	}
	if p.RolloutCols {
		// One derivation per order, a few at a time: each is several round
		// trips, and the orders do not depend on each other.
		sem := make(chan struct{}, 6)
		var wg sync.WaitGroup
		for _, row := range rows {
			wg.Add(1)
			go func(row cubclient.Row) {
				defer wg.Done()
				sem <- struct{}{}
				defer func() { <-sem }()
				ro, err := rollout.Load(ctx, c, cache, row)
				if err != nil {
					row["Rollout"] = map[string]any{"state": rollout.StateUnknown, "stage": "", "next": "", "blocker": err.Error()}
					return
				}
				row["Rollout"] = map[string]any{"state": ro.State, "stage": ro.Reached(), "next": ro.NextName(), "blocker": ro.Blocker}
			}(row)
		}
		wg.Wait()
	}
	return nil, nil
}

// RolloutsPreset is the chooser's rollouts-in-flight statement.
const RolloutsPreset = "ChangeOrder | in * | where State IN ('New', 'InProgress', 'Resolved') | columns Slug, Space.Slug, state(), stage(), next(), blocker(), CreatedAt | order by CreatedAt desc"

// ---- preview and promote

// PreviewLoader dry-runs a stage (rollout.PreviewStage); Promoter runs it;
// Releaser publishes it (rollout.ReleaseStage), after the given promote
// outcomes when B ran both.
type PreviewLoader func(ctx context.Context, ro *rollout.Rollout, stage int) (*rollout.Preview, error)
type Promoter func(ctx context.Context, ro *rollout.Rollout, stage int) ([]rollout.Outcome, error)
type Releaser func(ctx context.Context, ro *rollout.Rollout, stage int, promoted []rollout.Outcome) ([]rollout.ReleaseOutcome, error)

type previewMsg struct {
	orderID string
	stage   int
	p       *rollout.Preview
	err     error
}

// actionMsg is a write's result: the report to show once the refreshed
// reading has landed.
type actionMsg struct {
	orderID string
	title   string
	report  string
	err     error
}

// confirmState is the overlay a write waits behind: what will run, in cub
// terms, until y; run does it and writes the report.
type confirmState struct {
	title string
	lines []string
	stage int
	run   func(ctx context.Context) (string, error)
}

// previewFetch dry-runs the selected stage when its selected space has not
// taken the change and no preview is loaded yet.
func (m *Model) previewFetch() tea.Cmd {
	rs := m.roll
	if rs == nil || m.previewLoader == nil || rs.stage == 0 || rs.ro.Workflow == nil {
		return nil
	}
	sp := rs.selectedSpace()
	if sp == nil || sp.Taken || rs.previewPending[rs.stage] {
		return nil
	}
	if _, ok := rs.previews[rs.stage]; ok {
		return nil
	}
	if _, ok := rs.previewErrs[rs.stage]; ok {
		return nil
	}
	rs.previewPending[rs.stage] = true
	loader, ro, stage := m.previewLoader, rs.ro, rs.stage
	return func() tea.Msg {
		p, err := loader(context.Background(), ro, stage)
		return previewMsg{orderID: ro.Order.ID, stage: stage, p: p, err: err}
	}
}

func (m *Model) previewLoaded(msg previewMsg) {
	rs := m.roll
	if rs == nil || rs.ro.Order.ID != msg.orderID {
		return
	}
	delete(rs.previewPending, msg.stage)
	if msg.err != nil {
		rs.previewErrs[msg.stage] = msg.err.Error()
		return
	}
	rs.previews[msg.stage] = msg.p
}

// previewText renders the dry run for the selected space.
func (m Model) previewText(w int) (string, string) {
	rs := m.roll
	sp := rs.selectedSpace()
	st := rs.selectedStage()
	title := "what this promotes to " + sp.Slug
	if rs.previewPending[rs.stage] {
		return title, dimStyle.Render("running the dry run…")
	}
	if e, ok := rs.previewErrs[rs.stage]; ok {
		return title, errStyle.Render("dry run failed: "+e) + "\n" + dimStyle.Render("R retries")
	}
	p, ok := rs.previews[rs.stage]
	if !ok {
		if rs.ro.Workflow == nil {
			return title, dimStyle.Render("no ChangeWorkflow governs this change order")
		}
		return title, dimStyle.Render("…")
	}
	var spv *rollout.SpacePreview
	for i := range p.Spaces {
		if p.Spaces[i].Space.ID == sp.ID {
			spv = &p.Spaces[i]
		}
	}
	if spv == nil {
		return title, dimStyle.Render("not part of this stage's preview")
	}
	var out []string
	if spv.Skipped != "" {
		return title, dimStyle.Render(spv.Skipped)
	}
	if spv.Err != "" {
		return title, errStyle.Render(spv.Err)
	}
	if len(spv.Missing) > 0 {
		out = append(out, badStyle.Render("✗ lacks "+strings.Join(spv.Missing, ", ")+": the upgrade cannot clone units; promote from the CLI, which does"))
	}
	var unchanged []string
	for _, u := range spv.Units {
		if u.Err != "" {
			out = append(out, titleStyle.Render(u.Slug)+"  "+errStyle.Render(u.Err))
			continue
		}
		if u.NoChange && len(u.Kept) == 0 {
			unchanged = append(unchanged, u.Slug)
			continue
		}
		head := titleStyle.Render(u.Slug)
		switch {
		case u.NoChange:
			head += dimStyle.Render("  · nothing changes")
		case len(u.Fields) == 1:
			head += dimStyle.Render("  · 1 field")
		default:
			head += dimStyle.Render(fmt.Sprintf("  · %d fields", len(u.Fields)))
		}
		if n := len(u.Kept); n == 1 {
			head += warnStyle.Render("  · 1 kept")
		} else if n > 1 {
			head += warnStyle.Render(fmt.Sprintf("  · %d kept", n))
		}
		out = append(out, head)
		out = append(out, fieldLines(u.Fields, w)...)
		out = append(out, keptLines(u.Kept, w)...)
		if !u.NoChange {
			out = append(out, "", renderUnified(u.NormBefore, u.NormAfter, u.Slug+" now", u.Slug+" after promote"))
		}
		out = append(out, "")
	}
	if len(out) == 0 {
		out = append(out, dimStyle.Render("nothing would change here"))
	}
	if len(unchanged) > 0 {
		out = append(out, dimStyle.Render("no change: "+strings.Join(unchanged, ", ")))
	}
	gate := ""
	switch {
	case rs.stage != rs.ro.Next:
		gate = dimStyle.Render(fmt.Sprintf("not the next stage (next is %s)", rs.ro.NextName()))
	case !rollout.Open(rs.ro.Gates):
		gate = badStyle.Render("promote refused: " + rs.ro.Blocker)
	case len(p.Blockers()) > 0:
		gate = badStyle.Render("promote refused: " + strings.Join(p.Blockers(), "; "))
	default:
		gate = goodStyle.Render("P promotes this stage")
	}
	out = append(out, "", gate, dimStyle.Render(fmt.Sprintf("  cub variant promote --change-order %s", rs.ro.Order.Ref())),
		dimStyle.Render(fmt.Sprintf("      --target-stage %s --dry-run -o mutations", st.Name)))
	body := strings.Join(out, "\n")
	if w > 0 {
		body = lipgloss.NewStyle().MaxWidth(w).Render(body)
	}
	return title, body
}

// keptLines renders the upstream changes this merge will NOT bring, loudly:
// the space keeps its value, and the reason when a protection is recorded.
func keptLines(kept []rollout.KeptField, w int) []string {
	var out []string
	for _, k := range kept {
		p := k.Path
		if k.Doc != "" {
			p = dimStyle.Render(k.Doc+" ") + p
		}
		why := "kept as a local override"
		if k.Protected {
			why = "protected: a merge must not overwrite it"
		}
		line := "  " + warnStyle.Render("⚠ NOT changed") + "  " + p + ": stays " + warnStyle.Render(k.Current) + dimStyle.Render("  (upstream set "+k.Upstream+"; "+why+")")
		if w > 0 && lipgloss.Width(line) > w-2 {
			out = append(out, "  "+warnStyle.Render("⚠ NOT changed")+"  "+p,
				"      stays "+warnStyle.Render(k.Current)+dimStyle.Render("  · upstream set "+k.Upstream),
				dimStyle.Render("      "+why))
			continue
		}
		out = append(out, line)
	}
	return out
}

// fieldLines renders changed fields, wrapping the wide ones.
func fieldLines(fields []rollout.FieldChange, w int) []string {
	var out []string
	for _, f := range fields {
		line := "  " + fieldLine(f)
		if w > 0 && lipgloss.Width(line) > w-2 {
			p := f.Path
			if f.Doc != "" {
				p = dimStyle.Render(f.Doc+" ") + p
			}
			out = append(out, "  "+p)
			if f.Before != "" {
				out = append(out, "    "+delStyle.Render("- "+f.Before))
			}
			if f.After != "" {
				out = append(out, "    "+addStyle.Render("+ "+f.After))
			}
			continue
		}
		out = append(out, line)
	}
	return out
}

// promoteRequest is P: refuse with the reason, or open the confirm overlay.
func (m Model) promoteRequest() (tea.Model, tea.Cmd) {
	rs := m.roll
	ro := rs.ro
	switch {
	case m.promoter == nil:
		m.setStatus("promote is not wired in this build", true)
	case ro.Workflow == nil:
		m.setStatus("no ChangeWorkflow governs this change order; promote it per space from the CLI", true)
	case ro.Order.AbortedReason != "":
		m.setStatus("aborted: "+ro.Order.AbortedReason, true)
	case rs.stage == 0:
		m.setStatus("the source already has the change; → picks the stage to promote into", true)
	case ro.Next < 0:
		m.setStatus("every stage has taken this change", true)
	case rs.stage != ro.Next:
		m.setStatus(fmt.Sprintf("not the next stage: %s is (→/← to select it)", ro.NextName()), true)
	case !rollout.Open(ro.Gates):
		m.setStatus("promote refused: "+ro.Blocker, true)
	case rs.previewPending[rs.stage]:
		m.setStatus("the dry run is still running; try again in a moment", true)
	default:
		p, ok := rs.previews[rs.stage]
		if !ok {
			m.setStatus("no preview yet: "+firstNonEmpty(rs.previewErrs[rs.stage], "select a space in the stage first"), true)
			return m, m.previewFetch()
		}
		if b := p.Blockers(); len(b) > 0 {
			m.setStatus("promote refused: "+strings.Join(b, "; "), true)
			return m, nil
		}
		st := rs.selectedStage()
		units, fields := p.Changed()
		lines := []string{
			fmt.Sprintf("Promote %s into stage %s", ro.Order.Slug, st.Name),
			"",
		}
		for _, spv := range p.Spaces {
			if spv.Skipped != "" {
				lines = append(lines, dimStyle.Render(fmt.Sprintf("  %-40s skipped: %s", spv.Space.Slug, spv.Skipped)))
				continue
			}
			n, kept := 0, 0
			for _, u := range spv.Units {
				if !u.NoChange && u.Err == "" {
					n++
				}
				kept += len(u.Kept)
			}
			line := fmt.Sprintf("  %-40s %d unit(s) change, %d covered", spv.Space.Slug, n, len(spv.Units))
			if kept > 0 {
				line += warnStyle.Render(fmt.Sprintf("  · %d field(s) NOT changed (kept)", kept))
			}
			lines = append(lines, line)
		}
		lines = append(lines, "", fmt.Sprintf("%d unit(s), %d field(s) change across the stage. Promotion moves configuration only; it publishes no release.", units, fields), "", "Runs, per space:")
		for _, spv := range p.Spaces {
			if spv.Skipped == "" {
				lines = append(lines, "  PATCH /api/unit?"+promoteQueryString(ro, spv.Space.ID))
			}
		}
		lines = append(lines, "", "Equivalent:", "  "+strings.Join(rollout.PromoteCommands(ro, rs.stage), "\n  "), "",
			focusStyle.Render("y")+" promote   "+dimStyle.Render("any other key cancels"))
		promoter, stage := m.promoter, rs.stage
		rs.confirm = &confirmState{title: "Promote " + st.Name, lines: lines, stage: rs.stage, run: func(ctx context.Context) (string, error) {
			out, err := promoter(ctx, ro, stage)
			return promoteReport(ro, stage, out, err), err
		}}
	}
	return m, nil
}

// releasable says whether L has anything to do on the selected stage, or why not.
func (m Model) releasable() (ok bool, reason string) {
	rs := m.roll
	ro := rs.ro
	switch {
	case m.releaser == nil:
		return false, "release is not wired in this build"
	case ro.Workflow == nil:
		return false, "no ChangeWorkflow governs this change order"
	case rs.stage == 0:
		return false, "the source is the base; it releases nothing"
	}
	st := rs.selectedStage()
	targets, taken, released := 0, 0, 0
	for _, sp := range st.Spaces {
		if sp.ID == ro.Order.SpaceID || !sp.Releasable {
			continue
		}
		targets++
		if sp.Taken {
			taken++
		}
		if sp.Released {
			released++
		}
	}
	switch {
	case targets == 0:
		return false, fmt.Sprintf("nothing to release in %s: its spaces have no release target", st.Name)
	case taken == 0:
		return false, fmt.Sprintf("%s has not taken the change yet; promote first (P)", st.Name)
	case released == taken:
		return false, fmt.Sprintf("%s has released this change already", st.Name)
	}
	return true, ""
}

// releaseRequest is L: publish the selected stage's releases, pinned to the
// change order's end tag, behind the confirm overlay.
func (m Model) releaseRequest() (tea.Model, tea.Cmd) {
	rs := m.roll
	ro := rs.ro
	if ok, reason := m.releasable(); !ok {
		m.setStatus(reason, true)
		return m, nil
	}
	st := rs.selectedStage()
	lines := []string{fmt.Sprintf("Release %s from stage %s", ro.Order.Slug, st.Name), ""}
	for _, sp := range st.Spaces {
		switch {
		case sp.ID == ro.Order.SpaceID || !sp.Releasable:
			lines = append(lines, dimStyle.Render(fmt.Sprintf("  %-40s skipped: no release target", sp.Slug)))
		case !sp.Taken:
			lines = append(lines, dimStyle.Render(fmt.Sprintf("  %-40s skipped: has not taken the change", sp.Slug)))
		case sp.Released:
			lines = append(lines, dimStyle.Render(fmt.Sprintf("  %-40s skipped: already released", sp.Slug)))
		default:
			lines = append(lines, fmt.Sprintf("  %-40s publish, pinned to the change order's end tag", sp.Slug))
		}
	}
	lines = append(lines, "", "Each publish waits for the awaiting/triggers gate the promotion left on the units to clear (up to "+rollout.TriggerWait.String()+"), then runs:")
	for _, sp := range st.Spaces {
		if sp.ID != ro.Order.SpaceID && sp.Releasable && sp.Taken && !sp.Released {
			lines = append(lines, fmt.Sprintf("  POST /api/space/%s/release  {\"TagID\": \"%s\"}", sp.ID, ro.Order.EndTagID))
		}
	}
	lines = append(lines, "", "Equivalent:", "  "+strings.Join(rollout.ReleaseCommands(ro, rs.stage), "\n  "), "",
		focusStyle.Render("y")+" release   "+dimStyle.Render("any other key cancels"))
	releaser, stage := m.releaser, rs.stage
	rs.confirm = &confirmState{title: "Release " + st.Name, lines: lines, stage: rs.stage, run: func(ctx context.Context) (string, error) {
		out, err := releaser(ctx, ro, stage, nil)
		return releaseReport(ro, stage, out, err), err
	}}
	return m, nil
}

// bothRequest is B: promote the stage, then release it, one confirm.
func (m Model) bothRequest() (tea.Model, tea.Cmd) {
	rs := m.roll
	ro := rs.ro
	if m.releaser == nil {
		m.setStatus("release is not wired in this build", true)
		return m, nil
	}
	if rs.stage > 0 && rs.stage < len(ro.Stages) {
		targets := 0
		for _, sp := range rs.selectedStage().Spaces {
			if sp.ID != ro.Order.SpaceID && sp.Releasable {
				targets++
			}
		}
		if targets == 0 {
			m.setStatus(fmt.Sprintf("%s has no release targets; P promotes it, there is nothing to release", rs.selectedStage().Name), true)
			return m, nil
		}
	}
	model, cmd := m.promoteRequest()
	mm := model.(Model)
	if mm.roll.confirm == nil {
		return mm, cmd
	}
	c := mm.roll.confirm
	st := ro.Stages[c.stage]
	c.title = "Promote and release " + st.Name
	// swap the trailing prompt and add the release half
	c.lines = c.lines[:len(c.lines)-1]
	c.lines = append(c.lines, "Then, for each space with a release target, once its awaiting/triggers gate clears:")
	for _, sp := range st.Spaces {
		if sp.ID != ro.Order.SpaceID && sp.Releasable {
			c.lines = append(c.lines, fmt.Sprintf("  cub release publish --revision ChangeOrder:%s %s", ro.Order.Ref(), sp.Slug))
		}
	}
	c.lines = append(c.lines, "", focusStyle.Render("y")+" promote and release   "+dimStyle.Render("any other key cancels"))
	promoter, releaser, stage := mm.promoter, mm.releaser, c.stage
	c.run = func(ctx context.Context) (string, error) {
		out, err := promoter(ctx, ro, stage)
		report := promoteReport(ro, stage, out, err)
		if err != nil {
			return report, err
		}
		rel, rerr := releaser(ctx, ro, stage, out)
		return report + "\n" + releaseReport(ro, stage, rel, rerr), rerr
	}
	return mm, cmd
}

func promoteQueryString(ro *rollout.Rollout, spaceID string) string {
	return fmt.Sprintf("where=SpaceID = '%s' AND UpstreamUnitID IS NOT NULL&upgrade=true&change_order=%s", spaceID, ro.Order.ID)
}

// confirmKey owns the keys while the overlay is up.
func (m Model) confirmKey(k tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	rs := m.roll
	c := rs.confirm
	rs.confirm = nil
	if k.String() != "y" {
		m.setStatus("cancelled", false)
		return m, nil
	}
	ro := rs.ro
	m.setStatus(c.title+"…", false)
	return m, func() tea.Msg {
		report, err := c.run(context.Background())
		return actionMsg{orderID: ro.Order.ID, title: c.title, report: report, err: err}
	}
}

// actionDone records the report and refreshes the reading; the report opens
// once the refreshed rollout has landed (rolloutLoaded).
func (m Model) actionDone(msg actionMsg) (tea.Model, tea.Cmd) {
	rs := m.roll
	if rs == nil || rs.ro.Order.ID != msg.orderID {
		return m, nil
	}
	rs.report = msg.report + "\nThe reading below is refreshed from the server; Esc returns to it.\n"
	rs.reportTitle = msg.title
	if m.stmt != nil {
		return m.execute(lang.StmtString(m.stmt))
	}
	return m, nil
}

func promoteReport(ro *rollout.Rollout, stage int, outcomes []rollout.Outcome, err error) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Promoted %s into stage %s\n\n", ro.Order.Slug, ro.Stages[stage].Name)
	if err != nil {
		fmt.Fprintf(&b, "%s\n", errStyle.Render(err.Error()))
	}
	landed, failed := 0, 0
	for _, o := range outcomes {
		switch {
		case o.Skipped != "":
			fmt.Fprintf(&b, "  %-40s skipped: %s\n", o.Space.Slug, o.Skipped)
		case o.Err != "":
			failed++
			fmt.Fprintf(&b, "  %-40s %s\n", o.Space.Slug, errStyle.Render("failed: "+o.Err))
		default:
			if len(o.Errors) > 0 {
				failed++
			} else {
				landed++
			}
			fmt.Fprintf(&b, "  %-40s %d unit(s) processed, %d failed (HTTP %d)\n", o.Space.Slug, o.Changed, len(o.Errors), o.Status)
			for _, e := range o.Errors {
				fmt.Fprintf(&b, "      %s\n", errStyle.Render(e))
			}
		}
	}
	fmt.Fprintf(&b, "\n%d space(s) landed, %d failed. Running the promotion again is safe: units already carrying the end tag are passed over.\n", landed, failed)
	return b.String()
}

func releaseReport(ro *rollout.Rollout, stage int, outcomes []rollout.ReleaseOutcome, err error) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Released %s from stage %s\n\n", ro.Order.Slug, ro.Stages[stage].Name)
	if err != nil {
		fmt.Fprintf(&b, "%s\n", errStyle.Render(err.Error()))
	}
	published, failed := 0, 0
	for _, o := range outcomes {
		switch {
		case o.Skipped != "":
			fmt.Fprintf(&b, "  %-40s skipped: %s\n", o.Space.Slug, o.Skipped)
		case o.Err != "":
			failed++
			fmt.Fprintf(&b, "  %-40s %s\n", o.Space.Slug, errStyle.Render("failed: "+o.Err))
		default:
			published++
			waited := ""
			if o.WaitedFor > time.Second {
				waited = fmt.Sprintf(" (waited %s for triggers)", o.WaitedFor)
			}
			fmt.Fprintf(&b, "  %-40s published release %s%s\n", o.Space.Slug, firstNonEmpty(o.ReleaseNum, o.ReleaseID), waited)
		}
	}
	fmt.Fprintf(&b, "\n%d release(s) published, %d failed. Each is pinned to the change order's end tag, so it describes this change.\n", published, failed)
	return b.String()
}

// confirmView draws the overlay in place of the panes.
func (m Model) confirmView(w, h int) string {
	c := m.roll.confirm
	body := titleStyle.Render(" "+c.title) + "\n\n " + strings.Join(c.lines, "\n ")
	return lipgloss.NewStyle().Width(w).Height(h).MaxWidth(w).MaxHeight(h).Render(body)
}
