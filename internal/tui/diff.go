package tui

import (
	"context"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/confighub/cub-commander/internal/cubclient"
	"github.com/confighub/cub-commander/internal/exec"
	"github.com/confighub/cub-commander/internal/lang"
	"github.com/confighub/cub-commander/internal/plan"
)

// The diff view: pairs on the left, the selected pair's unified data diff on
// the right. Data is fetched per pair as it is shown, never for the whole set.

type diffState struct {
	res        *exec.DiffResult
	cur        int
	differOnly bool
	visible    []int             // indexes into res.Pairs after the filter
	data       map[string]string // unitID → data
	pending    map[string]bool
	scroll     int
}

type diffMsg struct {
	src  string
	stmt *lang.SelectStmt
	plan *plan.Plan
	res  *exec.DiffResult
}
type dataMsg struct {
	unitID string
	text   string
	err    error
}

// DataFetcher loads one unit's data; injected so tests run offline.
type DataFetcher func(ctx context.Context, row cubclient.Row) (string, error)

var (
	addStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("2"))
	delStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("1"))
	hunkStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("6")).Faint(true)
)

func (m *Model) startDiff(res *exec.DiffResult) {
	m.diff = &diffState{res: res, data: map[string]string{}, pending: map[string]bool{}}
	m.diff.refilter()
	m.mode = modeDiff
	m.focus = focusMain
	// Land on the first pair that differs, if any.
	for i, idx := range m.diff.visible {
		if s := res.Pairs[idx].Status; s == "differ" || s == "multi" {
			m.diff.cur = i
			break
		}
	}
	m.setStatus(diffSummary(res), false)
}

func diffSummary(res *exec.DiffResult) string {
	keys := make([]string, len(res.By))
	for i, k := range res.By {
		keys[i] = k.Path
	}
	return fmt.Sprintf("%d pairs: %d differ, %d same, %d only in A, %d only in B, %d n:m · paired by %s · A %d units, B %d units",
		len(res.Pairs), res.Counts["differ"], res.Counts["same"], res.Counts["only-a"], res.Counts["only-b"], res.Counts["multi"], strings.Join(keys, ", "), res.ARows, res.BRows)
}

func (d *diffState) refilter() {
	d.visible = d.visible[:0]
	for i, p := range d.res.Pairs {
		if d.differOnly && p.Status == "same" {
			continue
		}
		d.visible = append(d.visible, i)
	}
	if d.cur >= len(d.visible) {
		d.cur = max(0, len(d.visible)-1)
	}
}

func (d *diffState) current() *exec.Pair {
	if len(d.visible) == 0 {
		return nil
	}
	return &d.res.Pairs[d.visible[d.cur]]
}

func unitID(r cubclient.Row) string {
	own, _ := r["Unit"].(map[string]any)
	id, _ := own["UnitID"].(string)
	return id
}

// dataFetch loads the data of the current pair's two representative units.
func (m *Model) dataFetch() tea.Cmd {
	if m.diff == nil || m.dataFetcher == nil {
		return nil
	}
	p := m.diff.current()
	if p == nil || p.Status == "same" {
		return nil
	}
	var cmds []tea.Cmd
	for _, rows := range [][]cubclient.Row{p.A, p.B} {
		if len(rows) == 0 {
			continue
		}
		row := rows[0]
		id := unitID(row)
		if _, ok := m.diff.data[id]; ok || m.diff.pending[id] {
			continue
		}
		m.diff.pending[id] = true
		fetch := m.dataFetcher
		cmds = append(cmds, func() tea.Msg {
			text, err := fetch(context.Background(), row)
			return dataMsg{unitID: id, text: text, err: err}
		})
	}
	if len(cmds) == 0 {
		return nil
	}
	return tea.Batch(cmds...)
}

func (m Model) diffKey(k tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	d := m.diff
	if d == nil {
		return m, nil
	}
	switch k.String() {
	case "up", "k":
		if d.cur > 0 {
			d.cur--
			d.scroll = 0
		}
	case "down", "j":
		if d.cur < len(d.visible)-1 {
			d.cur++
			d.scroll = 0
		}
	case "n":
		for i := d.cur + 1; i < len(d.visible); i++ {
			if s := d.res.Pairs[d.visible[i]].Status; s != "same" {
				d.cur, d.scroll = i, 0
				break
			}
		}
	case "p":
		for i := d.cur - 1; i >= 0; i-- {
			if s := d.res.Pairs[d.visible[i]].Status; s != "same" {
				d.cur, d.scroll = i, 0
				break
			}
		}
	case "=":
		d.differOnly = !d.differOnly
		d.refilter()
	case "pgdown", " ":
		d.scroll += m.mainHeight() - 4
	case "pgup":
		d.scroll = max(0, d.scroll-(m.mainHeight()-4))
	case "enter":
		if p := d.current(); p != nil {
			m.showText("diff "+p.Key, m.renderPairDiff(p, m.mainWidth()-2, false))
		}
	case "q":
		return m, tea.Quit
	case "?":
		m.helpOpen = true
	}
	return m, m.dataFetch()
}

func statusGlyph(s string) string {
	switch s {
	case "same":
		return dimStyle.Render("=")
	case "differ":
		return localChipStyle.Render("≠")
	case "only-a":
		return delStyle.Render("A")
	case "only-b":
		return addStyle.Render("B")
	case "multi":
		return chipStyle.Render("n:m")
	}
	return "?"
}

func (m Model) diffView() string {
	d := m.diff
	w, h := m.mainWidth(), m.mainHeight()
	if d == nil {
		return ""
	}
	lw := w * 2 / 5
	if lw < 30 {
		lw = 30
	}
	rw := w - lw - 1

	// Left: the pairs.
	var left []string
	title := fmt.Sprintf("Pairs (%d)", len(d.visible))
	if d.differOnly {
		title += " ≠ only"
	}
	left = append(left, titleStyle.Render(title)+dimStyle.Render("  = toggles same"))
	body := h - 1
	start := 0
	if d.cur >= body {
		start = d.cur - body + 1
	}
	for i := start; i < len(d.visible) && i < start+body; i++ {
		p := d.res.Pairs[d.visible[i]]
		glyph := statusGlyph(p.Status)
		label := p.Key
		if p.Status == "multi" {
			label += fmt.Sprintf(" (%d vs %d)", len(p.A), len(p.B))
		}
		avail := lw - 6
		if len(label) > avail && avail > 1 {
			label = label[:avail-1] + "…"
		}
		line := fmt.Sprintf(" %-3s %s", glyph, label)
		if i == d.cur {
			line = keyStyle.Render(fmt.Sprintf(" %-3s %s", strings.TrimSpace(stripANSI(glyph)), label))
		}
		left = append(left, line)
	}
	leftCol := lipgloss.NewStyle().Width(lw).Height(h).MaxWidth(lw).MaxHeight(h).Render(strings.Join(left, "\n"))

	// Right: the selected pair.
	var right string
	if p := d.current(); p != nil {
		right = m.renderPairDiff(p, rw, true)
	}
	rightCol := lipgloss.NewStyle().Width(rw).Height(h).MaxWidth(rw).MaxHeight(h).Render(right)
	sep := dimStyle.Render(strings.Repeat("▏\n", h))
	return lipgloss.JoinHorizontal(lipgloss.Top, leftCol, sep, rightCol)
}

func stripANSI(s string) string {
	var b strings.Builder
	in := false
	for _, r := range s {
		switch {
		case r == 0x1b:
			in = true
		case in && r == 'm':
			in = false
		case !in:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// renderPairDiff renders one pair: a header naming both sides, then the
// unified diff of their data (or a note for one-sided and n:m pairs).
func (m Model) renderPairDiff(p *exec.Pair, width int, windowed bool) string {
	d := m.diff
	var b strings.Builder
	names := func(rows []cubclient.Row) string {
		var out []string
		for _, r := range rows {
			out = append(out, exec.RowName(r))
		}
		return strings.Join(out, ", ")
	}
	fmt.Fprintf(&b, "%s  %s\n", titleStyle.Render(p.Key), statusGlyph(p.Status))
	fmt.Fprintf(&b, "%s %s\n", delStyle.Render("A"), names(p.A))
	fmt.Fprintf(&b, "%s %s\n", addStyle.Render("B"), names(p.B))
	switch p.Status {
	case "same":
		b.WriteString(dimStyle.Render("identical data (same DataHash)"))
		return b.String()
	case "only-a", "only-b":
		b.WriteString(dimStyle.Render("no counterpart on the other side"))
		return b.String()
	case "multi":
		fmt.Fprintf(&b, "%s\n", dimStyle.Render("several units on a side; showing the first of each. Narrow the selection (add Cluster or Region) for 1:1 pairs."))
	}
	if len(p.A) == 0 || len(p.B) == 0 {
		return b.String()
	}
	ta, oka := d.data[unitID(p.A[0])]
	tb, okb := d.data[unitID(p.B[0])]
	if !oka || !okb {
		b.WriteString(dimStyle.Render("loading data…"))
		return b.String()
	}
	lines := exec.Unified(ta, tb, 3)
	plus, minus := exec.Changed(lines)
	fmt.Fprintf(&b, "%s %s\n", addStyle.Render(fmt.Sprintf("+%d", plus)), delStyle.Render(fmt.Sprintf("-%d", minus)))
	body := make([]string, 0, len(lines))
	for _, l := range lines {
		text := l.Text
		if len(text) > width-2 && width > 3 {
			text = text[:width-3] + "…"
		}
		switch l.Kind {
		case '+':
			body = append(body, addStyle.Render("+"+text))
		case '-':
			body = append(body, delStyle.Render("-"+text))
		case '@':
			body = append(body, hunkStyle.Render("  ⋯"))
		default:
			body = append(body, " "+text)
		}
	}
	if windowed {
		if d.scroll >= len(body) {
			d.scroll = max(0, len(body)-1)
		}
		body = body[d.scroll:]
	}
	b.WriteString(strings.Join(body, "\n"))
	return b.String()
}

// ---- marks in browse: m marks A then B, d diffs them.

type mark struct {
	terms []lang.Cmp
	label string
}

func (m *Model) markCurrent() {
	if m.browse == nil {
		return
	}
	terms := m.browse.selections(m.browse.panes())
	if len(terms) == 0 {
		m.setStatus("nothing selected to mark: pick a value in a pane first", true)
		return
	}
	parts := make([]string, len(terms))
	for i, t := range terms {
		parts[i] = lang.ExprString(t)
	}
	mk := mark{terms: terms, label: strings.Join(parts, " AND ")}
	// Marks behave like a set of at most two: marking a marked selection
	// unmarks it, a third mark replaces B.
	for i, existing := range m.marks {
		if existing.label == mk.label {
			m.marks = append(m.marks[:i], m.marks[i+1:]...)
			m.setStatus(fmt.Sprintf("unmarked %s   (%d mark left)", mk.label, len(m.marks)), false)
			return
		}
	}
	switch len(m.marks) {
	case 0:
		m.marks = append(m.marks, mk)
		m.setStatus("A marked: "+mk.label+"   (move to the other side, mark it with m, then d)", false)
	case 1:
		m.marks = append(m.marks, mk)
		m.setStatus("B marked: "+mk.label+"   (d diffs A vs B · M clears)", false)
	default:
		m.marks[1] = mk
		m.setStatus("B replaced: "+mk.label+"   (A is still "+m.marks[0].label+")", false)
	}
}

// diffMarks builds and runs the diff statement from the marks; with one mark
// the current selection is side B. Terms both sides share move into the
// common where step.
func (m Model) diffMarks() (tea.Model, tea.Cmd) {
	if m.stmt == nil || m.browse == nil {
		return m, nil
	}
	marks := append([]mark(nil), m.marks...)
	if len(marks) == 1 {
		cur := m.browse.selections(m.browse.panes())
		if len(cur) == 0 {
			m.setStatus("A is marked as "+marks[0].label+"; move onto the other side and press d, or mark it with m", true)
			return m, nil
		}
		parts := make([]string, len(cur))
		for i, t := range cur {
			parts[i] = lang.ExprString(t)
		}
		cl := strings.Join(parts, " AND ")
		if cl == marks[0].label {
			m.setStatus("A is marked as "+cl+" and you are still on it; move onto the other side (or mark it with m) and press d", true)
			return m, nil
		}
		marks = append(marks, mark{terms: cur, label: cl})
	}
	if len(marks) < 2 {
		m.setStatus("mark two selections with m, then d", true)
		return m, nil
	}
	common := map[string]bool{}
	for _, t := range marks[0].terms {
		for _, u := range marks[1].terms {
			if lang.ExprString(t) == lang.ExprString(u) {
				common[lang.ExprString(t)] = true
			}
		}
	}
	side := func(mk mark) []lang.Cmp {
		var out []lang.Cmp
		for _, t := range mk.terms {
			if !common[lang.ExprString(t)] {
				out = append(out, t)
			}
		}
		return out
	}
	a, bb := side(marks[0]), side(marks[1])
	if len(a) == 0 || len(bb) == 0 {
		m.setStatus(fmt.Sprintf("nothing to diff: A is %s and B is %s; one contains the other", marks[0].label, marks[1].label), true)
		return m, nil
	}
	st := *m.stmt
	st.Browse = nil
	for _, t := range marks[0].terms {
		if common[lang.ExprString(t)] {
			st = addTerm(st, t)
		}
	}
	st.Diff = &lang.DiffStep{A: lang.Conjoin(a), B: lang.Conjoin(bb)}
	m.marks = nil
	return m.rewrite(&st)
}
