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
)

// The revision picker: d on a unit's detail lists its revisions; Enter diffs
// the highlighted one against the data on screen (the head), m marks one so
// the next Enter diffs the two revisions instead.

type revPicker struct {
	rows    []cubclient.Row // newest first
	cur     int
	mark    int // index of the marked revision, -1 for none
	loading bool
	err     string
}

// RevLoader lists a unit's revisions; RevDataLoader fetches one revision's data.
type RevLoader func(ctx context.Context, row cubclient.Row) ([]cubclient.Row, error)
type RevDataLoader func(ctx context.Context, row cubclient.Row, revisionID string) (string, error)

type revisionsMsg struct {
	unitID string
	rows   []cubclient.Row
	err    error
}
type revDiffMsg struct {
	unitID string
	title  string
	a, b   string // texts
	labelA string
	labelB string
	err    error
}

func revField(r cubclient.Row, f string) any {
	rev, _ := r["Revision"].(map[string]any)
	return rev[f]
}

func revNum(r cubclient.Row) int {
	n, _ := revField(r, "RevisionNum").(float64)
	return int(n)
}

func (m *Model) openRevisions() tea.Cmd {
	d := m.det
	if d == nil || d.entity != "Unit" {
		m.setStatus("revisions belong to units", true)
		return nil
	}
	if m.revLoader == nil {
		m.setStatus("no revision loader configured", true)
		return nil
	}
	d.picker = &revPicker{mark: -1, loading: true}
	row, load := d.unitRow, m.revLoader
	id := unitID(row)
	// The picker diffs against the data on screen, so make sure it is loaded
	// and shown: d from the Metadata tab moves to Data.
	d.tab = 1
	m.renderDetail()
	return tea.Batch(m.detailLoad(), func() tea.Msg {
		rows, err := load(context.Background(), row)
		return revisionsMsg{unitID: id, rows: rows, err: err}
	})
}

func (m *Model) revisionsLoaded(msg revisionsMsg) {
	d := m.det
	if d == nil || d.picker == nil || d.unitKey() != msg.unitID {
		return
	}
	d.picker.loading = false
	if msg.err != nil {
		d.picker.err = msg.err.Error()
		return
	}
	rows := append([]cubclient.Row(nil), msg.rows...)
	sort.Slice(rows, func(i, j int) bool { return revNum(rows[i]) > revNum(rows[j]) })
	d.picker.rows = rows
	// Start on the revision before the head, the most common thing to diff against.
	if len(rows) > 1 {
		d.picker.cur = 1
	}
}

func (m Model) pickerKey(k tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	d := m.det
	p := d.picker
	switch k.String() {
	case "esc":
		d.picker = nil
		return m, nil
	case "up", "k":
		if p.cur > 0 {
			p.cur--
		}
	case "down", "j":
		if p.cur < len(p.rows)-1 {
			p.cur++
		}
	case "m":
		if len(p.rows) == 0 {
			return m, nil
		}
		if p.mark == p.cur {
			p.mark = -1
			m.setStatus("mark cleared", false)
		} else {
			p.mark = p.cur
			m.setStatus(fmt.Sprintf("marked revision %d; pick another and press Enter to diff the two", revNum(p.rows[p.cur])), false)
		}
	case "enter":
		if len(p.rows) == 0 {
			return m, nil
		}
		return m.runRevDiff()
	case "q":
		return m, tea.Quit
	case "?":
		m.helpOpen = true
	}
	return m, nil
}

// runRevDiff fetches the chosen revision(s) and diffs them: marked vs picked,
// or picked vs the data on screen.
func (m Model) runRevDiff() (tea.Model, tea.Cmd) {
	d := m.det
	p := d.picker
	pick := p.rows[p.cur]
	pickID, _ := revField(pick, "RevisionID").(string)
	pickNum := revNum(pick)
	if m.revDataLoader == nil {
		m.setStatus("no revision data loader configured", true)
		return m, nil
	}
	row, load := d.unitRow, m.revDataLoader
	uid := unitID(row)
	if p.mark >= 0 && p.mark != p.cur {
		marked := p.rows[p.mark]
		markID, _ := revField(marked, "RevisionID").(string)
		markNum := revNum(marked)
		// Older on the left.
		aID, aNum, bID, bNum := markID, markNum, pickID, pickNum
		if aNum > bNum {
			aID, aNum, bID, bNum = bID, bNum, aID, aNum
		}
		m.setStatus(fmt.Sprintf("diffing revision %d → %d …", aNum, bNum), false)
		return m, func() tea.Msg {
			a, err := load(context.Background(), row, aID)
			if err != nil {
				return revDiffMsg{unitID: uid, err: err}
			}
			b, err := load(context.Background(), row, bID)
			if err != nil {
				return revDiffMsg{unitID: uid, err: err}
			}
			return revDiffMsg{unitID: uid, title: fmt.Sprintf("diff revision %d → %d", aNum, bNum), a: a, b: b,
				labelA: fmt.Sprintf("revision %d", aNum), labelB: fmt.Sprintf("revision %d", bNum)}
		}
	}
	head, loaded := d.data, d.loaded
	headLoad := m.dataLoader
	m.setStatus(fmt.Sprintf("diffing revision %d → current …", pickNum), false)
	return m, func() tea.Msg {
		b := head
		if !loaded {
			if headLoad == nil {
				return revDiffMsg{unitID: uid, err: fmt.Errorf("current data not loaded")}
			}
			text, _, err := headLoad(context.Background(), row)
			if err != nil {
				return revDiffMsg{unitID: uid, err: err}
			}
			b = text
		}
		a, err := load(context.Background(), row, pickID)
		if err != nil {
			return revDiffMsg{unitID: uid, err: err}
		}
		return revDiffMsg{unitID: uid, title: fmt.Sprintf("diff revision %d → current data", pickNum), a: a, b: b,
			labelA: fmt.Sprintf("revision %d", pickNum), labelB: "current data"}
	}
}

func (m *Model) revDiffLoaded(msg revDiffMsg) {
	if msg.err != nil {
		m.setStatus("revision diff: "+msg.err.Error(), true)
		return
	}
	// The picker stays, mark included: Esc from the diff returns to the list
	// so the next revision is one keystroke away.
	m.showText(msg.title, renderUnified(msg.a, msg.b, msg.labelA, msg.labelB))
	m.focus = focusMain
}

// renderUnified renders a colored unified diff with a header naming both sides.
func renderUnified(a, b, labelA, labelB string) string {
	lines := exec.Unified(a, b, 3)
	plus, minus := exec.Changed(lines)
	var out []string
	out = append(out, delStyle.Render("--- "+labelA)+"   "+addStyle.Render("+++ "+labelB)+"   "+addStyle.Render(fmt.Sprintf("+%d", plus))+" "+delStyle.Render(fmt.Sprintf("-%d", minus)))
	if plus == 0 && minus == 0 {
		out = append(out, dimStyle.Render("identical"))
	}
	for _, l := range lines {
		switch l.Kind {
		case '+':
			out = append(out, addStyle.Render("+"+l.Text))
		case '-':
			out = append(out, delStyle.Render("-"+l.Text))
		case '@':
			out = append(out, hunkStyle.Render("  ⋯"))
		default:
			out = append(out, " "+l.Text)
		}
	}
	return strings.Join(out, "\n")
}

func (m Model) pickerView(width int) string {
	p := m.det.picker
	var b strings.Builder
	b.WriteString(titleStyle.Render(" Revisions") + dimStyle.Render("  ↑↓ choose · ⏎ diff against current data · m mark one, then ⏎ on another to diff the two · Esc close") + "\n")
	switch {
	case p.loading:
		b.WriteString(dimStyle.Render(" loading…"))
	case p.err != "":
		b.WriteString(errStyle.Render(" " + p.err))
	case len(p.rows) == 0:
		b.WriteString(dimStyle.Render(" no revisions"))
	}
	for i, r := range p.rows {
		num := revNum(r)
		created, _ := revField(r, "CreatedAt").(string)
		if len(created) > 16 {
			created = created[:16]
		}
		desc, _ := revField(r, "Description").(string)
		src, _ := revField(r, "Source").(string)
		user := ""
		if u, ok := r["User"].(map[string]any); ok {
			user, _ = u["Username"].(string)
		}
		tag := "   "
		if i == 0 {
			tag = dimStyle.Render("hd ")
		}
		if i == p.mark {
			tag = markStyle.Render("A  ")
		}
		line := fmt.Sprintf(" %s%4d  %s  %-28s %-12s %s", tag, num, created, firstN(user, 28), src, desc)
		if i == p.cur {
			line = keyStyle.Render(fmt.Sprintf(" %s%4d  %s  %-28s %-12s %s", strings.TrimSpace(stripANSI(tag))+strings.Repeat(" ", 3-len(strings.TrimSpace(stripANSI(tag)))), num, created, firstN(user, 28), src, desc))
		}
		b.WriteString(lipgloss.NewStyle().MaxWidth(width).Render(line) + "\n")
	}
	return b.String()
}

// revisionRows lists a unit's revisions with the fields the picker shows.
func revisionRows(ctx context.Context, c *cubclient.Client, row cubclient.Row) ([]cubclient.Row, error) {
	p, err := unitAPIPath(row)
	if err != nil {
		return nil, err
	}
	return c.List(ctx, p+"/revision", url.Values{
		"select":  {"RevisionNum,RevisionID,CreatedAt,Description,Source,DataHash,User.Username"},
		"include": {"UserID"},
	})
}

func revisionData(ctx context.Context, c *cubclient.Client, row cubclient.Row, revisionID string) (string, error) {
	p, err := unitAPIPath(row)
	if err != nil {
		return "", err
	}
	return c.GetRaw(ctx, p+"/revision/"+revisionID+"/data")
}

func unitAPIPath(row cubclient.Row) (string, error) {
	own, _ := row["Unit"].(map[string]any)
	uid, _ := own["UnitID"].(string)
	sid, _ := own["SpaceID"].(string)
	if sid == "" {
		if sp, ok := row["Space"].(map[string]any); ok {
			sid, _ = sp["SpaceID"].(string)
		}
	}
	if uid == "" || sid == "" {
		return "", fmt.Errorf("row has no UnitID/SpaceID")
	}
	return "/space/" + sid + "/unit/" + uid, nil
}
