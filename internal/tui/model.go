package tui

import (
	"fmt"
	"strconv"
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

// childExitMsg reports that the launched producer exited on its own.
type childExitMsg struct{ Code int }

// Child is the producer clog launched and owns. It is nil when clog is reading a
// pipe, where there is nothing to forward keys to or shut down.
type Child interface {
	// Send writes keystrokes to the child's stdin.
	Send(p []byte) error
	// Terminate signals the child's process group and waits for it to die,
	// escalating to SIGKILL after a grace period.
	Terminate()
	// Name is the command as invoked, for display.
	Name() string
}

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

	rows []Row

	// selected is a cursor position in [0, len(rows)]. The extra position past
	// the last row IS the status bar — the windowshade's pull handle. Resting
	// there means output is live; anywhere else means the shade is down. There
	// is no separate follow flag to keep in sync: see onShade.
	selected int

	// top is the first visible row. While live it is recomputed from the tail
	// each frame so the window rides the newest row; while held it stays put, so
	// arriving rows pile up below the window instead of scrolling the view.
	top int

	// heldRows and heldLines snapshot the counters at the moment the shade came
	// down, so the bar can report what is waiting behind it.
	heldRows, heldLines int

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

	// child is the producer clog launched, or nil when reading a pipe.
	child Child
	// forwarding sends every keystroke to the child instead of acting on it, so
	// a dev server's own shortcuts stay reachable.
	forwarding bool
	// childExited records the producer's exit; the TUI stays open so the crash
	// that killed it is still there to scroll back through.
	childExited bool
	childCode   int

	width, height int
}

// SetChild attaches the launched producer. Without one, quit is just quit and
// there is nothing to forward keys to.
func (m *Model) SetChild(c Child) { m.child = c }

// ChildExited reports the producer's exit into the update loop.
func ChildExited(code int) tea.Msg { return childExitMsg{Code: code} }

// doubleClickWindow is how close two clicks on the same row must be to count as
// a double-click.
const doubleClickWindow = 500 * time.Millisecond

func NewModel(ch <-chan entry.Entry, capacity, contextN int) Model {
	return Model{
		ch:           ch,
		ring:         buffer.New(capacity),
		contextN:     contextN,
		detail:       viewport.New(0, 0),
		mouse:        true,
		lastClickRow: -1,
		now:          time.Now,
	}
	// selected starts at 0 with no rows, which is already the shade: clog opens
	// live.
}

// onShade reports whether the cursor is resting on the status bar — the shade's
// pull handle — which is exactly the condition for output being live.
func (m Model) onShade() bool { return m.selected >= len(m.rows) }

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
		live := m.onShade() // ask before the row count changes under us
		m.ingested++
		m.ring.Append(entry.Entry(msg))
		m.rows = BuildDisplay(m.ring.Snapshot(), m.contextN)
		if live {
			m.selected = len(m.rows) // ride the handle as the list grows
		}
		return m, waitForEntry(m.ch)

	case doneMsg:
		return m, nil

	case childExitMsg:
		// Stay open: a crash is exactly when the scrollback is worth reading.
		m.childExited, m.childCode = true, msg.Code
		m.forwarding = false // nothing left to forward to
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
		if m.onShadeLine(msg.Y) {
			return m.grabShade(), nil // pulled the handle
		}
		row := m.rowAtPoint(msg.X, msg.Y)
		if row < 0 {
			return m, nil // a gap marker, empty space, or the detail pane
		}
		double := row == m.lastClickRow && m.now().Sub(m.lastClickAt) < doubleClickWindow
		m.lastClickRow, m.lastClickAt = row, m.now()
		m = m.setSelected(row) // clicking a row pulls the shade down
		// Double-click opens the pane; a single click while it is already open
		// re-targets it at the row just clicked.
		if double || m.showDetail {
			m = m.openDetail()
		}
		return m, nil
	}
	return m, nil
}

// terminateThenQuit shuts the producer down before leaving. It runs as a command
// rather than inline so the grace period doesn't freeze the UI.
func terminateThenQuit(c Child) tea.Cmd {
	return func() tea.Msg {
		c.Terminate()
		return tea.Quit()
	}
}

// keyBytes renders a key event as the bytes a terminal would have delivered, so
// the child sees what it would have seen had it owned the keyboard.
func keyBytes(msg tea.KeyMsg) []byte {
	switch msg.Type {
	case tea.KeyRunes:
		return []byte(string(msg.Runes))
	case tea.KeySpace:
		return []byte(" ")
	case tea.KeyEnter:
		return []byte("\r")
	case tea.KeyTab:
		return []byte("\t")
	case tea.KeyBackspace:
		return []byte{0x7f}
	default:
		// Control keys and escape sequences already stringify to their bytes for
		// the simple cases; anything exotic is dropped rather than guessed at.
		if s := msg.String(); len(s) == 1 {
			return []byte(s)
		}
		return nil
	}
}

// onShadeLine reports whether a screen row is the shade's pull handle. The
// detail pane's bar is a different bar and is not grabbable.
func (m Model) onShadeLine(y int) bool {
	return !m.showDetail && m.height > 0 && y == m.height-1
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

// setSelected moves the cursor to a position in [0, len(rows)], where len(rows)
// is the shade's pull handle. Keys, the wheel and clicks all route through here,
// so there is one place that knows how grabbing and releasing the shade works.
//
// Leaving the handle pulls the shade down: the window is pinned where it stands
// so the view stops moving, and the counters are snapshotted so the bar can
// report what is piling up behind it.
func (m Model) setSelected(i int) Model {
	if i < 0 {
		i = 0
	}
	if i > len(m.rows) {
		i = len(m.rows)
	}
	if m.onShade() && i < len(m.rows) {
		// Read the window while still live — it is tail-anchored — and freeze it
		// there.
		m.top, _ = m.listWindow()
		m.heldRows, m.heldLines = len(m.rows), m.ingested
	}
	m.selected = i
	if !m.onShade() {
		m = m.scrollToCursor()
	}
	return m
}

// moveSelection walks the cursor by delta positions.
func (m Model) moveSelection(delta int) Model { return m.setSelected(m.selected + delta) }

// grabShade puts the cursor back on the handle, which resumes live output.
func (m Model) grabShade() Model { return m.setSelected(len(m.rows)) }

// scrollToCursor nudges the held window's anchor by the minimum needed to keep
// the cursor on screen. It never moves further than that, so a held view stays
// as still as it can.
func (m Model) scrollToCursor() Model {
	if m.selected < m.top {
		m.top = m.selected
		return m
	}
	for m.top < len(m.rows)-1 {
		if _, end := m.listWindow(); m.selected < end {
			break
		}
		m.top++
	}
	return m
}

// openDetail points the detail pane at the selected row and shows it. With the
// cursor on the handle there is no selected row, so the newest one is inspected.
func (m Model) openDetail() Model {
	if len(m.rows) == 0 {
		return m
	}
	i := m.selected
	if i >= len(m.rows) {
		i = len(m.rows) - 1
	}
	m.detailEntry = m.rows[i].Entry
	m.detail.Width, m.detail.Height = m.detailDims()
	m.detail.SetContent(m.wrappedDetail())
	m.detail.GotoTop()
	m.showDetail = true
	return m
}

func (m Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// Forward mode owns the keyboard completely — including 'q', which is the
	// whole point: the child needs to see it. Only esc gets out.
	if m.forwarding {
		if msg.String() == "esc" {
			m.forwarding = false
			return m, nil
		}
		if m.child != nil {
			_ = m.child.Send(keyBytes(msg))
		}
		return m, nil
	}

	// Quit and the mouse toggle work from anywhere, including the detail pane.
	switch msg.String() {
	case "q", "ctrl+c":
		// Take the producer down with us. Nothing to do in pipe mode, or once
		// the child has already exited.
		if m.child != nil && !m.childExited {
			return m, terminateThenQuit(m.child)
		}
		return m, tea.Quit
	case "Q":
		// Detach: leave the child running, with its output now going nowhere.
		return m, tea.Quit
	case "f":
		// Hand the keyboard to the child so its own shortcuts (vite's r,
		// nodemon's rs) stay reachable.
		if m.child != nil && !m.childExited {
			m.forwarding = true
		}
		return m, nil
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
		// Toggle: off the handle holds the shade at the newest row, back on it
		// goes live.
		if m.onShade() {
			m = m.setSelected(len(m.rows) - 1)
		} else {
			m = m.grabShade()
		}
	case "j", "down":
		// Down from the newest row lands on the handle and goes live.
		m = m.moveSelection(1)
	case "k", "up":
		// Up off the handle pulls the shade down.
		m = m.moveSelection(-1)
	case "g":
		m = m.setSelected(0)
	case "G":
		m = m.grabShade()
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
	if m.onShade() {
		// Live: the window rides the newest row.
		return m.growUp(len(m.rows), budget)
	}
	// Held: pinned to the frozen anchor, so arriving rows pile up below the
	// window instead of scrolling the view out from under you.
	top := m.top
	if top > len(m.rows)-1 {
		top = len(m.rows) - 1
	}
	if top < 0 {
		top = 0
	}
	return m.growDown(top, budget)
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
	// Forward mode replaces the hints entirely: 'q' now goes to the child rather
	// than quitting, so it has to be unmistakable which mode you are in.
	if m.forwarding {
		name := "the child"
		if m.child != nil {
			name = m.child.Name()
		}
		return "▶ " + selStyle.Render(moreStyle.Render(
			fmt.Sprintf("clog %s · FORWARDING — every key goes to %s · esc to stop",
				m.spinner(), name)))
	}

	if m.showDetail {
		pos := "all"
		if !(m.detail.AtTop() && m.detail.AtBottom()) {
			pos = fmt.Sprintf("%d%%", int(m.detail.ScrollPercent()*100))
		}
		// The spinner rides along here too, so ingest stays visible while you
		// are reading an entry.
		return statusStyle.Render(fmt.Sprintf(
			"   clog %s · detail %s · j/k scroll · spc page · esc close · q quit",
			m.spinner(), pos))
	}

	live := m.onShade()
	mode := "HELD"
	if live {
		mode = "LIVE"
	}
	base := statusStyle.Render(fmt.Sprintf(
		"clog %s · %s · %s lines · %d shown", m.spinner(), mode, comma(m.ingested), len(m.rows)))

	var more string
	if start, _ := m.listWindow(); start > 0 {
		more += moreStyle.Render(fmt.Sprintf(" · ▲%d", start))
	}
	if !live {
		// What is piling up behind the shade. Both figures measure from the
		// moment it came down, so the ratio shows how much of the stream the
		// display filter is holding back.
		more += moreStyle.Render(fmt.Sprintf(" · ▼%d of %s waiting",
			len(m.rows)-m.heldRows, comma(m.ingested-m.heldLines)))
	}

	// A dead producer is worth saying loudly — the logs on screen are the last
	// thing it did.
	if m.childExited {
		more += moreStyle.Render(fmt.Sprintf(" · child exited (%d)", m.childCode))
	}

	// Hints are terse because the bar has to fit a terminal width: where the key
	// name already carries the meaning, the verb is dropped. State is rendered
	// before hints, so a narrow window loses hints rather than state.
	quitHint := "q quit"
	forwardHint := ""
	if m.child != nil && !m.childExited {
		quitHint = "q quit+stop · Q detach" // q takes the producer with it
		forwardHint = " · f keys→child"
	}
	tail := statusStyle.Render(fmt.Sprintf(
		" · j/k · spc %s · enter open%s · m mouse:%s · %s",
		toggleWord(live), forwardHint, onOff(m.mouse), quitHint))

	// The handle carries the cursor and the selected-row highlight while it
	// holds it, so "where is the cursor" has one consistent answer.
	line := base + more + tail
	if live {
		return "▶ " + selStyle.Render(line)
	}
	return "  " + line
}

// spinnerFrames advance one frame per ingested line, so the glyph moves exactly
// when data flows and freezes solid the instant it stops. A time-based pulse
// would need a ticker to turn itself off and would repaint while idle; this
// needs neither, and a fast stream visibly spins faster than a trickle.
var spinnerFrames = []rune("⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏")

func (m Model) spinner() string {
	return string(spinnerFrames[m.ingested%len(spinnerFrames)])
}

// comma groups thousands, so a six-figure line count stays readable at a glance.
func comma(n int) string {
	s := strconv.Itoa(n)
	var b strings.Builder
	for i := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

func toggleWord(live bool) string {
	if live {
		return "hold"
	}
	return "live"
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
