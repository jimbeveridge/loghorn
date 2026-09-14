package tui

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/cellbuf"

	"github.com/jimbeveridge/loghorn/internal/buffer"
	"github.com/jimbeveridge/loghorn/internal/clipboard"
	"github.com/jimbeveridge/loghorn/internal/entry"
)

type entryMsg entry.Entry
type doneMsg struct{}

// childExitMsg reports that the launched producer exited on its own.
type childExitMsg struct{ Code int }

// Child is the producer loghorn launched and owns. It is nil when loghorn is reading a
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
	statusStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("39"))
	moreStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("214")).Bold(true)
	tsStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("244"))
)

// tsLayout formats the per-line ingest time as HH:MM:SS.mmm (no date).
const tsLayout = "15:04:05.000"

type Model struct {
	ch   <-chan entry.Entry
	ring *buffer.Ring

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

	// topSeq and selSeq are the Entry.Seq of the rows top and selected pointed
	// at when last set. The ring is bounded, so an arriving line can evict an
	// old one and shift every index in m.rows even though nothing the user did
	// changed — rebuild relocates top and selected by these instead of trusting
	// the old index, or a held view would silently drift toward newer content
	// as the ring wrapped underneath it.
	topSeq, selSeq int

	// heldRows and heldLines snapshot the counters at the moment the shade came
	// down, so the bar can report what is waiting behind it.
	heldRows, heldLines int

	// The two view filters. They stack: with both on you see the failures within
	// one request, which is the combination worth having.
	//
	// showAll turns the failures filter off — every line is shown, failures still
	// styled as such. pinned narrows the view to corrID; the empty id is a real
	// value, meaning "the lines belonging to no request at all", so the pin needs
	// its own flag rather than using "" as off.
	showAll bool
	pinned  bool
	corrID  string

	// notice is a one-shot message on the status bar, cleared by the next
	// keystroke. Used where a key legitimately does nothing and silence would
	// read as a bug.
	notice string

	// ingested counts every line read since startup, including routine ones the
	// display filters out and older ones the ring has since evicted. It is the
	// status bar's proof that the pipe is alive: "shown" can sit still for
	// minutes while lines are streaming in perfectly well.
	ingested int

	showDetail  bool
	detail      viewport.Model
	detailEntry entry.Entry // the entry currently shown in the detail pane
	detailW     int         // pane width, sized to the entry; see detailWidth

	// showHelp overlays the key reference. Keys you reach for rarely live here
	// rather than on the status bar, which has to stay readable at a glance; the
	// bar advertises '?' so they stay discoverable. It scrolls, so a short
	// terminal still reaches every line.
	showHelp bool
	help     viewport.Model

	// mouse reports whether loghorn is capturing mouse events. While it is, the
	// terminal hands clicks to us instead of using them for text selection, so
	// 'm' turns capture off when you want to select and copy a line.
	mouse        bool
	lastClickRow int
	lastClickAt  time.Time
	now          func() time.Time   // injectable clock, for double-click timing
	copy         func(string) error // injectable clipboard, so tests never touch the real one

	// child is the producer loghorn launched, or nil when reading a pipe.
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

func NewModel(ch <-chan entry.Entry, capacity int) Model {
	return Model{
		ch:           ch,
		ring:         buffer.New(capacity),
		detail:       viewport.New(0, 0),
		help:         viewport.New(0, 0),
		mouse:        true,
		lastClickRow: -1,
		now:          time.Now,
		copy:         clipboard.Copy,
	}
	// selected starts at 0 with no rows, which is already the shade: loghorn opens
	// live.
}

// onShade reports whether the cursor is resting on the status bar — the shade's
// pull handle — which is exactly the condition for output being live.
func (m Model) onShade() bool { return m.selected >= len(m.rows) }

// rebuild recomputes the visible rows from the ring through the active filters.
// They stack: the correlation filter narrows the stream to one request, and the
// failures filter then picks the important lines out of what is left — so both
// on means "the failures in this request".
//
// Everything that changes the row set goes through here, so the cursor and the
// held window can be re-anchored in one place.
func (m Model) rebuild() Model {
	live := m.onShade() // ask before the row count changes under us

	entries := m.ring.Snapshot()
	if m.pinned {
		kept := make([]entry.Entry, 0, len(entries))
		for _, e := range entries {
			if e.CorrelationID == m.corrID {
				kept = append(kept, e)
			}
		}
		entries = kept
	}
	if m.showAll {
		m.rows = BuildAll(entries)
	} else {
		m.rows = BuildFailures(entries)
	}

	if live {
		m.selected = len(m.rows) // ride the handle as the list grows
	} else {
		m.top = seqIndex(m.rows, m.topSeq)
		m.selected = seqIndex(m.rows, m.selSeq)
	}
	return m
}

// seqIndex returns the index of the row carrying seq, or — if that entry has
// since been evicted from the ring — the index of the oldest still-held row
// newer than it. Rows are chronological, so eviction only ever removes from
// the front: the result only ever creeps forward by exactly as much as was
// evicted, never jumps to the newest row outright.
func seqIndex(rows []Row, seq int) int {
	for i, r := range rows {
		if r.Entry.Seq >= seq {
			return i
		}
	}
	return len(rows)
}

// anchorRow is the row a keystroke acts on: the selected one, or the newest when
// the cursor is parked on the shade.
func (m Model) anchorRow() (Row, bool) {
	if len(m.rows) == 0 {
		return Row{}, false
	}
	i := m.selected
	if i >= len(m.rows) {
		i = len(m.rows) - 1
	}
	return m.rows[i], true
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
			m.detailW = m.detailWidth() // the cap moved with the terminal
			m.detail.Width, m.detail.Height = m.detailDims()
			m.detail.SetContent(m.wrappedDetail()) // re-wrap to the new pane width
		}
		if m.showHelp {
			m = m.openHelp() // re-wrap and re-size, keeping it on screen
		}
		return m, nil

	case entryMsg:
		m.ingested++
		e := entry.Entry(msg)
		e.Seq = m.ingested
		m.ring.Append(e)
		return m.rebuild(), waitForEntry(m.ch)

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
			return m, nil // empty space, or the detail pane
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

// toggleCorrelation pins the view to the selected line's request, or releases it
// if already pinned.
func (m Model) toggleCorrelation() Model {
	if m.pinned {
		m.pinned, m.corrID = false, ""
		return m.rebuild()
	}
	row, ok := m.anchorRow()
	if !ok {
		m.notice = "nothing to correlate yet"
		return m
	}
	// A line with no id pins to the uncorrelated set — the lines belonging to no
	// request — which is a useful view in its own right (startup, shutdown, and
	// anything logged outside a request).
	m.pinned, m.corrID = true, row.Entry.CorrelationID
	return m.rebuild()
}

// yank copies the inspected entry to the system clipboard, uncoloured — what is
// on screen, in a form that pastes cleanly into a ticket or an editor. It copies
// the whole entry unwrapped, not the folded view: the pane's width is a display choice,
// not a decision about what you meant to take.
func (m Model) yank() Model {
	text := plainDetail(m.detailEntry)
	if err := m.copy(text); err != nil {
		m.notice = "clipboard: " + err.Error()
		return m
	}
	m.notice = fmt.Sprintf("copied %s", plural(len(strings.Split(text, "\n")), "line"))
	return m
}

// plainDetail is renderDetail without the colour, for anywhere the text leaves
// the terminal.
func plainDetail(e entry.Entry) string {
	if e.JSON != nil {
		return RenderYAML(e.JSON, true)
	}
	return string(e.Raw)
}

func plural(n int, unit string) string {
	if n == 1 {
		return "1 " + unit
	}
	return fmt.Sprintf("%d %ss", n, unit)
}

// quit leaves, taking the producer's process group with it. Nothing to stop in
// pipe mode, or once the child has already exited.
func (m Model) quit() (tea.Model, tea.Cmd) {
	if m.child != nil && !m.childExited {
		return m, terminateThenQuit(m.child)
	}
	return m, tea.Quit
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
// not over a selectable row: empty space below the list, the status bar, or the
// columns the detail pane is covering.
//
// The list is always laid out at full width, whether or not the pane is open, so
// hit-testing reads the same layout the renderer drew.
func (m Model) rowAtPoint(x, y int) int {
	if m.showDetail {
		paneW, _ := m.detailDims()
		if x >= m.width-paneW-detailChrome {
			return -1 // the divider, the margin, or the pane itself
		}
	}
	lines := m.listLines(m.width)
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
		m.topSeq = m.rowSeq(m.top)
	}
	m.selected = i
	m.selSeq = m.rowSeq(i)
	if !m.onShade() {
		m = m.scrollToCursor()
	}
	return m
}

// rowSeq is the Entry.Seq anchoring row i, or a sentinel that never matches a
// real row's Seq (which starts at 1) when i is out of range — in particular
// when i is the shade position, len(m.rows).
func (m Model) rowSeq(i int) int {
	if i < 0 || i >= len(m.rows) {
		return 0
	}
	return m.rows[i].Entry.Seq
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
	}
	if last := m.top + m.windowBudget() - 1; m.selected > last {
		m.top = m.selected - m.windowBudget() + 1
	}
	if m.top < 0 {
		m.top = 0
	}
	m.topSeq = m.rowSeq(m.top)
	return m
}

var (
	helpHeadStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("39")).Bold(true)
	helpKeyStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("214"))
	helpNoteStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
)

// helpKeyWidth is the key column. Wide enough for "ctrl+d / ctrl+u", the longest
// binding — anything narrower and it collides with its own description.
const helpKeyWidth = 18

// helpContent is the full key reference. Everything lives here, including the
// keys the status bar advertises — the bar is a reminder of the common ones, not
// the documentation.
//
// Entries are key-and-phrase, not sentences. A reference is scanned, not read;
// prose only earns its place where the behaviour would otherwise be a surprise,
// and then it is indented so it reads as a footnote rather than a binding.
func (m Model) helpContent() string {
	var b strings.Builder
	head := func(s string) { b.WriteString("\n" + helpHeadStyle.Render(" "+s) + "\n") }
	row := func(k, desc string) {
		b.WriteString("  " + helpKeyStyle.Render(fmt.Sprintf("%-*s", helpKeyWidth, k)) + desc + "\n")
	}
	note := func(s string) { b.WriteString("      " + helpNoteStyle.Render(s) + "\n") }

	b.WriteString(helpHeadStyle.Render(" loghorn — keys") + "\n")

	head("Moving")
	row("j / k", "line")
	row("ctrl+d / ctrl+u", "half page")
	row("pgdn / pgup", "page · fn+↓ fn+↑ on a Mac laptop")
	row("g / G", "oldest row / newest")
	row("space", "hold / release")
	row("click · wheel", "select a row · scroll")
	note("the status bar is the shade's handle: on it live, off it held")
	note("click the bar to go live")

	head("Filtering")
	row("a", "all lines / failures only")
	row("c", "pin this line's request · again releases")
	note("a line with no id pins everything outside a request")
	note("with a: the failures inside that request")

	head("Inspecting")
	row("enter", "open the pane · enter or esc closes")
	row("j / k · space", "scroll · page")
	row("y", "copy the entry to the clipboard")

	head("Producer")
	if m.child != nil {
		row("f", "send keys to the child until esc")
		row("Q", "quit, leave the child running")
		note("q also stops the child's process group")
	} else {
		note("reading a pipe · start as loghorn -- <command> for f and Q")
	}

	head("Other")
	row("m", "mouse capture · off frees text selection")
	row("?", "this help")
	row("q", "quit")
	return b.String()
}

// openHelp sizes and fills the help overlay.
func (m Model) openHelp() Model {
	m.help.Width, m.help.Height = m.width, m.helpHeight()
	m.help.SetContent(lipgloss.NewStyle().Width(m.width).Render(m.helpContent()))
	m.help.GotoTop()
	m.showHelp = true
	return m
}

func (m Model) helpHeight() int {
	if h := m.height - 1; h > 0 { // reserve the status bar
		return h
	}
	return 1
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
	m.detailW = m.detailWidth() // sized to this entry, not the last one
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

	// The help overlay owns the keyboard: scroll keys scroll it, anything that
	// looks like "done" closes it. ctrl+c still quits, as it does everywhere.
	if m.showHelp {
		switch msg.String() {
		case "ctrl+c":
			return m.quit()
		case "?", "esc", "enter", "q", " ":
			m.showHelp = false
			return m, nil
		}
		var cmd tea.Cmd
		m.help, cmd = m.help.Update(msg)
		return m, cmd
	}

	// A notice lasts until the next keystroke, whatever that is.
	m.notice = ""

	// Quit, help and the mouse toggle work from anywhere, including the detail
	// pane.
	switch msg.String() {
	case "?":
		return m.openHelp(), nil
	case "a":
		m.showAll = !m.showAll
		return m.rebuild(), nil
	case "c":
		return m.toggleCorrelation(), nil
	case "q", "ctrl+c":
		return m.quit()
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

	// When the detail pane is open it owns the keyboard: enter and esc both close
	// it — enter opened it, so closing with the same key saves crossing the
	// keyboard — and every other key scrolls the viewport (j/k, arrows, pages).
	if m.showDetail {
		switch msg.String() {
		case "esc", "enter":
			m.showDetail = false
			return m, nil
		case "y":
			return m.yank(), nil
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
	case "pgdown":
		m = m.moveSelection(m.windowBudget())
	case "pgup":
		m = m.moveSelection(-m.windowBudget())
	case "ctrl+d":
		m = m.moveSelection(m.halfPage())
	case "ctrl+u":
		m = m.moveSelection(-m.halfPage())
	case "g":
		m = m.setSelected(0)
	case "G":
		m = m.grabShade()
	}
	return m, nil
}

// detailReserve is how many columns are kept back from the detail pane when it
// is open. The pane is only as wide as its content needs, but never so wide that
// what is left — the list plus the pane's own chrome — becomes unreadable.
const detailReserve = 20

// The pane's left edge: a vertical rule, then one column of margin before the
// content. Both come out of detailReserve, so the list keeps the rest.
const (
	detailDivider = "│"
	detailChrome  = 2 // the divider column plus the margin column
)

var dividerStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))

// detailWidth is the width the pane wants: its longest line, so content that
// fits is shown unwrapped, capped so the list keeps detailReserve columns.
// Measured on the unwrapped render, and ANSI-aware — the content is coloured.
func (m Model) detailWidth() int {
	max := m.width - detailReserve
	if max < 1 {
		max = 1 // absurdly narrow terminal; degrade rather than go negative
	}
	w := maxLineWidth(renderDetail(m.detailEntry))
	if w > max {
		w = max
	}
	if w < 1 {
		w = 1
	}
	return w
}

// maxLineWidth is the display width of the widest line, ignoring ANSI styling.
func maxLineWidth(s string) int {
	max := 0
	for _, ln := range strings.Split(s, "\n") {
		if w := lipgloss.Width(ln); w > max {
			max = w
		}
	}
	return max
}

// detailDims returns the width and height of the detail pane, matching the split
// layout used by View. The width is cached on open and on resize rather than
// recomputed per frame, since measuring means rendering the entry.
func (m Model) detailDims() (w, h int) {
	w = m.detailW
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
		return RenderYAML(e.JSON, false)
	}
	return string(e.Raw)
}

// wrappedDetail renders the inspected entry and folds any line wider than the
// pane onto continuation lines, so nothing past the right edge is hidden.
//
// The pane sizes itself to its content first (detailWidth), so a line only folds
// once the entry is wider than the terminal less detailReserve. Callers redo the
// fold whenever that cap moves — on open and on every resize.
func (m Model) wrappedDetail() string {
	w, _ := m.detailDims()
	lines := strings.Split(renderDetail(m.detailEntry), "\n")
	for i, ln := range lines {
		lines[i] = wrapLine(ln, w)
	}
	return strings.Join(lines, "\n")
}

// wrapLine folds one rendered line to w columns, breaking at spaces where it can
// and mid-word where it must. Continuations keep the line's own indentation, so
// a long value doesn't spill back to the left edge and break up the YAML's
// nesting — unless that indent is more than half the pane, where it would leave
// too little room to read.
//
// cellbuf.Wrap rather than ansi.Wrap: the content is coloured, and cellbuf
// re-opens the active style on each continuation line. Without that, a folded
// value loses its colour after the first line, because the overlay composites
// the pane line by line.
func wrapLine(ln string, w int) string {
	if lipgloss.Width(ln) <= w {
		return ln
	}
	body := strings.TrimLeft(ln, " ")
	pad := len(ln) - len(body)
	if pad > w/2 {
		body, pad = ln, 0
	}
	indent := strings.Repeat(" ", pad)
	parts := strings.Split(cellbuf.Wrap(body, w-pad, ""), "\n")
	for i := range parts {
		parts[i] = indent + parts[i]
	}
	return strings.Join(parts, "\n")
}

func (m Model) View() string {
	if m.width == 0 {
		return "starting loghorn…"
	}
	if m.showHelp {
		return m.help.View() + "\n" + m.statusBar()
	}
	// The list is always laid out at the full terminal width. Opening the detail
	// pane draws over the right-hand columns rather than re-flowing the list into
	// a narrower one, so the text you were reading stays exactly where it was.
	body := m.listView(m.width)
	if m.showDetail {
		body = m.overlayDetail(body)
	}
	return body + "\n" + m.statusBar()
}

// overlayDetail composites the detail pane on top of the list. Each list line is
// clipped where the pane starts — never re-wrapped — so the visible left-hand
// text is identical to what was on screen before the pane opened.
func (m Model) overlayDetail(base string) string {
	paneW, paneH := m.detailDims()
	leftW := m.width - paneW - detailChrome
	if leftW < 0 {
		leftW = 0
	}

	baseLines := strings.Split(base, "\n")
	paneLines := strings.Split(m.detail.View(), "\n")

	out := make([]string, paneH)
	for i := range out {
		var left, right string
		if i < len(baseLines) {
			left = baseLines[i]
		}
		if i < len(paneLines) {
			right = paneLines[i]
		}
		// clipTo unconditionally, including past the end of the list: rows below
		// it still need the left column padded out, or the pane's edge goes
		// ragged where the list runs out. The rule runs the pane's full height
		// for the same reason.
		out[i] = clipTo(left, leftW) + dividerStyle.Render(detailDivider) + " " + right
	}
	return strings.Join(out, "\n")
}

// clipTo cuts a rendered line to w display columns and pads it back out to
// exactly w, so the pane always starts in the same column. Truncating and
// padding are separate Render calls because lipgloss wraps for Width before it
// truncates for MaxWidth — combined, an over-long line would fold instead of
// being cut.
func clipTo(line string, w int) string {
	if w < 1 {
		return ""
	}
	clipped := lipgloss.NewStyle().MaxWidth(w).Render(line)
	return lipgloss.NewStyle().Width(w).Render(clipped)
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
	// One row per line. Overshooting the budget makes Bubble Tea drop the top of
	// the frame (standard_renderer keeps only the last height lines), which reads
	// as the list being cleared.
	budget := m.windowBudget()
	if m.onShade() {
		// Live: the window rides the newest row.
		start = len(m.rows) - budget
		if start < 0 {
			start = 0
		}
		return start, len(m.rows)
	}
	// Held: pinned to the frozen anchor, so arriving rows pile up below the
	// window instead of scrolling the view out from under you.
	start = m.top
	if start > len(m.rows)-1 {
		start = len(m.rows) - 1
	}
	if start < 0 {
		start = 0
	}
	end = start + budget
	if end > len(m.rows) {
		end = len(m.rows)
	}
	return start, end
}

// windowBudget is how many rows fit above the status bar, and so how far a page
// key moves.
func (m Model) windowBudget() int {
	if b := m.height - 1; b > 0 {
		return b
	}
	return 1
}

// halfPage is the ctrl+d/ctrl+u step. Half a screen keeps a few lines of overlap
// either side of the jump, which matters more in a log than in a document — you
// keep your place instead of landing somewhere unrecognisable.
func (m Model) halfPage() int {
	if h := m.windowBudget() / 2; h > 0 {
		return h
	}
	return 1
}

// listLine is one rendered line of the list paired with the display row it
// shows.
type listLine struct {
	text string
	row  int
}

// listLines lays the visible list out exactly as it is drawn. Rendering and
// mouse hit-testing both read it, so a click can never land on a different row
// than the one under the cursor.
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
			fmt.Sprintf("loghorn %s · FORWARDING — every key goes to %s · esc to stop",
				m.spinner(), name)))
	}

	if m.showHelp {
		return "  " + statusStyle.Render(fmt.Sprintf(
			"loghorn %s · help · j/k scroll · esc close", m.spinner()))
	}

	if m.showDetail {
		pos := "all"
		if !(m.detail.AtTop() && m.detail.AtBottom()) {
			pos = fmt.Sprintf("%d%%", int(m.detail.ScrollPercent()*100))
		}
		// The spinner rides along here too, so ingest stays visible while you
		// are reading an entry.
		bar := "  " + statusStyle.Render(fmt.Sprintf(
			"loghorn %s · detail %s · j/k scroll · y yank · enter/esc close · ? help",
			m.spinner(), pos))
		if m.notice != "" {
			bar += moreStyle.Render(" · " + m.notice)
		}
		return bar
	}

	live := m.onShade()
	mode := "HELD"
	if live {
		mode = "LIVE"
	}
	base := statusStyle.Render(fmt.Sprintf(
		"loghorn %s · %s · %s lines · %d shown", m.spinner(), mode, comma(m.ingested), len(m.rows)))

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

	// Active filters are named; the default (failures only, unpinned) says
	// nothing, so the bar stays quiet until something is actually narrowing or
	// widening what you see.
	if m.showAll {
		more += moreStyle.Render(" · ALL")
	}
	if m.pinned {
		if m.corrID == "" {
			more += moreStyle.Render(" · uncorrelated")
		} else {
			more += moreStyle.Render(" · id:" + shortID(m.corrID))
		}
	}

	// Mouse capture is state, not a hint, and only worth saying when it is off —
	// that is the surprising case, and the confirmation you want after pressing
	// 'm' to select text.
	if !m.mouse {
		more += moreStyle.Render(" · mouse:off")
	}

	if m.notice != "" {
		more += moreStyle.Render(" · " + m.notice)
	}

	// Only the keys reached for constantly earn a place here; the rest ('m',
	// 'f', 'Q', 'g'/'G') live behind '?', which is advertised so they stay
	// discoverable. State is rendered before hints, so a narrow window loses
	// hints rather than state.
	tail := statusStyle.Render(fmt.Sprintf(
		" · j/k · spc %s · enter open · ? help · q quit", toggleWord(live)))

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

// shortID trims a correlation id to a recognisable prefix — trace ids are long
// enough to swallow the bar whole.
func shortID(s string) string {
	const n = 8
	if len([]rune(s)) <= n {
		return s
	}
	return string([]rune(s)[:n]) + "…"
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
