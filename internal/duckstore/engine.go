// Package duckstore keeps loghorn's records in DuckDB: one fixed table,
// records, for logs and spans alike, with the common fields as columns,
// every normalized field in a JSON attributes column, and the record as
// captured in _raw.
// The views all_records and all_fields in the session's own in-memory
// catalog cover every table and archive; the web view reads only those.
package duckstore

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	duckdb "github.com/duckdb/duckdb-go/v2"

	"github.com/jimbeveridge/loghorn/internal/adapter"
	"github.com/jimbeveridge/loghorn/internal/config"
	"github.com/jimbeveridge/loghorn/internal/record"
)

const (
	// MaxQueue bounds the records waiting to be written; past it, live
	// records are skipped for DuckDB (their text log keeps them).
	MaxQueue = 200_000
	// maxBatch caps the records written in one batch.
	maxBatch = 20_000
	// memoryLimit caps DuckDB's buffer pool (its default is 80% of RAM);
	// past it DuckDB spills to temporary files.
	memoryLimit = "1GB"
)

// item is one queued write: a record, or a time to give records already
// queued or written.
type item struct {
	r    record.Record
	fill *fill
}

type fill struct {
	recs []record.Record
	t    time.Time
}

// Engine is one session's DuckDB instance and its writer.
type Engine struct {
	connector *duckdb.Connector
	db        *sql.DB
	wc        *sql.Conn // the writer's connection
	in        config.Input
	parser    *adapter.Parser // for in; stateless, so shared by rebuilds
	warn      func(string)

	mu        sync.Mutex
	cond      *sync.Cond
	queue     []item
	added     uint64 // records accepted by Add (queued or skipped)
	committed uint64 // records written or given up on
	closing   bool
	wdone     chan struct{}
	maxQueue  int  // MaxQueue, unless SetMaxQueue changed it
	overflow  bool // the queue overflowed (warned once)
	skipped   int  // records skipped for DuckDB this run

	// Writer state: touched with wmu held (the writer goroutine, Archive,
	// MarkComplete, Failed), or before the writer starts and after it stops.
	wmu      sync.Mutex
	parts    []*part      // every records table: memory, then the file's
	dest     *part        // the table new records go to
	mem      *part        // the in-memory table
	files    *FileOptions // nil: memory only
	failed   bool         // a file write failed: the rest goes to memory
	complete bool         // the files hold every record from text (MarkComplete)
	// failWrites and failArchive make file writes and archive checks fail
	// (tests).
	failWrites, failArchive bool
	rejectRow               func(record.Record) bool // tests: rows to refuse as bad data
	tmpDir                  string                   // an in-memory engine's spill folder, removed at Close
	failHotDays             bool                     // tests: the hot days can't be read
	warned                  map[string]bool          // warnings already given

	// modelMu guards the table model's views: WithModel replaces them
	// holding it, and queries over them hold it for reading. model is the
	// DDL last applied.
	modelMu sync.RWMutex
	model   string
	arch    archiveState

	// schemaMu keeps queries off the views while the writer changes what
	// they read (archiving a day).
	schemaMu sync.RWMutex
	version  atomic.Uint64
}

// OpenMemory opens an engine holding everything in memory, for replays and
// tests. warn reports problems (once each) to the user.
func OpenMemory(in config.Input, warn func(string)) (*Engine, error) {
	e, err := open(in, warn)
	if err != nil {
		return nil, err
	}
	if err := e.rebuildViews(); err != nil {
		e.closeDB()
		return nil, err
	}
	if err := e.harden(); err != nil {
		e.closeDB()
		return nil, err
	}
	e.start()
	return e, nil
}

// harden shuts the database off from everything outside its own files once
// they are open: no file or network access outside the data folder (where
// the archives are written and read, and DuckDB spills), and no extension
// installed or loaded on demand. SQL from the command line then cannot reach
// the machine even through a query the guard did not foresee.
func (e *Engine) harden() error {
	stmts := []string{"SET autoinstall_known_extensions = false", "SET autoload_known_extensions = false"}
	var dir, tmp string
	if e.files != nil {
		dir = filepath.Clean(e.files.Dir) + string(filepath.Separator)
		tmp = filepath.Join(dir, ".duckdb-tmp")
	} else {
		// In memory there is no data folder: spill to a folder of this
		// engine's own, removed at Close, not to .tmp in the working
		// directory.
		t, err := os.MkdirTemp("", "loghorn-duckdb-")
		if err != nil {
			return fmt.Errorf("duckdb: temp folder: %w", err)
		}
		e.tmpDir = t
		dir, tmp = t+string(filepath.Separator), t
	}
	stmts = append(stmts,
		"SET allowed_directories = ["+lit(dir)+"]",
		"SET temp_directory = "+lit(tmp))
	// Last: once the configuration is locked, no SQL can undo the above.
	// Nothing may SET anything after this (open's memory_limit runs first).
	stmts = append(stmts, "SET enable_external_access = false", "SET lock_configuration = true")
	for _, st := range stmts {
		if err := e.exec(st); err != nil {
			return fmt.Errorf("duckdb: %s: %w", st, err)
		}
	}
	return nil
}

// open starts DuckDB with the in-memory tables, which also take a file
// write that failed.
func open(in config.Input, warn func(string)) (*Engine, error) {
	c, err := duckdb.NewConnector("", nil)
	if err != nil {
		return nil, fmt.Errorf("duckdb: %w", err)
	}
	db := sql.OpenDB(c)
	wc, err := db.Conn(context.Background())
	if err != nil {
		db.Close()
		c.Close()
		return nil, fmt.Errorf("duckdb: %w", err)
	}
	if warn == nil {
		warn = func(string) {}
	}
	e := &Engine{connector: c, db: db, wc: wc, in: in, parser: adapter.New(in), warn: warn,
		warned: map[string]bool{}}
	e.cond = sync.NewCond(&e.mu)
	e.maxQueue = MaxQueue
	if err := e.exec("SET memory_limit = " + lit(memoryLimit)); err != nil {
		e.closeDB()
		return nil, err
	}
	p := &part{alias: "memory", writable: true}
	for _, ddl := range []string{tableDDL(p.ref()), fieldsDDL(p.fieldsRef())} {
		if err := e.exec(ddl); err != nil {
			e.closeDB()
			return nil, err
		}
	}
	e.mem, e.dest, e.parts = p, p, []*part{p}
	return e, nil
}

func (e *Engine) start() {
	e.wdone = make(chan struct{})
	go e.writer()
}

// ReadLock keeps the views' tables in place while a query runs; call the
// returned func when done.
func (e *Engine) ReadLock() (unlock func()) {
	e.schemaMu.RLock()
	return e.schemaMu.RUnlock
}

// DB is the session's database, for read queries.
func (e *Engine) DB() *sql.DB { return e.db }

// QueryContext runs a read query.
func (e *Engine) QueryContext(ctx context.Context, q string, args ...any) (*sql.Rows, error) {
	return e.db.QueryContext(ctx, q, args...)
}

// Version changes whenever written data changes.
func (e *Engine) Version() uint64 { return e.version.Load() }

// Add queues r for writing; r.Seq is its _id. It never blocks on DuckDB:
// past the queue bound the record is skipped for DuckDB (live input, whose
// text log keeps it) and counted (see closeFiles).
func (e *Engine) Add(r record.Record) { e.add(r, false) }

// AddWait is Add for input with no text log behind it (replays, loading
// text): when the queue is full it waits for room instead of skipping.
func (e *Engine) AddWait(r record.Record) { e.add(r, true) }

func (e *Engine) add(r record.Record, wait bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for wait && len(e.queue) >= e.maxQueue && !e.closing {
		e.cond.Wait()
	}
	e.added++
	if e.closing {
		e.committed++
		return
	}
	if len(e.queue) >= e.maxQueue {
		e.committed++
		e.skipped++
		if !e.overflow {
			e.overflow = true
			e.warn("loghorn: DuckDB writer is behind; skipping records for DuckDB (the text log keeps them)")
		}
		return
	}
	e.queue = append(e.queue, item{r: r})
	e.cond.Broadcast()
}

// SetMaxQueue changes the queue bound (for tests and tuning).
func (e *Engine) SetMaxQueue(n int) {
	e.mu.Lock()
	e.maxQueue = n
	e.mu.Unlock()
}

// SetTime gives recs, added earlier with no time, the time t. It is applied
// in order with the records queued around it.
func (e *Engine) SetTime(recs []record.Record, t time.Time) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.added++
	if e.closing {
		e.committed++
		return
	}
	e.queue = append(e.queue, item{fill: &fill{recs: append([]record.Record(nil), recs...), t: t}})
	e.cond.Broadcast()
}

// Sync waits until every record added before the call is written, or the
// timeout passes; it reports whether they were.
func (e *Engine) Sync(timeout time.Duration) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	target := e.added
	if e.committed >= target {
		return true
	}
	expired := false
	t := time.AfterFunc(timeout, func() {
		e.mu.Lock()
		expired = true
		e.cond.Broadcast()
		e.mu.Unlock()
	})
	defer t.Stop()
	for e.committed < target && !expired {
		e.cond.Wait()
	}
	return e.committed >= target
}

func (e *Engine) writer() {
	defer close(e.wdone)
	for {
		e.mu.Lock()
		for len(e.queue) == 0 && !e.closing {
			e.cond.Wait()
		}
		if len(e.queue) == 0 && e.closing {
			e.mu.Unlock()
			return
		}
		n := min(len(e.queue), maxBatch)
		batch := e.queue[:n:n]
		e.queue = e.queue[n:]
		if len(e.queue) == 0 {
			e.queue = nil
		}
		e.cond.Broadcast() // room for AddWait
		e.mu.Unlock()

		e.wmu.Lock()
		var recs []record.Record
		for _, it := range batch {
			if it.fill == nil {
				recs = append(recs, it.r)
				continue
			}
			if len(recs) > 0 {
				e.write(recs)
				recs = nil
			}
			e.applyFill(it.fill)
		}
		if len(recs) > 0 {
			e.write(recs)
		}
		e.wmu.Unlock()

		e.mu.Lock()
		e.committed += uint64(n)
		e.cond.Broadcast()
		e.mu.Unlock()
	}
}

// Close writes what is queued, stops the writer and closes DuckDB, after
// checkpointing the database file.
func (e *Engine) Close() error {
	return e.close(e.closeFiles)
}

func (e *Engine) close(before func() error) error {
	e.mu.Lock()
	if e.closing {
		e.mu.Unlock()
		return nil
	}
	e.closing = true
	e.cond.Broadcast()
	e.mu.Unlock()
	if e.wdone != nil {
		<-e.wdone
	}
	var err error
	if before != nil {
		err = before()
	}
	if cerr := e.closeDB(); err == nil {
		err = cerr
	}
	return err
}

func (e *Engine) closeDB() error {
	e.wc.Close()
	err := e.db.Close()
	if cerr := e.connector.Close(); err == nil {
		err = cerr
	}
	if e.tmpDir != "" {
		os.RemoveAll(e.tmpDir)
	}
	return err
}
