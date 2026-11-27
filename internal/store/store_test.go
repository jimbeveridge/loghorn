package store

import (
	"context"
	"database/sql"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/jimbeveridge/loghorn/internal/config"
	"github.com/jimbeveridge/loghorn/internal/query"
	"github.com/jimbeveridge/loghorn/internal/record"
	"github.com/jimbeveridge/loghorn/internal/schema"
)

var t0 = time.Date(2026, 11, 17, 17, 0, 0, 0, time.UTC)

func rec(stream string, t time.Time) record.Record {
	return record.Record{Stream: stream, Time: t, Fields: map[string]any{}}
}

func TestAddAssignsSeq(t *testing.T) {
	s := New(ModeHistorical, time.Time{})
	for i := 0; i < 3; i++ {
		s.Add(rec("a", t0), time.Time{})
	}
	recs := s.Records()
	if len(recs) != 3 {
		t.Fatalf("len=%d", len(recs))
	}
	for i, r := range recs {
		if r.Seq != i+1 {
			t.Errorf("recs[%d].Seq=%d", i, r.Seq)
		}
	}
}

func TestInheritFromPrevious(t *testing.T) {
	s := New(ModeHistorical, time.Time{})
	s.Add(rec("a", t0), time.Time{})
	s.Add(rec("a", time.Time{}), time.Time{})
	recs := s.Records()
	if !recs[1].Time.Equal(t0) || !recs[1].TimeInherited || recs[0].TimeInherited {
		t.Fatalf("got %+v", recs)
	}
}

func TestInheritFromReceived(t *testing.T) {
	s := New(ModeLive, t0)
	rcv := t0.Add(5 * time.Second)
	s.Add(rec("a", time.Time{}), rcv)
	recs := s.Records()
	if !recs[0].Time.Equal(rcv) || !recs[0].TimeInherited {
		t.Fatalf("got %+v", recs[0])
	}
}

func TestBackfillLeading(t *testing.T) {
	s := New(ModeHistorical, time.Time{})
	s.Add(rec("a", time.Time{}), time.Time{})
	s.Add(rec("a", time.Time{}), time.Time{})
	if in := mustInfo(t, s); !in.Earliest.IsZero() || !in.Latest.IsZero() {
		t.Fatalf("bounds with no real time: %+v", in)
	}
	s.Add(rec("a", t0), time.Time{})
	recs := s.Records()
	for i, r := range recs {
		if !r.Time.Equal(t0) {
			t.Errorf("recs[%d].Time=%v", i, r.Time)
		}
	}
	if !recs[0].TimeInherited || !recs[1].TimeInherited || recs[2].TimeInherited {
		t.Fatalf("inherited flags wrong: %+v", recs)
	}
}

func TestStreamsSorted(t *testing.T) {
	s := New(ModeHistorical, time.Time{})
	for _, n := range []string{"db", "api", "db", "server"} {
		s.Add(rec(n, t0), time.Time{})
	}
	got := mustStreams(t, s)
	want := []StreamCount{{Name: "api", Source: "log", Count: 1}, {Name: "db", Source: "log", Count: 2}, {Name: "server", Source: "log", Count: 1}}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestInfoBounds(t *testing.T) {
	s := New(ModeLive, t0)
	s.Add(rec("a", t0.Add(2*time.Second)), time.Time{})
	s.Add(rec("a", time.Time{}), time.Time{})
	s.Add(rec("a", t0.Add(time.Second)), time.Time{})
	s.Add(rec("a", t0.Add(9*time.Second)), time.Time{})
	in := mustInfo(t, s)
	if in.Mode != ModeLive || !in.RunStart.Equal(t0) || in.Count != 4 ||
		!in.Earliest.Equal(t0.Add(time.Second)) || !in.Latest.Equal(t0.Add(9*time.Second)) {
		t.Fatalf("%+v", in)
	}
}

func TestPreloadReadsFixtures(t *testing.T) {
	s := New(ModeHistorical, time.Time{})
	if err := Preload(s, []string{"../../testdata/web/pino.log"}, config.Input{}); err != nil {
		t.Fatal(err)
	}
	// pino.log: 3 db, 2 api, 2 server records (one server line untimed); the
	// npm banner line has no stream label and is dropped.
	want := []StreamCount{{Name: "api", Source: "log", Count: 2}, {Name: "db", Source: "log", Count: 3}, {Name: "server", Source: "log", Count: 2}}
	if got := mustStreams(t, s); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("got %v want %v", got, want)
	}
	recs := s.Records()
	last := recs[len(recs)-1]
	if !last.TimeInherited || last.Time.IsZero() {
		t.Fatalf("untimed line should inherit previous time: %+v", last)
	}
}

func TestConcurrentAddAndRead(t *testing.T) {
	s := New(ModeLive, t0)
	done := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-done:
					return
				default:
				}
				s.Records()
				s.Streams()
				s.Info()
			}
		}()
	}
	for i := 0; i < 10000; i++ {
		var tm time.Time
		if i%3 != 0 && i > 5 {
			tm = t0.Add(time.Duration(i) * time.Millisecond)
		}
		s.Add(rec("a", tm), time.Time{})
	}
	close(done)
	wg.Wait()
	s.Flush()
	if mustInfo(t, s).Count != 10000 {
		t.Fatal("lost records")
	}
}

func TestStreamsBySource(t *testing.T) {
	s := New(ModeHistorical, time.Time{})
	for _, r := range []record.Record{
		{Stream: "db"},
		{Stream: "db", Source: "otel"},
		{Stream: "db", Source: "otel"},
		{Stream: "api", Source: "log"},
	} {
		r.Time, r.Fields = t0, map[string]any{}
		s.Add(r, time.Time{})
	}
	got := mustStreams(t, s)
	want := []StreamCount{{Name: "api", Source: "log", Count: 1}, {Name: "db", Source: "log", Count: 1}, {Name: "db", Source: "otel", Count: 2}}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func svcRec(service, kind string, t time.Time) record.Record {
	return record.Record{Source: record.SourceOtel, Service: service, Stream: kind, Time: t, Fields: map[string]any{"k": "v"}}
}

func TestKindsRangeEdges(t *testing.T) {
	s := New(ModeHistorical, time.Time{})
	t10 := time.Date(2026, 11, 17, 10, 0, 0, 0, time.UTC)
	s.Add(svcRec("a", "db", t10), time.Time{})
	s.Add(svcRec("a", "db", t10.Add(5*time.Minute)), time.Time{})
	s.Add(svcRec("a", "x", t10.Add(10*time.Minute)), time.Time{})
	k, _, _ := s.Kinds("otel", "a", t10, t10.Add(10*time.Minute))
	if !reflect.DeepEqual(k, []Count{{"db", 2}}) {
		t.Fatalf("bounded: %v", k)
	}
	k, _, _ = s.Kinds("otel", "a", time.Time{}, time.Time{})
	if !reflect.DeepEqual(k, []Count{{"db", 2}, {"x", 1}}) {
		t.Fatalf("unbounded: %v", k)
	}
}

func TestKindsZeroTime(t *testing.T) {
	s := New(ModeHistorical, time.Time{})
	s.Add(svcRec("a", "db", time.Time{}), time.Time{})
	if k, _, _ := s.Kinds("otel", "a", time.Time{}, time.Time{}); len(k) != 1 {
		t.Fatalf("zero from should count: %v", k)
	}
	if k, _, _ := s.Kinds("otel", "a", t0, time.Time{}); len(k) != 0 {
		t.Fatalf("bounded from should skip: %v", k)
	}
}

func TestKindsServiceAll(t *testing.T) {
	s := New(ModeHistorical, time.Time{})
	s.Add(svcRec("a", "db", t0), time.Time{})
	s.Add(svcRec("a", "db", t0), time.Time{})
	s.Add(svcRec("a", "rpc", t0), time.Time{})
	s.Add(svcRec("b", "http.server", t0), time.Time{})
	k, sv, _ := s.Kinds("otel", "*", time.Time{}, time.Time{})
	if !reflect.DeepEqual(k, []Count{{"db", 2}, {"http.server", 1}, {"rpc", 1}}) {
		t.Fatalf("kinds: %v", k)
	}
	if !reflect.DeepEqual(sv, []Count{{"a", 3}, {"b", 1}}) {
		t.Fatalf("services: %v", sv)
	}
	k, sv, _ = s.Kinds("otel", "b", time.Time{}, time.Time{})
	if !reflect.DeepEqual(k, []Count{{"http.server", 1}}) || len(sv) != 2 {
		t.Fatalf("b: %v %v", k, sv)
	}
	if k, _, _ := s.Kinds("", "*", time.Time{}, time.Time{}); len(k) != 0 {
		t.Fatalf("log source should be empty: %v", k)
	}
}

func TestKindsEmpty(t *testing.T) {
	s := New(ModeHistorical, time.Time{})
	k, sv, _ := s.Kinds("otel", "*", time.Time{}, time.Time{})
	if k == nil || sv == nil || len(k) != 0 || len(sv) != 0 {
		t.Fatalf("empty store: %#v %#v", k, sv)
	}
	s.Add(svcRec("a", "db", t0), time.Time{})
	k, sv, _ = s.Kinds("otel", "nope", time.Time{}, time.Time{})
	if k == nil || len(k) != 0 || len(sv) != 1 {
		t.Fatalf("unknown service: %#v %#v", k, sv)
	}
	k, sv, _ = s.Kinds("bogus", "*", time.Time{}, time.Time{})
	if k == nil || sv == nil {
		t.Fatalf("bogus source: %#v %#v", k, sv)
	}
}

func TestKindsBudget(t *testing.T) {
	if raceEnabled {
		t.Skip("wall-clock budget is not checked under the race detector")
	}
	s := New(ModeHistorical, time.Time{})
	const n = 200000
	for i := 0; i < n; i++ {
		r := record.Record{
			Source:  []string{record.SourceLog, record.SourceOtel}[i%2],
			Service: fmt.Sprintf("svc%d", i%3),
			Stream:  fmt.Sprintf("kind%d", i%30),
			Time:    t0.Add(time.Duration(i) * time.Millisecond),
			Fields:  map[string]any{"a": "b", "c": "d"},
		}
		s.Add(r, time.Time{})
	}
	s.Flush() // the budget is for the query, not for the writer catching up
	from, to := t0.Add(n*time.Millisecond/10), t0.Add(n*time.Millisecond*9/10)
	best := time.Hour
	for i := 0; i < 5; i++ {
		start := time.Now()
		s.Kinds("otel", "*", from, to)
		if d := time.Since(start); d < best {
			best = d
		}
	}
	if best > 50*time.Millisecond {
		t.Fatalf("Kinds over %d records took %v, budget 50ms", n, best)
	}
}

func TestKindsConcurrentAdd(t *testing.T) {
	s := New(ModeLive, time.Time{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 10000; i++ {
			s.Add(svcRec("a", "db", t0.Add(time.Duration(i)*time.Millisecond)), time.Time{})
		}
	}()
	for {
		s.Kinds("otel", "*", time.Time{}, time.Time{})
		select {
		case <-done:
			if k, _, _ := s.Kinds("otel", "*", time.Time{}, time.Time{}); k[0].Count != 10000 {
				t.Fatalf("final count %v", k)
			}
			return
		default:
		}
	}
}

// TestReplayNeverSkips: replays and text loads wait for the writer instead
// of skipping records when the queue is full.
func TestReplayNeverSkips(t *testing.T) {
	s := New(ModeHistorical, time.Time{})
	defer s.Close()
	s.e.SetMaxQueue(10)
	for i := 0; i < 2000; i++ {
		s.Add(rec("a", t0.Add(time.Duration(i)*time.Millisecond)), time.Time{})
	}
	if n := mustInfo(t, s).Count; n != 2000 {
		t.Fatalf("stored %d of 2000", n)
	}
}

// A request makes two reads (schema then query, span then trace). When the
// writer is behind, only the first may wait: the second would time out too.
func TestReadsWaitOnceWhileWriterIsBehind(t *testing.T) {
	s := New(ModeLive, time.Now())
	defer s.Close()
	waits := 0
	s.waitFor = func(d time.Duration) bool { waits++; time.Sleep(d); return false }
	start := time.Now()
	s.Streams()
	s.Info()
	if waits != 1 {
		t.Errorf("waited %d times for a writer that is behind, want 1", waits)
	}
	if el := time.Since(start); el > readWait+readWait/2 {
		t.Errorf("two reads took %v", el)
	}
}

func mustInfo(t testing.TB, s *Store) Info {
	t.Helper()
	in, err := s.Info()
	if err != nil {
		t.Fatal(err)
	}
	return in
}

func mustStreams(t testing.TB, s *Store) []StreamCount {
	t.Helper()
	out, err := s.Streams()
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// afterFirst runs fn once, after the first statement a query sends.
type afterFirst struct {
	query.Backend
	fn   func()
	done bool
}

func (a *afterFirst) QueryContext(ctx context.Context, q string, args ...any) (*sql.Rows, error) {
	rows, err := a.Backend.QueryContext(ctx, q, args...)
	if !a.done {
		a.done = true
		a.fn() // DuckDB has built this answer already
	}
	return rows, err
}

// TestQueryIsOneSnapshot: a query's statements (counts, then the page) see
// the same records, even when records are written between them: the count
// and the rows shown agree (review §4).
func TestQueryIsOneSnapshot(t *testing.T) {
	s := New(ModeLive, time.Time{})
	defer s.Close()
	for i := 0; i < 3; i++ {
		s.Add(logRecord(t, logLine(i, dayNow)), dayNow)
	}
	s.e.Sync(5 * time.Second)
	queryBackend = func(b query.Backend) query.Backend {
		return &afterFirst{Backend: b, fn: func() {
			for i := 3; i < 8; i++ {
				s.e.Add(withDay(logRecord(t, logLine(i, dayNow)), i))
			}
			s.e.Sync(5 * time.Second)
		}}
	}
	defer func() { queryBackend = func(b query.Backend) query.Backend { return b } }()
	tl, _ := s.e.Tallies("log", "*", "api")
	res, err := s.Query(context.Background(), schema.FromTallies("log", "api", tl), query.Query{Stream: "api", Source: "log", Limit: 100}, dayNow)
	if err != nil {
		t.Fatal(err)
	}
	if res.Matched != len(res.Rows) {
		t.Errorf("matched %d, but %d rows shown", res.Matched, len(res.Rows))
	}
}

func withDay(r record.Record, n int) record.Record {
	r.Seq = DayNumber(dayNow)<<32 + 100 + n
	return r
}

// TestSeqSurvivesMaxSeqError: while the stored ids of a day can't be read,
// a record takes the next id after the highest one given out this session,
// and the day asks again; once the read works, ids continue after both.
func TestSeqSurvivesMaxSeqError(t *testing.T) {
	s := New(ModeHistorical, time.Time{})
	defer s.Close()
	var warns []string
	s.warn = func(m string) { warns = append(warns, m) }
	fail, asked := true, 0
	s.maxSeq = func(day int) (int, error) {
		asked++
		if fail {
			return 0, fmt.Errorf("read failed (test)")
		}
		return 0, nil // the records added so far aren't written yet
	}
	s.Add(rec("a", t0), time.Time{})
	s.Add(rec("a", t0), time.Time{})
	fail = false
	s.Add(rec("a", t0), time.Time{})
	s.Add(rec("a", t0), time.Time{})
	if asked != 3 {
		t.Errorf("MaxSeq asked %d times, want 3 (again after each failure, then not)", asked)
	}
	if len(warns) != 1 {
		t.Errorf("warnings %q, want one", warns)
	}
	seen := map[int]bool{}
	for _, r := range s.Records() {
		if seen[r.Seq] {
			t.Errorf("id %d given twice", r.Seq)
		}
		seen[r.Seq] = true
	}
	if len(seen) != 4 {
		t.Errorf("%d distinct ids, want 4", len(seen))
	}
}

// TestUntimedAfterOutOfRangeKeepsValidTime: a time _time can't hold is
// stored as NULL, so it isn't passed on: the untimed record after it takes
// the last valid time.
func TestUntimedAfterOutOfRangeKeepsValidTime(t *testing.T) {
	s := New(ModeHistorical, time.Time{})
	defer s.Close()
	s.Add(rec("a", t0), time.Time{})
	s.Add(rec("a", time.Date(3000, 1, 1, 0, 0, 0, 0, time.UTC)), time.Time{})
	s.Add(rec("a", time.Time{}), time.Time{})
	s.Flush()
	var got *time.Time
	if err := s.e.DB().QueryRow("SELECT _time FROM all_records WHERE _id = 3").Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got == nil || !got.Equal(t0) {
		t.Errorf("untimed record's _time = %v, want %v", got, t0)
	}
}
