package tui

import (
	"context"
	"fmt"
	"os"
	osexec "os/exec"
	"path/filepath"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/confighub/cub-commander/internal/cubclient"
	"github.com/confighub/cub-commander/internal/exec"
)

// Detail is master/detail for one row: a Metadata tab (the extended row)
// and, for units, a Data tab holding the configuration. `e` on Data opens
// $EDITOR and saves the result as a new revision, conditional on the hash
// the data was read at; a conflict keeps your buffer for the next `e`.

type detailState struct {
	row     cubclient.Row
	entity  string
	tab     int    // 0 metadata, 1 data
	data    string // the text shown and edited: the unit's data, or one resource's document
	hash    string // the unit's DataHash the data was read at
	loaded  bool
	loading bool
	draft   string // your last edit when the save conflicted
	from    mode   // where Esc returns
	picker  *revPicker

	// For a Resource: the unit that owns it and the document it is.
	unitRow  cubclient.Row
	stream   *exec.Stream
	docIndex int
}

// editable reports whether the row has data to show and edit.
func (d *detailState) editable() bool { return d.entity == "Unit" || d.entity == "Resource" }

// unitKey identifies the unit behind the detail for message matching.
func (d *detailState) unitKey() string { return unitID(d.unitRow) }

// DataLoader reads a unit's data and hash; DataSaver writes it conditionally.
type DataLoader func(ctx context.Context, row cubclient.Row) (text, hash string, err error)
type DataSaver func(ctx context.Context, row cubclient.Row, text, ifMatch string) (rev int, err error)

type unitDataMsg struct {
	unitID string
	text   string
	hash   string
	err    error
}
type editedMsg struct {
	path string
	err  error
}
type savedMsg struct {
	unitID string
	rev    int
	text   string
	err    error
}

const editDescription = "cub commander edit"

func (m *Model) openDetailRow(row cubclient.Row) {
	entity := "Unit"
	if m.plan != nil {
		entity = m.plan.Entity.Name
	}
	unitRow := row
	if res, ok := row["Resource"].(map[string]any); ok {
		entity = "Resource"
		uid, _ := res["UnitID"].(string)
		sid, _ := res["SpaceID"].(string)
		if u, ok := row["Unit"].(map[string]any); ok && uid == "" {
			uid, _ = u["UnitID"].(string)
		}
		if sp, ok := row["Space"].(map[string]any); ok && sid == "" {
			sid, _ = sp["SpaceID"].(string)
		}
		unitRow = cubclient.Row{"Unit": map[string]any{"UnitID": uid, "SpaceID": sid}}
	}
	same := m.det != nil && m.det.entity == entity && m.det.unitKey() == unitID(unitRow) && unitID(unitRow) != "" &&
		(entity != "Resource" || rowLabel("Resource", m.det.row) == rowLabel("Resource", row))
	if same {
		// same unit or resource: keep the loaded data and draft, but never a stale picker
		m.det.row = row
		m.det.picker = nil
	} else {
		m.det = &detailState{row: row, entity: entity, from: m.mode, unitRow: unitRow}
	}
	if m.mode != modeDetail {
		m.det.from = m.mode
	}
	m.mode = modeDetail
	m.focus = focusMain
	m.renderDetail()
}

func (m *Model) renderDetail() {
	if m.det == nil {
		return
	}
	var body string
	switch m.det.tab {
	case 1:
		switch {
		case m.det.loaded:
			body = m.det.data
		case m.det.loading:
			body = dimStyle.Render("loading data…")
		default:
			body = dimStyle.Render("no data")
		}
	default:
		body = renderRow(m.det.row)
	}
	m.detail.SetContent(body)
	m.detail.GotoTop()
}

// detailLoad fetches the unit's data for the Data tab (a resource's tab
// shows its own document out of it).
func (m *Model) detailLoad() tea.Cmd {
	d := m.det
	if d == nil || !d.editable() || d.loaded || d.loading || m.dataLoader == nil {
		return nil
	}
	d.loading = true
	row, load := d.unitRow, m.dataLoader
	id := unitID(row)
	return func() tea.Msg {
		text, hash, err := load(context.Background(), row)
		return unitDataMsg{unitID: id, text: text, hash: hash, err: err}
	}
}

// dataLoaded stores fetched unit data; for a resource, only its document is shown.
func (m *Model) dataLoaded(msg unitDataMsg) {
	d := m.det
	if d == nil || d.unitKey() != msg.unitID {
		return
	}
	d.loading = false
	if msg.err != nil {
		m.setStatus("unit data: "+msg.err.Error(), true)
		m.renderDetail()
		return
	}
	d.hash = msg.hash
	if d.entity == "Resource" {
		res, _ := d.row["Resource"].(map[string]any)
		rt, _ := res["ResourceType"].(string)
		rn, _ := res["ResourceName"].(string)
		d.stream = exec.SplitDocs(msg.text)
		i, err := d.stream.Find(rt, rn)
		if err != nil {
			m.setStatus(err.Error(), true)
			m.renderDetail()
			return
		}
		d.docIndex = i
		d.data, d.loaded = d.stream.Docs[i].Text, true
	} else {
		d.data, d.loaded = msg.text, true
	}
	m.renderDetail()
}

func (m Model) detailKey(k tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	d := m.det
	if d == nil {
		return m, nil
	}
	if d.picker != nil {
		return m.pickerKey(k)
	}
	switch k.String() {
	case "d":
		if d.entity != "Unit" {
			m.setStatus("revision diffs are per unit; open the unit for them", true)
			return m, nil
		}
		return m, m.openRevisions()
	case "D", "shift+d":
		if d.entity == "Resource" {
			return m, nil
		}
		return m.pivotRow("d", d.row, d.entity)
	case "1", "[", "left":
		d.tab = 0
		m.renderDetail()
		return m, nil
	case "2", "]", "right", "tab":
		if d.editable() {
			d.tab = 1
			m.renderDetail()
			return m, m.detailLoad()
		}
		m.setStatus(fmt.Sprintf("a %s has only Metadata; open a unit or a resource for its Data", d.entity), true)
		return m, nil
	case "e":
		if !d.editable() {
			m.setStatus("only unit and resource data is editable", true)
			return m, nil
		}
		if d.tab != 1 {
			d.tab = 1
			m.renderDetail()
		}
		if !d.loaded {
			m.setStatus("data not loaded yet", true)
			return m, m.detailLoad()
		}
		return m.editData()
	case "R", "shift+r":
		d.loaded, d.loading = false, false
		m.renderDetail()
		return m, m.detailLoad()
	case "s", "t", "u", "r", "l":
		if d.entity == "Resource" {
			return m, nil
		}
		return m.pivotRow(k.String(), d.row, d.entity)
	case "q":
		return m, tea.Quit
	case "?":
		m.helpOpen = true
		return m, nil
	}
	var cmd tea.Cmd
	m.detail, cmd = m.detail.Update(k)
	return m, cmd
}

// editData writes the data (or the draft from a conflicted save) to a temp
// file and hands the terminal to $EDITOR; the callback carries the file back.
func (m Model) editData() (tea.Model, tea.Cmd) {
	d := m.det
	text := d.data
	if d.draft != "" {
		text = d.draft
	}
	f, err := os.CreateTemp("", "cub-commander-*.yaml")
	if err != nil {
		m.setStatus("temp file: "+err.Error(), true)
		return m, nil
	}
	path := f.Name()
	if _, err := f.WriteString(text); err != nil {
		f.Close()
		m.setStatus("temp file: "+err.Error(), true)
		return m, nil
	}
	f.Close()
	editor := os.Getenv("EDITOR")
	if editor == "" {
		editor = "vi"
	}
	args := strings.Fields(editor)
	args = append(args, path)
	c := osexec.Command(args[0], args[1:]...)
	m.setStatus("editing in "+filepath.Base(args[0])+"…", false)
	return m, tea.ExecProcess(c, func(err error) tea.Msg { return editedMsg{path: path, err: err} })
}

// afterEdit compares the edited file with what was read and saves it as a
// revision when it changed, conditional on the hash read.
func (m *Model) afterEdit(msg editedMsg) tea.Cmd {
	d := m.det
	defer os.Remove(msg.path)
	if msg.err != nil {
		m.setStatus("editor exited with an error; nothing saved: "+msg.err.Error(), true)
		return nil
	}
	b, err := os.ReadFile(msg.path)
	if err != nil {
		m.setStatus("read edited file: "+err.Error(), true)
		return nil
	}
	text := string(b)
	if d == nil {
		return nil
	}
	if text == d.data {
		d.draft = ""
		m.setStatus("no changes; nothing saved", false)
		return nil
	}
	if m.dataSaver == nil {
		m.setStatus("no saver configured", true)
		return nil
	}
	if d.hash == "" {
		// An absent If-Match is an unconditional write on the server; never send one.
		d.draft = text
		m.setStatus("the read served no ETag, so the write cannot be made conditional; not saved (R reloads, e reopens the draft)", true)
		return nil
	}
	d.draft = text
	row, hash, save := d.unitRow, d.hash, m.dataSaver
	id := unitID(row)
	lines := exec.Unified(d.data, text, 0)
	plus, minus := exec.Changed(lines)
	// A resource edit is written as the whole unit with its document replaced.
	full := text
	if d.entity == "Resource" && d.stream != nil {
		full = d.stream.Replace(d.docIndex, text)
	}
	m.setStatus(fmt.Sprintf("saving +%d -%d …", plus, minus), false)
	return func() tea.Msg {
		rev, err := save(context.Background(), row, full, hash)
		return savedMsg{unitID: id, rev: rev, text: text, err: err}
	}
}

func (m *Model) afterSave(msg savedMsg) tea.Cmd {
	d := m.det
	if d == nil || d.unitKey() != msg.unitID {
		return nil
	}
	if msg.err != nil {
		if _, conflict := msg.err.(*cubclient.ConflictError); conflict {
			m.setStatus("conflict: the unit changed since you read it. Reloaded the head; press e to reopen your edit against it", true)
			d.loaded, d.loading = false, false
			m.renderDetail()
			return m.detailLoad()
		}
		m.setStatus("save failed: "+msg.err.Error(), true)
		return nil
	}
	d.draft = ""
	d.loaded, d.loading = false, false
	if msg.rev > 0 {
		m.setStatus(fmt.Sprintf("saved as revision %d", msg.rev), false)
	} else {
		m.setStatus("saved", false)
	}
	m.renderDetail()
	return m.detailLoad()
}

func (m Model) detailView() string {
	d := m.det
	w := m.mainWidth()
	if d == nil {
		return ""
	}
	name := exec.RowName(d.row)
	if d.entity == "Resource" {
		name = rowLabel("Resource", d.row)
	}
	tabs := []string{"Metadata"}
	if d.editable() {
		tabs = append(tabs, "Data")
	}
	var parts []string
	for i, t := range tabs {
		label := fmt.Sprintf(" %d %s ", i+1, t)
		if i == d.tab {
			parts = append(parts, keyStyle.Render(label))
		} else {
			parts = append(parts, dimStyle.Render(label))
		}
	}
	extra := ""
	if d.tab == 1 && d.loaded {
		extra = dimStyle.Render(fmt.Sprintf("  hash %s…", firstN(d.hash, 12)))
		if d.entity == "Resource" && d.stream != nil {
			extra += dimStyle.Render(fmt.Sprintf("  document %d of %d in its unit", d.docIndex+1, len(d.stream.Docs)))
		}
		if d.draft != "" {
			extra += localChipStyle.Render("  unsaved draft (e reopens it)")
		}
		extra += dimStyle.Render("  · e edit · d diff revisions · R reload")
	}
	switch {
	case d.entity == "Space":
		extra = dimStyle.Render("  (Space: metadata only · u lists its units · t its targets)")
	case d.entity == "Resource" && d.tab == 0:
		extra = dimStyle.Render("  2 or → for Data (this resource's document; e edits it within its unit)")
	case !d.editable():
		extra = dimStyle.Render("  (" + d.entity + ": metadata only)")
	case d.tab == 0:
		extra = dimStyle.Render("  2 or → for Data · d diff revisions · s space · r revisions · l links")
	}
	head := lipgloss.NewStyle().MaxWidth(w).Render(titleStyle.Render(" "+name) + "  " + strings.Join(parts, " ") + extra)
	if d.picker != nil {
		return head + "\n" + m.pickerView(w)
	}
	return head + "\n" + m.detail.View()
}

func firstN(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
