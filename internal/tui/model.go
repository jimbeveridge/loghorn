package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"clog/internal/buffer"
	"clog/internal/entry"
)

type entryMsg entry.Entry
type doneMsg struct{}

var (
	dimStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
	impStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("203")).Bold(true)
	selStyle    = lipgloss.NewStyle().Background(lipgloss.Color("236"))
	gapStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	statusStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("39"))
	moreStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("214")).Bold(true)
	tsStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("244"))
)

// tsLayout formats the per-line ingest time as HH:MM:SS.mmm (no date).
const tsLayout = "15:04:05.000"

type Model struct {
	ch       <-chan entry.Entry
	ring     *buffer.Ring
	contextN int

	rows     []Row
	selected int
	follow   bool

	showDetail  bool
	detail      viewport.Model
	detailEntry entry.Entry // the entry currently shown in the detail pane

	width, height int
}

func NewModel(ch <-chan entry.Entry, capacity, contextN int) Model {
	return Model{
		ch:       ch,
		ring:     buffer.New(capacity),
		contextN: contextN,
		follow:   true,
		detail:   viewport.New(0, 0),
	}
}

func (m Model) Init() tea.Cmd {
	return waitForEntry(m.ch)
}

func waitForEntry(ch <-chan entry.Entry) tea.Cmd {
	if ch == nil {
		return nil
	}
	return func() tea.Msg {
		e, ok := <-ch
		if !ok {
			return doneMsg{}
		}
		return entryMsg(e)
	}
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		if m.showDetail {
			m.detail.Width, m.detail.Height = m.detailDims()
			m.detail.SetContent(m.wrappedDetail()) // re-wrap to the new pane width
		}
		return m, nil

	case entryMsg:
		m.ring.Append(entry.Entry(msg))
		m.rows = BuildDisplay(m.ring.Snapshot(), m.contextN)
		if m.follow && len(m.rows) > 0 {
			m.selected = len(m.rows) - 1
		}
		return m, waitForEntry(m.ch)

	case doneMsg:
		return m, nil

	case tea.KeyMsg:
		return m.handleKey(msg)
	}
	return m, nil
}

func (m Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// Quit works from anywhere.
	if s := msg.String(); s == "q" || s == "ctrl+c" {
		return m, tea.Quit
	}

	// When the detail pane is open it owns the keyboard: Esc closes it, and
	// every other key scrolls the viewport (j/k, arrows, page keys).
	if m.showDetail {
		if msg.String() == "esc" {
			m.showDetail = false
			return m, nil
		}
		var cmd tea.Cmd
		m.detail, cmd = m.detail.Update(msg)
		return m, cmd
	}

	// List mode.
	switch msg.String() {
	case "enter":
		if len(m.rows) > 0 {
			m.detailEntry = m.rows[m.selected].Entry
			m.detail.Width, m.detail.Height = m.detailDims()
			m.detail.SetContent(m.wrappedDetail())
			m.detail.GotoTop()
			m.showDetail = true
		}
	case " ":
		m.follow = !m.follow
		if m.follow && len(m.rows) > 0 {
			m.selected = len(m.rows) - 1
		}
	case "j", "down":
		if m.selected < len(m.rows)-1 {
			m.selected++
		}
		// Follow iff parked on the newest row. Pressing down while already at
		// the bottom must NOT pause (there's nowhere to go); walking down to
		// the tail resumes follow.
		m.follow = len(m.rows) > 0 && m.selected == len(m.rows)-1
	case "k", "up":
		if m.selected > 0 {
			m.selected--
			m.follow = false // moved up, away from the live tail → pause
		}
	case "g":
		m.selected = 0
		m.follow = len(m.rows) <= 1 // top is the tail only with a single row
	case "G":
		if len(m.rows) > 0 {
			m.selected = len(m.rows) - 1
		}
		m.follow = true // jump to newest → resume follow
	}
	return m, nil
}

// detailDims returns the width and height of the detail pane, matching the
// split layout used by View (list takes the left half, detail the right).
func (m Model) detailDims() (w, h int) {
	listWidth := m.width / 2
	w = m.width - listWidth - 1
	if w < 1 {
		w = 1
	}
	h = m.height - 1 // reserve the status bar line
	if h < 1 {
		h = 1
	}
	return w, h
}

func renderDetail(e entry.Entry) string {
	if e.JSON != nil {
		return RenderJSON(e.JSON, false)
	}
	return string(e.Raw)
}

// wrappedDetail renders the inspected entry and wraps it to the detail pane
// width so wide JSON values aren't clipped off the right edge — everything is
// reachable by scrolling vertically instead.
func (m Model) wrappedDetail() string {
	w, _ := m.detailDims()
	return lipgloss.NewStyle().Width(w).Render(renderDetail(m.detailEntry))
}

func (m Model) View() string {
	if m.width == 0 {
		return "starting clog…"
	}
	if m.showDetail {
		listWidth := m.width / 2
		list := m.listView(listWidth)
		body := lipgloss.JoinHorizontal(lipgloss.Top, list, " ", m.detail.View())
		return body + "\n" + m.statusBar()
	}
	return m.listView(m.width) + "\n" + m.statusBar()
}

// listWindow returns the [start, end) range of display rows currently visible
// in the list for the terminal height. Rows before start are older and unseen;
// rows at end or beyond are newer and unseen — which only happens while paused,
// since follow mode pins the window to the last row. "Rows" here is the active
// display set (in v0, important lines + context), so the counts always reflect
// whatever the current view is filtering to.
func (m Model) listWindow() (start, end int) {
	visible := m.height - 1
	if visible < 1 {
		visible = 1
	}
	if len(m.rows) <= visible {
		return 0, len(m.rows)
	}
	start = len(m.rows) - visible
	if m.selected < start {
		start = m.selected
	}
	end = start + visible
	if end > len(m.rows) {
		end = len(m.rows)
	}
	return start, end
}

func (m Model) listView(width int) string {
	var b strings.Builder
	start, end := m.listWindow()
	for i := start; i < end; i++ {
		row := m.rows[i]
		// Preface each line with the wall-clock time we ingested it (no date),
		// e.g. 15:04:05.000. Budget the message width around the prefix, the
		// timestamp, and one separating space.
		ts := row.Entry.Received.Format(tsLayout)
		msg := truncate(row.Entry.Message, width-len(ts)-3)
		switch row.Kind {
		case RowImportant:
			msg = impStyle.Render(msg)
		case RowContext:
			msg = dimStyle.Render(msg)
		}
		line := tsStyle.Render(ts) + " " + msg
		prefix := "  "
		if i == m.selected {
			prefix = "▶ "
			line = selStyle.Render(line)
		}
		if row.GapBefore && i != start {
			b.WriteString(gapStyle.Render("  ⋯") + "\n")
		}
		b.WriteString(prefix + line + "\n")
	}
	return lipgloss.NewStyle().Width(width).Render(b.String())
}

func (m Model) statusBar() string {
	if m.showDetail {
		pos := "all shown"
		if !(m.detail.AtTop() && m.detail.AtBottom()) {
			pos = fmt.Sprintf("%d%%", int(m.detail.ScrollPercent()*100))
		}
		return statusStyle.Render(fmt.Sprintf(
			" clog · detail [%s] · j/k scroll · space page · esc close · q quit", pos))
	}
	mode := "PAUSED"
	if m.follow {
		mode = "FOLLOW"
	}
	start, end := m.listWindow()
	base := statusStyle.Render(fmt.Sprintf(" clog · %s · %d shown", mode, len(m.rows)))
	// Unseen content: ▲ older above the window, ▼ newer below it. Below is only
	// non-zero while paused (follow keeps the window at the bottom), so a ▼N
	// flags "you're paused and N newer lines have arrived out of view."
	var more string
	if above := start; above > 0 {
		more += moreStyle.Render(fmt.Sprintf(" ▲%d", above))
	}
	if below := len(m.rows) - end; below > 0 {
		more += moreStyle.Render(fmt.Sprintf(" ▼%d new", below))
	}
	tail := statusStyle.Render(fmt.Sprintf(
		" · j/k move · space %s · enter inspect · q quit", toggleWord(m.follow)))
	return base + more + tail
}

func toggleWord(follow bool) string {
	if follow {
		return "pause"
	}
	return "follow"
}

func truncate(s string, w int) string {
	if w < 1 {
		w = 1
	}
	if len(s) <= w {
		return s
	}
	if w <= 1 {
		return "…"
	}
	return s[:w-1] + "…"
}
