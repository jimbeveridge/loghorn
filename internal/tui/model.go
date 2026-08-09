package tui

import (
	"fmt"
	"strings"
	"time"

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

	// ingested counts every line read since startup, including routine ones the
	// display filters out and older ones the ring has since evicted. It is the
	// status bar's proof that the pipe is alive: "shown" can sit still for
	// minutes while lines are streaming in perfectly well.
	ingested int

	showDetail  bool
	detail      viewport.Model
	detailEntry entry.Entry // the entry currently shown in the detail pane

	// mouse reports whether clog is capturing mouse events. While it is, the
	// terminal hands clicks to us instead of using them for text selection, so
	// 'm' turns capture off when you want to select and copy a line.
	mouse        bool
	lastClickRow int
	lastClickAt  time.Time
	now          func() time.Time // injectable clock, for double-click timing

	width, height int
}

// doubleClickWindow is how close two clicks on the same row must be to count as
// a double-click.
const doubleClickWindow = 500 * time.Millisecond

func NewModel(ch <-chan entry.Entry, capacity, contextN int) Model {
	return Model{
		ch:           ch,
		ring:         buffer.New(capacity),
		contextN:     contextN,
		follow:       true,
		detail:       viewport.New(0, 0),
		mouse:        true,
		lastClickRow: -1,
		now:          time.Now,
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
		m.ingested++
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

	case tea.MouseMsg:
		return m.handleMouse(msg)
	}
	return m, nil
}

func (m Model) handleMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	switch msg.Button {
	case tea.MouseButtonWheelUp, tea.MouseButtonWheelDown:
		// The open detail pane owns the wheel, the same way it owns the
		// keyboard; otherwise the wheel walks the list one row per notch.
		if m.showDetail {
			var cmd tea.Cmd
			m.detail, cmd = m.detail.Update(msg)
			return m, cmd
		}
		delta := -1
		if msg.Button == tea.MouseButtonWheelDown {
			delta = 1
		}
		return m.moveSelection(delta), nil

	case tea.MouseButtonLeft:
		if msg.Action != tea.MouseActionPress {
			return m, nil // ignore the release half of a click
		}
		row := m.rowAtPoint(msg.X, msg.Y)
		if row < 0 {
			return m, nil // a gap marker, empty space, or the detail pane
		}
		double := row == m.lastClickRow && m.now().Sub(m.lastClickAt) < doubleClickWindow
		m.lastClickRow, m.lastClickAt = row, m.now()
		m.selected = row
		m.follow = row == len(m.rows)-1
		// Double-click opens the pane; a single click while it is already open
		// re-targets it at the row just clicked.
		if double || m.showDetail {
			m = m.openDetail()
		}
		return m, nil
	}
	return m, nil
}

// rowAtPoint maps a screen cell to a display row index, or -1 when the point is
// not over a selectable row: a "⋯" gap marker, empty space below the list, the
// status bar, or the detail pane's half of a split screen.
func (m Model) rowAtPoint(x, y int) int {
	width := m.width
	if m.showDetail {
		width = m.width / 2
		if x >= width {
			return -1
		}
	}
	lines := m.listLines(width)
	if y < 0 || y >= len(lines) {
		return -1
	}
	return lines[y].row
}

// moveSelection moves the cursor by delta rows, clamped to the ends, and
// re-derives follow: following iff parked on the newest row. Keys, the wheel and
// clicks all route through here so there is one scroll model rather than a
// separate mouse one.
func (m Model) moveSelection(delta int) Model {
	if len(m.rows) == 0 {
		return m
	}
	m.selected += delta
	if m.selected < 0 {
		m.selected = 0
	}
	if last := len(m.rows) - 1; m.selected > last {
		m.selected = last
	}
	m.follow = m.selected == len(m.rows)-1
	return m
}

// openDetail points the detail pane at the selected row and shows it.
func (m Model) openDetail() Model {
	if len(m.rows) == 0 {
		return m
	}
	m.detailEntry = m.rows[m.selected].Entry
	m.detail.Width, m.detail.Height = m.detailDims()
	m.detail.SetContent(m.wrappedDetail())
	m.detail.GotoTop()
	m.showDetail = true
	return m
}

func (m Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// Quit and the mouse toggle work from anywhere, including the detail pane.
	switch msg.String() {
	case "q", "ctrl+c":
		return m, tea.Quit
	case "m":
		// Handing tracking back to the terminal restores native text selection,
		// so a line can be selected and copied.
		m.mouse = !m.mouse
		if m.mouse {
			return m, tea.EnableMouseCellMotion
		}
		return m, tea.DisableMouse
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
		m = m.openDetail()
	case " ":
		m.follow = !m.follow
		if m.follow && len(m.rows) > 0 {
			m.selected = len(m.rows) - 1
		}
	case "j", "down":
		// moveSelection follows iff parked on the newest row: pressing down at
		// the bottom must NOT pause (there's nowhere to go), and walking down to
		// the tail resumes follow.
		m = m.moveSelection(1)
	case "k", "up":
		m = m.moveSelection(-1) // moved up, away from the live tail → pause
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
	if len(m.rows) == 0 {
		return 0, 0
	}
	// One line per row, plus one for each "⋯" gap marker the row draws — the
	// budget is in terminal lines, not rows. Overshooting it makes Bubble Tea
	// drop the top of the frame (standard_renderer keeps only the last height
	// lines), which reads as the list being cleared.
	budget := m.height - 1 // reserve the status bar line
	if budget < 1 {
		budget = 1
	}
	start, end = m.growUp(len(m.rows), budget)
	if m.selected < start {
		// Selection has been scrolled above the tail window: anchor on it and
		// grow downward instead.
		start, end = m.growDown(m.selected, budget)
	}
	return start, end
}

// growUp fills budget lines backward from end (exclusive), returning the window
// it settled on. The topmost row draws no gap marker, so it is charged one line.
func (m Model) growUp(end, budget int) (int, int) {
	used, start := 0, end
	for i := end - 1; i >= 0; i-- {
		cost := m.rowLines(i, false)
		if used+cost > budget {
			// It may still fit as the window's first row, where the marker is
			// suppressed.
			if used+m.rowLines(i, true) <= budget {
				start = i
			}
			break
		}
		used += cost
		start = i
	}
	return start, end
}

// growDown fills budget lines forward from start, returning the window it
// settled on.
func (m Model) growDown(start, budget int) (int, int) {
	used, end := 0, start
	for i := start; i < len(m.rows); i++ {
		cost := m.rowLines(i, i == start)
		if used+cost > budget {
			break
		}
		used += cost
		end = i + 1
	}
	return start, end
}

// rowLines is how many terminal lines row i occupies: one for the row itself,
// plus one for its gap marker unless it is the window's first row.
func (m Model) rowLines(i int, first bool) int {
	if m.rows[i].GapBefore && !first {
		return 2
	}
	return 1
}

// listLine is one rendered line of the list paired with the display row it
// shows. A "⋯" gap marker gets row -1: it is drawn, but there is nothing there
// to select.
type listLine struct {
	text string
	row  int
}

// listLines lays the visible list out exactly as it is drawn. Rendering and
// mouse hit-testing both read it, so a click can never land on a different row
// than the one under the cursor — gap markers mean a screen line index is not a
// row index.
func (m Model) listLines(width int) []listLine {
	start, end := m.listWindow()
	lines := make([]listLine, 0, end-start)
	for i := start; i < end; i++ {
		row := m.rows[i]
		// Preface each line with the wall-clock time we ingested it (no date),
		// e.g. 15:04:05.000. Budget the message width around the prefix, the
		// timestamp, and one separating space.
		ts := row.Entry.Received.Format(tsLayout)
		msg := truncate(flatten(row.Entry.Message), width-len(ts)-3)
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
			lines = append(lines, listLine{text: gapStyle.Render("  ⋯"), row: -1})
		}
		lines = append(lines, listLine{text: prefix + line, row: i})
	}
	return lines
}

func (m Model) listView(width int) string {
	// Joined, not appended with a trailing "\n": a trailing newline would render
	// as one more (blank) line and push the frame past the terminal height.
	lines := m.listLines(width)
	texts := make([]string, len(lines))
	for i, l := range lines {
		texts[i] = l.text
	}
	return lipgloss.NewStyle().Width(width).Render(strings.Join(texts, "\n"))
}

func (m Model) statusBar() string {
	if m.showDetail {
		pos := "all"
		if !(m.detail.AtTop() && m.detail.AtBottom()) {
			pos = fmt.Sprintf("%d%%", int(m.detail.ScrollPercent()*100))
		}
		return statusStyle.Render(fmt.Sprintf(
			" clog · detail %s · j/k scroll · spc page · esc close · q quit", pos))
	}
	mode := "PAUSED"
	if m.follow {
		mode = "FOLLOW"
	}
	start, end := m.listWindow()
	base := statusStyle.Render(fmt.Sprintf(
		" clog · %s · %d lines · %d shown", mode, m.ingested, len(m.rows)))
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
	// Hints are terse because the bar has to fit a terminal width: where the key
	// name already carries the meaning, the verb is dropped. State is rendered
	// before hints, so a narrow window loses hints rather than state.
	tail := statusStyle.Render(fmt.Sprintf(
		" · j/k · spc %s · enter open · m mouse:%s · q quit",
		toggleWord(m.follow), onOff(m.mouse)))
	return base + more + tail
}

func toggleWord(follow bool) string {
	if follow {
		return "pause"
	}
	return "follow"
}

func onOff(b bool) string {
	if b {
		return "on"
	}
	return "off"
}

// flatten collapses a message onto the one terminal line the list gives it.
// Messages decoded from JSON carry real newlines — stack traces above all — and
// rendering those verbatim makes a single row span many lines, pushing the frame
// past the terminal height; Bubble Tea then drops the *top* of the frame, so the
// list looks like it was wiped by the arriving error. The escapes are ASCII, so
// the width budget still holds. The full text is one 'enter' away in the detail
// pane, where newlines are printed as newlines.
var msgEscapes = strings.NewReplacer("\r\n", `\n`, "\n", `\n`, "\r", `\r`, "\t", `\t`)

func flatten(s string) string { return msgEscapes.Replace(s) }

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
