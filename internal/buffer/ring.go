// Package buffer provides a bounded ring buffer of log entries.
package buffer

import "loghorn/internal/entry"

type Ring struct {
	data  []entry.Entry
	start int // index of oldest element
	count int
}

func New(capacity int) *Ring {
	if capacity < 1 {
		capacity = 1
	}
	return &Ring{data: make([]entry.Entry, capacity)}
}

func (r *Ring) Append(e entry.Entry) {
	if r.count < len(r.data) {
		r.data[(r.start+r.count)%len(r.data)] = e
		r.count++
		return
	}
	// Full: overwrite oldest and advance start.
	r.data[r.start] = e
	r.start = (r.start + 1) % len(r.data)
}

func (r *Ring) Len() int { return r.count }

// Snapshot returns a copy of the entries, oldest to newest.
func (r *Ring) Snapshot() []entry.Entry {
	out := make([]entry.Entry, r.count)
	for i := 0; i < r.count; i++ {
		out[i] = r.data[(r.start+i)%len(r.data)]
	}
	return out
}
