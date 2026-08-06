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
)

type Model struct {
	ch       <-chan entry.Entry
	ring     *buffer.Ring
	contextN int

	rows     []Row
	selected int
	follow   bool

	showDetail bool
	detail     viewport.Model

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
			m.detail.Width, m.detail.Height = m.detailDims()
			m.detail.SetContent(renderDetail(m.rows[m.selected].Entry))
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
		m.follow = false
	case "k", "up":
		if m.selected > 0 {
			m.selected--
		}
		m.follow = false
	case "g":
		m.selected = 0
		m.follow = false
	case "G":
		if len(m.rows) > 0 {
			m.selected = len(m.rows) - 1
		}
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

func (m Model) listView(width int) string {
	var b strings.Builder
	// Show a trailing window of rows that fits the height (minus status bar).
	visible := m.height - 1
	if visible < 1 {
		visible = 1
	}
	start := 0
	if len(m.rows) > visible {
		start = len(m.rows) - visible
		if m.selected < start {
			start = m.selected
		}
	}
	for i := start; i < len(m.rows) && i < start+visible; i++ {
		row := m.rows[i]
		line := truncate(row.Entry.Message, width-2)
		styled := line
		switch row.Kind {
		case RowImportant:
			styled = impStyle.Render(line)
		case RowContext:
			styled = dimStyle.Render(line)
		}
		prefix := "  "
		if i == m.selected {
			prefix = "▶ "
			styled = selStyle.Render(styled)
		}
		if row.GapBefore && i != start {
			b.WriteString(gapStyle.Render("  ⋯") + "\n")
		}
		b.WriteString(prefix + styled + "\n")
	}
	return lipgloss.NewStyle().Width(width).Render(b.String())
}

func (m Model) statusBar() string {
	if m.showDetail {
		return statusStyle.Render(
			" clog · detail · j/k scroll · space page · esc close · q quit")
	}
	mode := "PAUSED"
	if m.follow {
		mode = "FOLLOW"
	}
	return statusStyle.Render(fmt.Sprintf(
		" clog · %s · %d shown · j/k move · space %s · enter inspect · q quit",
		mode, len(m.rows), toggleWord(m.follow)))
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
