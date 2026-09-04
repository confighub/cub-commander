// Package tui is the full-screen application: a command area at the bottom,
// a main area showing results, a detail view or plain text, a chips row for
// the current WHERE, a history drawer, and the key bar. See docs/design.md §5.
package tui

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/table"
	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"

	"github.com/confighub/cub-commander/internal/catalog"
	"github.com/confighub/cub-commander/internal/cubclient"
	"github.com/confighub/cub-commander/internal/exec"
	"github.com/confighub/cub-commander/internal/history"
	"github.com/confighub/cub-commander/internal/lang"
	"github.com/confighub/cub-commander/internal/plan"
)

type mode int

const (
	modeResults mode = iota
	modeDetail
	modeText
	modeBrowse
	modeDiff
)

type focus int

const (
	focusCmd focus = iota
	focusMain
	focusDrawer
)

// Runner executes one statement; injected so the model is testable offline.
type Runner func(ctx context.Context, st lang.Stmt, sess plan.Session) (tea.Msg, error)

// Sampler fills the live catalog (label keys and values, spaces); nil offline.
type Sampler func(ctx context.Context, live *catalog.Live) error

// Fetcher performs a list GET for panes that load on demand (a unit's resources).
type Fetcher func(ctx context.Context, path string, q url.Values) ([]cubclient.Row, error)

type Model struct {
	width, height int
	sess          plan.Session
	runner        Runner
	sampler       Sampler
	live          *catalog.Live
	completer     *Completer
	hist          *history.Store
	ctxName       string
	server        string

	cmd    textarea.Model
	tbl    table.Model
	detail viewport.Model
	text   viewport.Model
	mode   mode
	focus  focus

	// last executed statement and its outcome
	stmt      *lang.SelectStmt
	plan      *plan.Plan
	result    *exec.Result
	colCursor int
	textTitle string

	// history navigation from the command area
	histIdx   int // -1 = live draft
	histDraft string

	// drawer
	drawerOpen   bool
	drawerFilter string
	drawerCursor int
	drawerItems  []history.Entry

	// detail
	det           *detailState
	dataLoader    DataLoader
	dataSaver     DataSaver
	revLoader     RevLoader
	revDataLoader RevDataLoader
	textFrom      mode

	// diff
	diff        *diffState
	dataFetcher DataFetcher
	marks       []mark

	// browse and the chooser
	fetcher       Fetcher
	previewMode   int
	resCache      map[string][]cubclient.Row
	resPending    map[string]bool
	browse        *browseState
	chooserOpen   bool
	chooserCursor int
	chooserItems  []chooserItem

	// completion popup
	popup      []Candidate
	popupStart int
	popupSel   int
	popupOpen  bool

	status     string
	statusErr  bool
	running    bool
	runningSrc string
	runStart   time.Time
	kitty     bool
	helpOpen  bool
}

// Messages.
type resultMsg struct {
	src  string
	stmt *lang.SelectStmt
	plan *plan.Plan
	res  *exec.Result
}
type textMsg struct {
	src, title, body string
}
type errMsg struct {
	src string
	err error
}
type sampledMsg struct{ err error }
type tickMsg time.Time

// tick keeps the loading panel's elapsed time moving while a statement runs.
func tick() tea.Cmd {
	return tea.Tick(250*time.Millisecond, func(t time.Time) tea.Msg { return tickMsg(t) })
}

func New(sess plan.Session, runner Runner, sampler Sampler, hist *history.Store) Model {
	ta := textarea.New()
	ta.Prompt = ""
	ta.ShowLineNumbers = false
	ta.Placeholder = "Unit | in * | where …    (Tab completes, ^/ help)"
	ta.KeyMap.InsertNewline = key.NewBinding(key.WithKeys("shift+enter"))
	ta.DynamicHeight = true
	ta.MinHeight = 3
	ta.MaxHeight = 8
	ta.CharLimit = 0
	ta.Focus()

	tbl := table.New(table.WithFocused(false))
	live := catalog.NewLive()
	m := Model{
		sess: sess, runner: runner, sampler: sampler, live: live, completer: &Completer{Live: live}, hist: hist,
		ctxName: os.Getenv("CUB_CONTEXT"), server: strings.TrimPrefix(strings.TrimPrefix(os.Getenv("CUB_SERVER"), "https://"), "http://"),
		cmd: ta, tbl: tbl, detail: viewport.New(), text: viewport.New(),
		histIdx: -1, resCache: map[string][]cubclient.Row{}, resPending: map[string]bool{},
	}
	m.text.SetContent(helpText)
	m.textTitle = "Welcome"
	m.mode = modeText
	m.openChooser()
	return m
}

func (m Model) Init() tea.Cmd {
	if m.sampler == nil {
		return nil
	}
	sampler, live := m.sampler, m.live
	return func() tea.Msg { return sampledMsg{err: sampler(context.Background(), live)} }
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.layout()
		return m, nil
	case tea.KeyboardEnhancementsMsg:
		m.kitty = msg.SupportsKeyDisambiguation()
		return m, nil
	case tea.KeyReleaseMsg:
		return m, nil
	case tickMsg:
		if m.running {
			return m, tick()
		}
		return m, nil
	case resourcesMsg:
		byUnit := map[string][]cubclient.Row{}
		for _, r := range msg.rows {
			res, _ := r["Resource"].(map[string]any)
			id, _ := res["UnitID"].(string)
			byUnit[id] = append(byUnit[id], r)
		}
		for _, id := range msg.unitIDs {
			delete(m.resPending, id)
			m.resCache[id] = byUnit[id] // nil when the unit has none: cached as empty
		}
		if msg.err != nil {
			m.setStatus("resources: "+msg.err.Error(), true)
		}
		return m, nil
	case detailMsg:
		m.openDetailRow(msg.row)
		return m, nil
	case unitDataMsg:
		if m.det != nil && unitID(m.det.row) == msg.unitID {
			m.det.loading = false
			if msg.err != nil {
				m.setStatus("unit data: "+msg.err.Error(), true)
			} else {
				m.det.data, m.det.hash, m.det.loaded = msg.text, msg.hash, true
			}
			m.renderDetail()
		}
		return m, nil
	case revisionsMsg:
		m.revisionsLoaded(msg)
		return m, nil
	case revDiffMsg:
		m.revDiffLoaded(msg)
		return m, nil
	case editedMsg:
		return m, m.afterEdit(msg)
	case savedMsg:
		return m, m.afterSave(msg)
	case diffMsg:
		m.running = false
		m.stmt, m.plan = msg.stmt, msg.plan
		m.chooserOpen = false
		m.startDiff(msg.res)
		m.record(msg.src, len(msg.res.Pairs), "")
		return m, m.dataFetch()
	case dataMsg:
		if m.diff != nil {
			delete(m.diff.pending, msg.unitID)
			if msg.err != nil {
				m.setStatus("unit data: "+msg.err.Error(), true)
				m.diff.data[msg.unitID] = ""
			} else {
				m.diff.data[msg.unitID] = msg.text
			}
		}
		return m, nil
	case sampledMsg:
		if msg.err != nil {
			m.setStatus("catalog sampling failed: "+msg.err.Error(), true)
		} else if m.status == "" {
			m.setStatus(fmt.Sprintf("catalog: %d spaces, %d unit label keys", len(m.live.Spaces()), len(m.live.LabelKeys("Unit"))), false)
		}
		if m.chooserOpen {
			m.chooserItems = m.presets()
		}
		return m, nil
	case resultMsg:
		m.running = false
		m.stmt, m.plan, m.result = msg.stmt, msg.plan, msg.res
		m.colCursor = 0
		m.chooserOpen = false
		m.mode = modeResults
		m.fillTable()
		if len(msg.plan.Browse) > 0 && msg.res.Raw != nil {
			m.startBrowse(msg.plan.Browse, msg.res.Raw)
			return m, m.resourceFetch(m.browse.panes())
		}
		m.setStatus(m.rowsStatus(msg), false)
		m.record(msg.src, len(msg.res.Rows), "")
		return m, nil
	case textMsg:
		m.running = false
		m.showText(msg.title, msg.body)
		m.record(msg.src, 0, "")
		return m, nil
	case errMsg:
		m.running = false
		m.setStatus(msg.err.Error(), true)
		m.record(msg.src, 0, msg.err.Error())
		return m, nil
	case tea.KeyPressMsg:
		return m.key(msg)
	}
	return m, nil
}

// rowsStatus says how many rows came back and, when the statement was scoped
// to one space, says so: a small result in a narrow scope is the most common
// surprise, and the fix (IN * or USE *) belongs next to the number.
func (m *Model) rowsStatus(msg resultMsg) string {
	n := len(msg.res.Rows)
	if msg.plan == nil || msg.plan.List == nil {
		return fmt.Sprintf("%d rows", n)
	}
	hidden := ""
	if len(msg.res.Hidden) > 0 {
		hidden = fmt.Sprintf("   (constant labels hidden: %s)", strings.Join(msg.res.Hidden, ", "))
	}
	if sp := msg.plan.List.Space; sp != "" {
		return fmt.Sprintf("%d rows in space %s   (in * or USE * for the whole org)%s", n, sp, hidden)
	}
	if msg.res.ServerRows != n {
		return fmt.Sprintf("%d rows org-wide, %d after local stages%s", msg.res.ServerRows, n, hidden)
	}
	return fmt.Sprintf("%d rows org-wide%s", n, hidden)
}

func (m *Model) record(src string, rows int, errText string) {
	if m.hist == nil || strings.TrimSpace(src) == "" {
		return
	}
	_ = m.hist.Append(history.Entry{Stmt: src, Space: m.sess.Space, Rows: rows, Err: errText})
	m.histIdx = -1
}

func (m *Model) setStatus(s string, isErr bool) {
	m.status, m.statusErr = s, isErr
}

func (m *Model) showText(title, body string) {
	if m.mode != modeText {
		m.textFrom = m.mode
	}
	m.textTitle = title
	m.text.SetContent(body)
	m.text.GotoTop()
	m.mode = modeText
}

// key routes a key press by global bindings first, then by focus.
func (m Model) key(k tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	s := k.String()
	// An open completion popup owns the keys that navigate or close it.
	if m.popupOpen && m.focus == focusCmd {
		return m.cmdKey(k)
	}
	// So does an open revision picker in the detail view (Esc closes it, not the view).
	if m.mode == modeDetail && m.focus == focusMain && m.det != nil && m.det.picker != nil {
		return m.pickerKey(k)
	}
	// Global.
	// No F-keys: they are awkward on a Mac. Everything global is a control chord
	// the editor does not use (ctrl+a/e/f/b/k/u/w/d/n/p/t/v are readline).
	switch s {
	case "ctrl+c", "ctrl+q":
		return m, tea.Quit
	case "ctrl+/", "ctrl+_":
		m.helpOpen = !m.helpOpen
		return m, nil
	case "ctrl+g":
		if m.mode == modeBrowse && m.browse != nil {
			return m.commitBrowse(m.browse.panes())
		}
		if m.result != nil {
			m.mode = modeResults
			m.focus = focusMain
		}
		return m, nil
	case "ctrl+b":
		m.openChooser()
		return m, nil
	case "ctrl+o":
		m.openDetail()
		return m, nil
	case "ctrl+r":
		m.toggleDrawer()
		return m, nil
	case "ctrl+x":
		if m.plan != nil {
			m.showText("EXPLAIN", m.plan.Explain("")+"\n"+m.plan.CubCommand())
			m.focus = focusMain
		}
		return m, nil
	case "esc":
		switch {
		case m.helpOpen:
			m.helpOpen = false
		case m.drawerOpen:
			m.drawerOpen = false
			m.focus = focusCmd
		case m.chooserOpen && (m.result != nil || m.browse != nil):
			m.chooserOpen = false
		case m.mode == modeDetail:
			if m.det != nil && (m.det.from == modeBrowse || m.det.from == modeDiff || m.det.from == modeResults) {
				m.mode = m.det.from
			} else if m.result != nil {
				m.mode = modeResults
			} else {
				m.openChooser()
			}
		case m.mode == modeText && m.textFrom == modeDetail && m.det != nil:
			m.mode = modeDetail
		case m.mode == modeText && (m.textFrom == modeBrowse && m.browse != nil || m.textFrom == modeDiff && m.diff != nil):
			m.mode = m.textFrom
		case m.mode == modeText && m.result != nil:
			m.mode = modeResults
		case m.mode == modeDiff:
			if m.browse != nil {
				m.mode = modeBrowse
			} else if m.result != nil {
				m.mode = modeResults
			} else {
				m.openChooser()
			}
		case m.mode == modeBrowse:
			m.openChooser()
		case m.focus != focusCmd:
			m.focus = focusCmd
		}
		return m, nil
	case "shift+tab":
		m.toggleFocus()
		return m, nil
	case "tab":
		if m.focus == focusMain && m.mode == modeDetail && !m.drawerOpen {
			return m.detailKey(k) // Tab switches detail tabs; ⇧Tab still moves focus
		}
		if m.focus != focusCmd || m.drawerOpen {
			m.toggleFocus()
			return m, nil
		}
		// In the command area Tab completes; see cmdKey.
	}
	if m.helpOpen {
		return m, nil
	}
	switch m.focus {
	case focusDrawer:
		return m.drawerKey(k)
	case focusMain:
		if m.chooserOpen {
			return m.chooserKey(k)
		}
		if m.mode == modeBrowse {
			return m.browseKey(k)
		}
		if m.mode == modeDiff {
			return m.diffKey(k)
		}
		if m.mode == modeDetail {
			return m.detailKey(k)
		}
		return m.mainKey(k)
	}
	return m.cmdKey(k)
}

func (m *Model) toggleFocus() {
	m.popupOpen = false
	if m.drawerOpen {
		m.drawerOpen = false
	}
	if m.focus == focusCmd {
		m.focus = focusMain
		m.tbl.Focus()
	} else {
		m.focus = focusCmd
		m.tbl.Blur()
	}
}

func (m Model) cmdKey(k tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	s := k.String()
	if m.popupOpen {
		switch s {
		case "tab", "down":
			m.popupSel = (m.popupSel + 1) % len(m.popup)
			m.applyCandidate(false)
			return m, nil
		case "shift+tab", "up":
			m.popupSel = (m.popupSel + len(m.popup) - 1) % len(m.popup)
			m.applyCandidate(false)
			return m, nil
		case "enter", "right":
			m.applyCandidate(true)
			m.popupOpen = false
			return m, nil
		case "esc":
			m.popupOpen = false
			return m, nil
		}
		m.popupOpen = false
	}
	switch s {
	case "tab":
		m.complete()
		return m, nil
	case "enter":
		src := m.cmd.Value()
		if strings.TrimSpace(src) == "" {
			return m, nil
		}
		if _, err := lang.Parse(src); err != nil {
			if pe, ok := err.(*lang.ParseError); ok && pe.Pos >= len(strings.TrimRight(src, " \n\t")) {
				// Incomplete: keep typing on a new line.
				m.cmd.InsertString("\n")
				return m, nil
			}
			m.setStatus(err.Error(), true)
			return m, nil
		}
		return m.execute(src)
	case "alt+enter", "ctrl+enter":
		return m.execute(m.cmd.Value())
	case "up":
		if m.cmd.Line() == 0 {
			m.historyStep(1)
			return m, nil
		}
	case "down":
		if m.cmd.Line() == m.cmd.LineCount()-1 {
			m.historyStep(-1)
			return m, nil
		}
	case "ctrl+l":
		m.cmd.Reset()
		m.histIdx = -1
		return m, nil
	}
	var cmd tea.Cmd
	m.cmd, cmd = m.cmd.Update(k)
	m.layout()
	return m, cmd
}

// complete runs the completer at the cursor and either inserts the single
// candidate, extends to the common prefix, or opens the popup.
func (m *Model) complete() {
	text, cur := m.cmd.Value(), m.cursorOffset()
	start, cands := m.completer.Complete(text[:cur])
	if len(cands) == 0 {
		m.setStatus("no completions here", false)
		return
	}
	m.popup, m.popupStart, m.popupSel = cands, start, 0
	if len(cands) == 1 {
		m.applyCandidate(true)
		return
	}
	partial := text[start:cur]
	if cp := commonPrefix(cands); len(cp) > len(partial) {
		m.replaceRange(start, cur, cp)
	}
	m.popupOpen = true
}

// applyCandidate writes the selected candidate over the partial segment.
// final adds the trailing space or closing quote.
func (m *Model) applyCandidate(final bool) {
	if len(m.popup) == 0 {
		return
	}
	c := m.popup[m.popupSel]
	text := c.Text
	if final && c.Quote {
		text += "'"
	}
	if final && c.Space {
		text += " "
	}
	m.replaceRange(m.popupStart, m.cursorOffset(), text)
}

// cursorOffset is the byte offset of the cursor in the textarea value.
func (m *Model) cursorOffset() int {
	lines := strings.Split(m.cmd.Value(), "\n")
	row, col := m.cmd.Line(), m.cmd.Column()
	off := 0
	for i := 0; i < row && i < len(lines); i++ {
		off += len(lines[i]) + 1
	}
	if row < len(lines) {
		r := []rune(lines[row])
		if col > len(r) {
			col = len(r)
		}
		off += len(string(r[:col]))
	}
	return off
}

func (m *Model) replaceRange(start, end int, with string) {
	v := m.cmd.Value()
	if start < 0 || end > len(v) || start > end {
		return
	}
	nv := v[:start] + with + v[end:]
	m.cmd.SetValue(nv)
	// Put the cursor right after the insertion.
	target := start + len(with)
	before := nv[:target]
	row := strings.Count(before, "\n")
	col := len([]rune(before[strings.LastIndex(before, "\n")+1:]))
	m.cmd.MoveToBegin()
	for i := 0; i < row; i++ {
		m.cmd.CursorDown()
	}
	m.cmd.SetCursorColumn(col)
	m.layout()
}

// historyStep moves through history from the command area: +1 older, -1 newer.
func (m *Model) historyStep(dir int) {
	if m.hist == nil || m.hist.Len() == 0 {
		return
	}
	if m.histIdx == -1 {
		m.histDraft = m.cmd.Value()
	}
	items := m.hist.Recent("", 500)
	next := m.histIdx + dir
	if next < -1 || next >= len(items) {
		return
	}
	m.histIdx = next
	if next == -1 {
		m.cmd.SetValue(m.histDraft)
	} else {
		m.cmd.SetValue(items[next].Stmt)
	}
	m.cmd.MoveToEnd()
	m.layout()
}

func (m Model) mainKey(k tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	s := k.String()
	switch m.mode {
	case modeText:
		var cmd tea.Cmd
		m.text, cmd = m.text.Update(k)
		return m, cmd
	case modeDetail:
		var cmd tea.Cmd
		m.detail, cmd = m.detail.Update(k)
		return m, cmd
	}
	if m.result == nil {
		return m, nil
	}
	switch s {
	case "q":
		return m, tea.Quit
	case "?":
		m.helpOpen = true
		return m, nil
	case "b":
		m.openChooser()
		return m, nil
	case "left":
		if m.colCursor > 0 {
			m.colCursor--
			m.fillTable()
		}
		return m, nil
	case "right":
		if m.colCursor < len(m.result.Headers)-1 {
			m.colCursor++
			m.fillTable()
		}
		return m, nil
	case "enter":
		m.openDetail()
		return m, nil
	case "f", "=":
		return m.addChip()
	case "-", "backspace":
		return m.removeLastChip()
	case "o":
		return m.toggleOrder()
	case "y":
		if v := m.cell(m.colCursor); v != "" {
			m.setStatus("yank: "+v+"   (clipboard arrives with M3)", false)
		}
		return m, nil
	case "s", "t", "u", "d", "r", "l", "m":
		return m.pivot(s)
	}
	var cmd tea.Cmd
	m.tbl, cmd = m.tbl.Update(k)
	return m, cmd
}

func (m *Model) openDetail() {
	if m.result == nil || m.result.Raw == nil || len(m.result.Raw) == 0 {
		return
	}
	i := m.tbl.Cursor()
	if i < 0 || i >= len(m.result.Raw) {
		return
	}
	m.openDetailRow(m.result.Raw[i])
}

func (m *Model) toggleDrawer() {
	m.drawerOpen = !m.drawerOpen
	if m.drawerOpen {
		m.focus = focusDrawer
		m.drawerFilter = ""
		m.drawerCursor = 0
		m.refreshDrawer()
	} else {
		m.focus = focusCmd
	}
}

func (m *Model) refreshDrawer() {
	if m.hist == nil {
		m.drawerItems = nil
		return
	}
	m.drawerItems = m.hist.Recent(m.drawerFilter, 200)
	if m.drawerCursor >= len(m.drawerItems) {
		m.drawerCursor = max(0, len(m.drawerItems)-1)
	}
}

func (m Model) drawerKey(k tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	s := k.String()
	switch s {
	case "up", "ctrl+p":
		if m.drawerCursor > 0 {
			m.drawerCursor--
		}
	case "down", "ctrl+n":
		if m.drawerCursor < len(m.drawerItems)-1 {
			m.drawerCursor++
		}
	case "enter", "alt+enter", "ctrl+enter":
		if len(m.drawerItems) > 0 {
			src := m.drawerItems[m.drawerCursor].Stmt
			m.cmd.SetValue(src)
			m.cmd.MoveToEnd()
			m.drawerOpen = false
			m.focus = focusCmd
			m.layout()
			if s != "enter" {
				return m.execute(src)
			}
		}
	case "backspace":
		if len(m.drawerFilter) > 0 {
			m.drawerFilter = m.drawerFilter[:len(m.drawerFilter)-1]
			m.refreshDrawer()
		}
	default:
		if k.Text != "" {
			m.drawerFilter += k.Text
			m.drawerCursor = 0
			m.refreshDrawer()
		}
	}
	return m, nil
}

// execute parses and runs statements from the command area.
func (m Model) execute(src string) (tea.Model, tea.Cmd) {
	stmts, err := lang.Parse(src)
	if err != nil {
		m.setStatus(err.Error(), true)
		return m, nil
	}
	if len(stmts) == 0 {
		return m, nil
	}
	// USE is session state, applied synchronously.
	var rest []lang.Stmt
	for _, st := range stmts {
		if u, ok := st.(*lang.UseStmt); ok {
			if u.Org {
				m.sess.Space = "*"
			} else {
				m.sess.Space = u.Space
			}
			m.setStatus("USE "+m.sess.Space, false)
			continue
		}
		rest = append(rest, st)
	}
	if len(rest) == 0 {
		m.record(src, 0, "")
		return m, nil
	}
	m.running = true
	m.runningSrc = src
	m.runStart = time.Now()
	m.setStatus("running…", false)
	sess := m.sess
	runner := m.runner
	return m, tea.Batch(tick(), func() tea.Msg {
		var last tea.Msg
		for _, st := range rest {
			msg, err := runner(context.Background(), st, sess)
			if err != nil {
				return errMsg{src: src, err: err}
			}
			last = msg
		}
		switch x := last.(type) {
		case resultMsg:
			x.src = src
			return x
		case textMsg:
			x.src = src
			return x
		case diffMsg:
			x.src = src
			return x
		}
		return last
	})
}

// rewrite replaces the command area with a statement and runs it.
func (m Model) rewrite(st *lang.SelectStmt) (tea.Model, tea.Cmd) {
	src := lang.StmtString(st)
	m.cmd.SetValue(src)
	m.cmd.MoveToEnd()
	m.layout()
	return m.execute(src)
}
