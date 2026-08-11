package tui

import "github.com/jimbeveridge/loghorn/internal/entry"

type RowKind int

const (
	RowImportant RowKind = iota
	RowContext
)

type Row struct {
	Entry entry.Entry
	Kind  RowKind
}

// BuildFailures shows only the entries the engine marked important. There is no
// leading-context window: 'a' shows the whole stream when you want the lines
// around a failure, which is both simpler and unbounded.
//
// Nothing marks where lines were skipped, either. Without a context window
// almost every pair of failures has something hidden between them, so a marker
// would appear before nearly every row and cost half the screen to say something
// the mode already says.
func BuildFailures(entries []entry.Entry) []Row {
	rows := make([]Row, 0, len(entries))
	for _, e := range entries {
		if e.Important {
			rows = append(rows, Row{Entry: e, Kind: RowImportant})
		}
	}
	return rows
}

// BuildAll shows every entry, keeping the importance styling so failures still
// stand out among them.
func BuildAll(entries []entry.Entry) []Row {
	rows := make([]Row, 0, len(entries))
	for _, e := range entries {
		kind := RowContext
		if e.Important {
			kind = RowImportant
		}
		rows = append(rows, Row{Entry: e, Kind: kind})
	}
	return rows
}
