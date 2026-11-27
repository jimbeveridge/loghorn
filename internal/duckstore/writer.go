package duckstore

import (
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	duckdb "github.com/duckdb/duckdb-go/v2"

	"github.com/jimbeveridge/loghorn/internal/record"
	"github.com/jimbeveridge/loghorn/internal/schema"
)

// write stores one batch, logs and spans alike, with its tallies in one
// transaction. When the batch fails, memory tells the cause apart: if memory
// takes it, the file is at fault and the rest of the session goes to memory;
// if memory refuses it too, a record's data is at fault, so the batch is
// stored row by row and only the refused records are skipped (they stay in
// the text log).
func (e *Engine) write(batch []record.Record) {
	defer e.archiveAtMidnight(batch)
	p := e.dest
	if err := e.writeTx(p, batch); err != nil {
		if p.file {
			if e.writeTx(e.mem, batch) == nil {
				e.fileFailed(err)
				err = nil
			}
		}
		if err != nil {
			e.writeRows(p, batch, err)
		}
	}
	e.ownerOnly()
	e.version.Add(1)
}

// writeRows stores batch one record at a time, skipping the records p
// refuses. batchErr is why the whole batch failed: when the first record
// alone fails with that same error, the fault isn't in the data (DuckDB
// itself is failing), so the rest isn't tried. Those records are counted as
// skipped, leaving the files to be imported again from text; a record
// refused for its own data is not, or every start would import it again.
// Each distinct error is warned about once.
func (e *Engine) writeRows(p *part, batch []record.Record, batchErr error) {
	skipped := 0
	var first error
	for i, r := range batch {
		err := e.writeTx(p, []record.Record{r})
		if err == nil {
			continue
		}
		if i == 0 && err.Error() == batchErr.Error() {
			e.mu.Lock()
			e.skipped += len(batch)
			e.mu.Unlock()
			e.warnKeyed("skip:"+firstLine(err), fmt.Sprintf("loghorn: DuckDB skipped %d record(s): it could not store them (%v); the text log keeps them", len(batch), firstLine(err)))
			return
		}
		skipped++
		if first == nil {
			first = err
		}
	}
	if skipped > 0 {
		e.warnKeyed("skip:"+firstLine(first), fmt.Sprintf("loghorn: DuckDB skipped %d record(s) it could not store (%v); the text log keeps them", skipped, firstLine(first)))
	}
}

// writeTx appends recs to p and adds their tallies, all or nothing.
func (e *Engine) writeTx(p *part, recs []record.Record) error {
	if p.file && e.failWrites {
		return errors.New("write refused (test)")
	}
	if e.rejectRow != nil {
		for _, r := range recs {
			if e.rejectRow(r) {
				return errors.New("row refused (test)")
			}
		}
	}
	if err := e.exec("BEGIN TRANSACTION"); err != nil {
		return err
	}
	err := e.appendRows(p, recs)
	if err == nil {
		err = e.saveTallies(p, recs)
	}
	if err == nil {
		if err = e.exec("COMMIT"); err == nil {
			return nil
		}
	}
	e.exec("ROLLBACK") // fails harmlessly when DuckDB already aborted it
	return err
}

// fileFailed handles a failed file write: warn once, and send this and
// every later record to memory. The records stay in the text log.
func (e *Engine) fileFailed(err error) {
	if !e.failed {
		e.failed = true
		e.warn(fmt.Sprintf("loghorn: DuckDB write failed (%v); keeping this run's later records in memory (the text log keeps them)", firstLine(err)))
	}
	e.dest = e.mem
}

func (e *Engine) warnOnce(msg string) {
	key := firstLine(fmt.Errorf("%s", msg))
	if len(key) > 60 {
		key = key[:60]
	}
	e.warnKeyed(key, msg)
}

// warnKeyed warns msg unless a warning with key was already given.
func (e *Engine) warnKeyed(key, msg string) {
	if e.warned[key] {
		return
	}
	e.warned[key] = true
	e.warn(msg)
}

func firstLine(err error) string {
	s := err.Error()
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return s
}

// rebuild turns a stored _raw back into its record's fields, the same way
// the record was first built.
func (e *Engine) rebuild(source string, raw []byte) (record.Record, bool) {
	if source == record.SourceOtel {
		return record.NormalizeSpan(raw)
	}
	return record.Normalize(e.parser.ParseLine(raw), e.in)
}

func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// minTime and maxTime bound what _time (TIMESTAMP_NS) can hold. A time
// outside them is stored as NULL, as a missing time is; _raw keeps it.
var (
	minTime = time.Unix(0, math.MinInt64+1).UTC()
	maxTime = time.Unix(0, math.MaxInt64).UTC()
)

// TimeFits reports whether t is a real time _time can hold. A record whose
// time doesn't fit is stored with a NULL _time.
func TimeFits(t time.Time) bool {
	return !t.IsZero() && !t.Before(minTime) && !t.After(maxTime)
}

// row is a record's values in column order.
func row(r record.Record) []driver.Value {
	var tm any
	if TimeFits(r.Time) {
		tm = r.Time.UTC()
	}
	src := r.Key().Source
	tid, sid := record.TraceRef(r.Fields)
	sev, _ := r.Fields["severity"].(string)
	var parent, msg, dur any
	if src == record.SourceOtel {
		p, _ := r.Fields["parent_span_id"].(string)
		parent = nullStr(record.NormalizeSpanID(p))
		n, _ := r.Fields["name"].(string)
		msg = nullStr(n)
		if d, ok := r.Fields["duration_ms"].(json.Number); ok {
			if f, err := d.Float64(); err == nil {
				dur = f
			}
		}
	} else {
		m, _ := r.Fields["message"].(string)
		msg = nullStr(m)
	}
	raw := r.Raw
	if raw == nil {
		raw = []byte{}
	}
	return []driver.Value{int64(r.Seq), tm, r.TimeInherited, src, r.Service, r.Stream,
		nullStr(tid), nullStr(sid), parent, nullStr(sev), msg, dur, json.RawMessage(attributes(r.Fields)), raw}
}

func (e *Engine) appendRows(p *part, recs []record.Record) error {
	return e.wc.Raw(func(dc any) error {
		a, err := duckdb.NewAppender(dc.(driver.Conn), p.alias, "main", recordsTable)
		if err != nil {
			return err
		}
		for _, r := range recs {
			if err := a.AppendRow(row(r)...); err != nil {
				a.Close()
				return fmt.Errorf("append to %s: %w", p.ref(), err)
			}
		}
		return a.Close()
	})
}

type tallyKey struct {
	day                         int
	service, source, kind, path string
}

// saveTallies adds recs' field paths to p's catalog's tallies.
func (e *Engine) saveTallies(p *part, recs []record.Record) error {
	tl := map[tallyKey]*schema.Tally{}
	for _, r := range recs {
		for path, v := range r.Fields {
			k := tallyKey{dayOf(r.Seq), r.Service, r.Key().Source, r.Stream, path}
			t := tl[k]
			if t == nil {
				t = &schema.Tally{}
				tl[k] = t
			}
			t.Count++
			t.Kinds |= schema.Classify(v)
		}
	}
	keys := make([]tallyKey, 0, len(tl))
	for k := range tl {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i].path < keys[j].path })
	for len(keys) > 0 {
		n := min(len(keys), 500)
		var sb strings.Builder
		args := make([]any, 0, n*7)
		sb.WriteString("INSERT INTO " + p.fieldsRef() + " VALUES ")
		for i, k := range keys[:n] {
			if i > 0 {
				sb.WriteString(", ")
			}
			sb.WriteString("(?, ?, ?, ?, ?, ?, ?)")
			t := tl[k]
			args = append(args, k.day, k.service, k.source, k.kind, k.path, int64(t.Count), int32(t.Kinds))
		}
		sb.WriteString(" ON CONFLICT DO UPDATE SET n = n + excluded.n, kinds = kinds | excluded.kinds")
		if err := e.exec(sb.String(), args...); err != nil {
			return err
		}
		keys = keys[n:]
	}
	return nil
}

// applyFill sets the time of records written without one.
func (e *Engine) applyFill(f *fill) {
	if len(f.recs) == 0 {
		return
	}
	ids := make([]string, len(f.recs))
	for i, r := range f.recs {
		ids[i] = strconv.Itoa(r.Seq)
	}
	in := strings.Join(ids, ",")
	ns := f.t.UnixNano()
	for _, p := range e.parts {
		if !p.writable {
			continue
		}
		if err := e.exec("UPDATE "+p.ref()+" SET _time = make_timestamp_ns(CAST(? AS BIGINT)) WHERE _id IN ("+in+")", ns); err != nil {
			e.warnOnce("loghorn: DuckDB: " + err.Error())
		}
	}
	e.version.Add(1)
}

func (e *Engine) exec(q string, args ...any) error {
	_, err := e.wc.ExecContext(bg, q, args...)
	if err != nil {
		return fmt.Errorf("%w\n%s", err, q)
	}
	return nil
}
