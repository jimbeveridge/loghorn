package tui

import "clog/internal/entry"

type RowKind int

const (
	RowImportant RowKind = iota
	RowContext
)

type Row struct {
	Entry     entry.Entry
	Kind      RowKind
	GapBefore bool
}

// BuildDisplay hides routine lines, showing each important entry preceded by up
// to contextN routine lines. Overlapping context windows never duplicate a line,
// and a row that is not contiguous in the source stream with the previously
// emitted row gets GapBefore=true.
func BuildDisplay(entries []entry.Entry, contextN int) []Row {
	if contextN < 0 {
		contextN = 0
	}
	// Decide which source indices are visible and with what kind.
	kind := make(map[int]RowKind)
	var order []int // source indices, ascending, each appearing once
	seen := make(map[int]bool)

	add := func(i int, k RowKind) {
		if seen[i] {
			// Important always wins over context if both apply.
			if k == RowImportant {
				kind[i] = RowImportant
			}
			return
		}
		seen[i] = true
		kind[i] = k
		order = append(order, i)
	}

	for i, e := range entries {
		if !e.Important {
			continue
		}
		start := i - contextN
		if start < 0 {
			start = 0
		}
		for j := start; j < i; j++ {
			add(j, RowContext)
		}
		add(i, RowImportant)
	}

	rows := make([]Row, 0, len(order))
	prev := -2
	for _, i := range order {
		rows = append(rows, Row{
			Entry:     entries[i],
			Kind:      kind[i],
			GapBefore: i != prev+1,
		})
		prev = i
	}
	return rows
}
