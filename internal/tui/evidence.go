package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/confighub/cub-commander/internal/scout"
)

// One captured observation, not a live cache. Refresh replaces it before I/O;
// pointer identity prevents late responses crossing a navigation or refresh.
type evidenceState struct {
	generation uint64
	request    scout.Request
	snapshot   *scout.Snapshot
	err        error
	loading    bool
	cancel     context.CancelFunc
}

func (s *evidenceState) stop() {
	if s != nil && s.cancel != nil {
		s.cancel()
		s.cancel = nil
		s.loading = false
	}
}

func (s *evidenceState) label(now time.Time) string {
	switch {
	case s == nil || s.err != nil:
		return "unavailable"
	case s.loading:
		return "reading"
	case s.snapshot == nil:
		return "cancelled"
	case !s.snapshot.Available:
		return "unavailable"
	case !now.Before(s.snapshot.ExpiresAt) || now.Before(s.snapshot.ObservedAt):
		return "STALE"
	default:
		return "snapshot"
	}
}

type evidenceMsg struct {
	state    *evidenceState
	snapshot scout.Snapshot
	err      error
}
type evidenceExpiredMsg struct{ generation uint64 }

func (m Model) evidenceVisible() bool {
	return m.mode == modeDetail && m.det != nil && m.det.entity == "Resource" && m.det.tab == 2 && !m.chooserOpen
}

func (m *Model) loadEvidence(refresh bool) tea.Cmd {
	d := m.det
	if d == nil || d.entity != "Resource" {
		return nil
	}
	req, err := scout.Resolve(d.row, m.scoutConfig.Bindings)
	if e := d.evidence; !refresh && err == nil && e != nil && e.request == req && (e.loading || e.snapshot != nil || e.err != nil) {
		return nil
	}
	d.evidence.stop()
	m.evidenceGeneration++
	s := &evidenceState{generation: m.evidenceGeneration, request: req, err: err}
	d.evidence = s
	if err == nil && m.evidenceLoader == nil {
		s.err = fmt.Errorf("Scout provider is not configured")
	}
	if s.err != nil {
		m.renderDetail()
		return nil
	}
	parent := m.evidenceContext
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithCancel(parent)
	s.cancel, s.loading = cancel, true
	load := m.evidenceLoader
	m.renderDetail()
	return func() tea.Msg {
		snapshot, err := load(ctx, req)
		return evidenceMsg{state: s, snapshot: snapshot, err: err}
	}
}

func (m *Model) evidenceLoaded(msg evidenceMsg) tea.Cmd {
	if !m.evidenceVisible() || m.det.evidence != msg.state || !msg.state.loading {
		return nil
	}
	s := msg.state
	s.stop()
	s.err = msg.err
	if msg.err == nil {
		s.snapshot = &msg.snapshot
	}
	m.renderDetail()
	if s.snapshot != nil && s.snapshot.Available && time.Now().Before(s.snapshot.ExpiresAt) {
		// A single local expiry notification, never another cluster request.
		// Carry a number so abandoned timers do not retain old snapshots.
		generation := s.generation
		return tea.Tick(time.Until(s.snapshot.ExpiresAt), func(time.Time) tea.Msg { return evidenceExpiredMsg{generation: generation} })
	}
	return nil
}

func (m Model) evidenceBody(now time.Time) string {
	s := m.det.evidence
	if s == nil {
		return "Evidence unavailable"
	}
	var b strings.Builder
	if s.err != nil {
		// Adapter errors are controlled messages, never raw provider stderr.
		fmt.Fprintf(&b, "Evidence unavailable\n%s\n", s.err)
	} else if s.loading {
		b.WriteString("Reading bounded evidence...\n")
	} else if s.snapshot == nil {
		b.WriteString("Read cancelled; no observation\n")
	} else if !s.snapshot.Available {
		b.WriteString("Evidence unavailable; no live object was observed\n")
	} else {
		label := "Captured snapshot"
		if !now.Before(s.snapshot.ExpiresAt) || now.Before(s.snapshot.ObservedAt) {
			label = "STALE snapshot"
		}
		fmt.Fprintf(&b, "%s\nObserved: %s\nExpires:  %s\n", label, s.snapshot.ObservedAt.Format(time.RFC3339Nano), s.snapshot.ExpiresAt.Format(time.RFC3339Nano))
	}
	r := s.request
	if r.Validate() == nil {
		bin, args := m.scoutConfig.Command(r)
		fmt.Fprintf(&b, "Target ID: %s\nContext: %s\n%s %s/%s (namespace %q)\n\n%s\n", r.TargetID, r.Context,
			r.Resource.APIVersion, r.Resource.Kind, r.Resource.Name, r.Resource.Namespace, shellLines(append([]string{bin}, args...), m.mainWidth()))
	}
	if s.snapshot != nil {
		fmt.Fprintf(&b, "\n%s\n", s.snapshot.JSON)
	}
	return ansi.Hardwrap(ansi.Strip(b.String()), m.mainWidth(), true)
}

// Wrap outside quotes, using shell continuations, so the displayed invocation
// still preserves every argument when copied from a narrow terminal.
func shellLines(words []string, width int) string {
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }
	var b strings.Builder
	column := 0
	for i, word := range words {
		q := quote(word)
		if i > 0 {
			if column+1+ansi.StringWidth(q) > width-2 {
				b.WriteString(" \\\n")
				column = 0
			} else {
				b.WriteByte(' ')
				column++
			}
		}
		var chunk string
		for _, r := range word {
			next := chunk + string(r)
			if column+ansi.StringWidth(quote(next)) > width-2 && chunk != "" {
				b.WriteString(quote(chunk))
				b.WriteString("\\\n")
				column = 0
				chunk = ""
			}
			chunk += string(r)
		}
		q = quote(chunk)
		b.WriteString(q)
		column += ansi.StringWidth(q)
	}
	return b.String()
}

func (m *Model) renderEvidencePreservingScroll() {
	offset := m.detail.YOffset()
	m.renderDetail()
	m.detail.SetYOffset(offset)
}
