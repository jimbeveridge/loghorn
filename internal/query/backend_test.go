package query

import (
	"context"
	"testing"
	"time"

	"github.com/jimbeveridge/loghorn/internal/config"
	"github.com/jimbeveridge/loghorn/internal/duckstore"
	"github.com/jimbeveridge/loghorn/internal/record"
	"github.com/jimbeveridge/loghorn/internal/schema"
)

// backend is an in-memory DuckDB engine holding the test's records, for
// RunSQL.
type backend struct{ *duckstore.Engine }

func newBackend(t *testing.T, recs []record.Record) backend {
	t.Helper()
	b := openBackend(t, recs)
	t.Cleanup(func() { b.Close() })
	return b
}

// openBackend is newBackend for a caller that closes the engine itself.
// Each engine runs its own DuckDB threads, so a test that runs many
// queries must not keep every engine open until it ends.
func openBackend(t *testing.T, recs []record.Record) backend {
	t.Helper()
	e, err := duckstore.OpenMemory(config.Input{}, func(s string) { t.Log(s) })
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range recs {
		e.Add(r)
	}
	if !e.Sync(30 * time.Second) {
		e.Close()
		t.Fatal("sync timed out")
	}
	return backend{e}
}

// schema is the schema DuckDB infers for one stream of a service.
func (b backend) schema(t *testing.T, svc, src, stream string) schema.Schema {
	t.Helper()
	tl, err := b.Tallies(src, svc, stream)
	if err != nil {
		t.Fatal(err)
	}
	return schema.FromTallies(src, stream, tl)
}

// keptBackend is a backend that shows each record with the fields it was
// added with. The behaviour tests build records by hand, so their raw text
// is not a line the adapter would read back into the same fields; SQL
// still decides which records match, their order, groups and metrics, and
// the engine still supplies each row's id, time and service.
type keptBackend struct {
	backend
	recs map[int]record.Record
}

func (b keptBackend) Rebuild(id int64, t time.Time, inherited bool, service, source, stream, raw string) record.Record {
	r := b.backend.Rebuild(id, t, inherited, service, source, stream, raw)
	if o, ok := b.recs[int(id)]; ok {
		r.Fields, r.Raw = o.Fields, o.Raw
	}
	return r
}

// openKeptBackend stores recs as the behaviour tests mean them: every
// record in the default service when it names none. The caller closes it.
func openKeptBackend(t *testing.T, recs []record.Record) keptBackend {
	t.Helper()
	kept := map[int]record.Record{}
	stored := make([]record.Record, len(recs))
	for i, r := range recs {
		if r.Service == "" {
			r.Service = record.DefaultService
		}
		stored[i] = r
		kept[r.Seq] = r
	}
	return keptBackend{openBackend(t, stored), kept}
}

// runSQL answers q over recs on a fresh engine, closed before it returns.
func runSQL(t *testing.T, recs []record.Record, s schema.Schema, q Query, now, runStart time.Time) (Result, error) {
	t.Helper()
	b := openKeptBackend(t, recs)
	defer b.Close()
	return RunSQL(context.Background(), b, s, q, now, runStart)
}

// inferred is the schema DuckDB infers for one stream of recs, over every
// service.
func inferred(t *testing.T, recs []record.Record, src, stream string) schema.Schema {
	t.Helper()
	b := openKeptBackend(t, recs)
	defer b.Close()
	return b.schema(t, "*", src, stream)
}
