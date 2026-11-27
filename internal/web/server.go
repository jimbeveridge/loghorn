// Package web serves the query view: an embedded page and a JSON API over the
// session store. It listens on loopback only and refuses any request whose
// Host is not the listener's own loopback address (DNS rebinding) and any
// write whose Origin is not the page's own (cross-site requests).
package web

import (
	"bytes"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/jimbeveridge/loghorn/internal/export"
	"github.com/jimbeveridge/loghorn/internal/favorites"
	"github.com/jimbeveridge/loghorn/internal/jsoncolor"
	"github.com/jimbeveridge/loghorn/internal/query"
	"github.com/jimbeveridge/loghorn/internal/record"
	"github.com/jimbeveridge/loghorn/internal/schema"
	"github.com/jimbeveridge/loghorn/internal/sqlcolor"
	"github.com/jimbeveridge/loghorn/internal/sqlfmt"
	"github.com/jimbeveridge/loghorn/internal/store"
	"github.com/jimbeveridge/loghorn/internal/svcconf"
	"github.com/jimbeveridge/loghorn/internal/trace"
)

//go:embed static
var embedded embed.FS

// maxBody caps request bodies (schemas, queries, favorites, pins).
const maxBody = 1 << 20

// Options configures the handler.
type Options struct {
	Store *store.Store
	// ConfigBase holds the per-service config: <ConfigBase>/services/<key>/
	// (svcconf.Dir) with pins, favorites and field settings.
	ConfigBase string
	Now        func() time.Time // nil means time.Now

	// Instance identity, reported by /api/session so a CLI can pick among
	// running instances: process id, working directory, TCP port and start time.
	PID     int
	Cwd     string
	Port    int
	Started time.Time

	// loghorn serve's identity, also reported by /api/session: its data
	// folder, socket, version and OTLP/HTTP port (0 for a view without one).
	DataDir  string
	Socket   string
	Version  string
	OTLPPort int
}

type server struct {
	o       Options
	static  fs.FS
	writeMu sync.Mutex // serialises pins, field settings and favorites writes
}

// Listen opens a TCP listener on 127.0.0.1:port (0 picks a free port).
func Listen(port int) (net.Listener, error) {
	return net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
}

// New returns the HTTP handler for the query view.
func New(o Options) http.Handler {
	if o.Now == nil {
		o.Now = time.Now
	}
	sub, err := fs.Sub(embedded, "static")
	if err != nil {
		panic(err) // the embed directive guarantees static/ exists
	}
	s := &server{o: o, static: sub}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", s.index)
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServerFS(sub)))
	mux.HandleFunc("GET /api/session", s.session)
	mux.HandleFunc("GET /api/streams", s.streams)
	mux.HandleFunc("GET /api/kinds", s.kinds)
	mux.HandleFunc("GET /api/pins", s.getPins)
	mux.HandleFunc("PUT /api/pins", s.putPins)
	mux.HandleFunc("GET /api/schema", s.getSchema)
	mux.HandleFunc("PUT /api/schema", s.putSchema)
	mux.HandleFunc("POST /api/query", s.query)
	mux.HandleFunc("POST /api/sql", s.sql)
	mux.HandleFunc("GET /api/record", s.record)
	mux.HandleFunc("GET /api/trace", s.trace)
	mux.HandleFunc("GET /api/aggregate", s.aggregate)
	mux.HandleFunc("GET /api/favorites", s.listFavorites)
	mux.HandleFunc("PUT /api/favorites/{name}", s.putFavorite)
	mux.HandleFunc("DELETE /api/favorites/{name}", s.deleteFavorite)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotFound, "not found")
	})
	return s.guard(mux)
}

// guard enforces the loopback Host on every request and the page's own
// Origin on every non-GET request. POST /api/query alone may omit Origin (a
// curl client); a present, foreign Origin is refused everywhere. Requests on
// the per-user unix socket skip both checks: those guard against a browser
// page reaching the TCP port, and a browser cannot reach the socket (its 0700
// directory is the access control).
func (s *server) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !onUnixSocket(r) {
			port := listenerPort(r)
			if port == "" || (r.Host != "127.0.0.1:"+port && r.Host != "localhost:"+port) {
				writeError(w, http.StatusForbidden, "forbidden host")
				return
			}
		}
		if !onUnixSocket(r) && r.Method != http.MethodGet && r.Method != http.MethodHead {
			origin := r.Header.Get("Origin")
			exempt := origin == "" && r.Method == http.MethodPost && r.URL.Path == "/api/query"
			if !exempt && origin != "http://"+r.Host {
				writeError(w, http.StatusForbidden, "forbidden origin")
				return
			}
		}
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		r.Body = http.MaxBytesReader(w, r.Body, maxBody)
		next.ServeHTTP(w, r)
	})
}

// onUnixSocket reports whether the request arrived on a unix socket listener.
func onUnixSocket(r *http.Request) bool {
	_, ok := r.Context().Value(http.LocalAddrContextKey).(*net.UnixAddr)
	return ok
}

// listenerPort is the port of the local address the request arrived on, or
// "" when the server did not record one (the guard then refuses).
func listenerPort(r *http.Request) string {
	a, ok := r.Context().Value(http.LocalAddrContextKey).(net.Addr)
	if !ok || a == nil {
		return ""
	}
	_, port, err := net.SplitHostPort(a.String())
	if err != nil {
		return ""
	}
	return port
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

func decode(r *http.Request, v any) error {
	return json.NewDecoder(r.Body).Decode(v)
}

func (s *server) index(w http.ResponseWriter, r *http.Request) {
	b, err := fs.ReadFile(s.static, "index.html")
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(b)
}

// allServices is the service value that means every service (read only).
const allServices = "*"

// serviceParam is ?service=, with "" meaning record.DefaultService.
func serviceParam(r *http.Request) string {
	return orDefault(r.URL.Query().Get("service"))
}

func orDefault(svc string) string {
	if svc == "" {
		return record.DefaultService
	}
	return svc
}

// saveService is the ?service= a write applies to. All services can't be
// written: it writes a 400 and returns false.
func saveService(w http.ResponseWriter, r *http.Request) (string, bool) {
	svc := serviceParam(r)
	if svc == allServices {
		writeError(w, http.StatusBadRequest, "Choose a service to save")
		return "", false
	}
	return svc, true
}

// readService is the ?service= of a read that is per service only (pins,
// favorites); the page asks each service in turn for all of them.
func readService(w http.ResponseWriter, r *http.Request) (string, bool) {
	svc := serviceParam(r)
	if svc == allServices {
		writeError(w, http.StatusBadRequest, "choose a service")
		return "", false
	}
	return svc, true
}

// svcDir is the config directory of one service (never allServices).
func (s *server) svcDir(svc string) string {
	return svcconf.Dir(s.o.ConfigBase, svc)
}

// timePtr is nil for the zero time, so JSON shows null rather than year 1.
func timePtr(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}

type sessionJSON struct {
	Mode       store.Mode `json:"mode"`
	RunStart   *time.Time `json:"runStart"`
	Earliest   *time.Time `json:"earliest"`
	Latest     *time.Time `json:"latest"`
	Count      int        `json:"count"`
	ConfigBase string     `json:"configBase"`
	PID        int        `json:"pid"`
	Cwd        string     `json:"cwd"`
	Port       int        `json:"port"`
	Started    time.Time  `json:"started"`
	Version    string     `json:"version"`
	DataDir    string     `json:"dataDir"`
	Socket     string     `json:"socket"`
	OTLPPort   int        `json:"otlpPort"`
	// Error is why the counts are missing (zero): the store couldn't be
	// read.
	Error string `json:"error,omitempty"`
}

// session answers 200 with serve's identity even when the store can't be
// read (counts zero, the reason in error): sock.Probe finds serve here.
func (s *server) session(w http.ResponseWriter, r *http.Request) {
	in, err := s.o.Store.Info()
	msg := ""
	if err != nil {
		msg = err.Error()
		in = store.Info{Mode: in.Mode, RunStart: in.RunStart}
	}
	writeJSON(w, http.StatusOK, sessionJSON{Error: msg,
		Mode: in.Mode, RunStart: timePtr(in.RunStart), Earliest: timePtr(in.Earliest),
		Latest: timePtr(in.Latest), Count: in.Count, ConfigBase: s.o.ConfigBase,
		PID: s.o.PID, Cwd: s.o.Cwd, Port: s.o.Port, Started: s.o.Started,
		Version: s.o.Version, DataDir: s.o.DataDir, Socket: s.o.Socket, OTLPPort: s.o.OTLPPort,
	})
}

type streamJSON struct {
	Name   string `json:"name"`
	Source string `json:"source"`
	Count  int    `json:"count"`
}

func (s *server) streams(w http.ResponseWriter, r *http.Request) {
	streams, err := s.o.Store.Streams()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := []streamJSON{}
	for _, sc := range streams {
		out = append(out, streamJSON{sc.Name, sc.Source, sc.Count})
	}
	writeJSON(w, http.StatusOK, out)
}

type countJSON struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
}

type serviceCountJSON struct {
	Key   string `json:"key"`
	Count int    `json:"count"`
}

// kinds counts the kinds of one service (or "*") and the records of every
// service, for one source, within the ?from=&to= range (query range values).
func (s *server) kinds(w http.ResponseWriter, r *http.Request) {
	src, ok := sourceParam(w, r)
	if !ok {
		return
	}
	rng := query.Range{From: r.URL.Query().Get("from"), To: r.URL.Query().Get("to")}
	from, to, err := rng.Resolve(s.o.Now(), s.o.Store.RunStart())
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	kc, sc, err := s.o.Store.Kinds(src, serviceParam(r), from, to)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := struct {
		Kinds    []countJSON        `json:"kinds"`
		Services []serviceCountJSON `json:"services"`
	}{make([]countJSON, 0, len(kc)), make([]serviceCountJSON, 0, len(sc))}
	for _, c := range kc {
		out.Kinds = append(out.Kinds, countJSON{c.Name, c.Count})
	}
	for _, c := range sc {
		out.Services = append(out.Services, serviceCountJSON{c.Name, c.Count})
	}
	writeJSON(w, http.StatusOK, out)
}

type pinsJSON struct {
	Pins []svcconf.Pin `json:"pins"`
}

func (s *server) getPins(w http.ResponseWriter, r *http.Request) {
	svc, ok := readService(w, r)
	if !ok {
		return
	}
	pins, err := svcconf.LoadPins(s.svcDir(svc))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, pinsJSON{pins})
}

// putPins replaces the service's pins and returns them as saved (sources
// normalised, duplicates dropped).
func (s *server) putPins(w http.ResponseWriter, r *http.Request) {
	svc, ok := saveService(w, r)
	if !ok {
		return
	}
	var body pinsJSON
	if err := decode(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "bad pins: "+err.Error())
		return
	}
	dir := s.svcDir(svc)
	s.writeMu.Lock()
	err := svcconf.SavePins(dir, body.Pins)
	var pins []svcconf.Pin
	if err == nil {
		pins, err = svcconf.LoadPins(dir)
	}
	s.writeMu.Unlock()
	switch {
	case errors.Is(err, svcconf.ErrBadPin):
		writeError(w, http.StatusBadRequest, err.Error())
	case err != nil:
		writeError(w, http.StatusInternalServerError, err.Error())
	default:
		writeJSON(w, http.StatusOK, pinsJSON{pins})
	}
}

// effective returns the effective schema of stream k within service svc: the
// inferred schema (cached by the store until the data changes) with the
// service's saved field settings applied. For allServices it infers over
// every service and applies no saved settings. k.Source must already be
// parsed (record.ParseSource).
func (s *server) effective(svc string, k record.StreamKey) (schema.Schema, error) {
	inferred, err := s.o.Store.Schema(svc, k.Source, k.Name)
	if err != nil {
		return schema.Schema{}, err
	}
	if svc == allServices {
		return schema.Effective(inferred, nil), nil
	}
	saved, err := schema.LoadFields(s.svcDir(svc), k.Source)
	if err != nil {
		return schema.Schema{}, err
	}
	return schema.Effective(inferred, saved), nil
}

var allTypes = []schema.Type{schema.TInt, schema.TFloat, schema.TBool, schema.TString, schema.TDuration, schema.TTimestamp, schema.TJSON}

// sourceParam parses the optional ?source= (default log). On an unknown
// source it writes a 400 and returns false.
func sourceParam(w http.ResponseWriter, r *http.Request) (string, bool) {
	v := r.URL.Query().Get("source")
	src, ok := record.ParseSource(v)
	if !ok {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("unknown source %q", v))
	}
	return src, ok
}

func (s *server) getSchema(w http.ResponseWriter, r *http.Request) {
	stream := r.URL.Query().Get("stream")
	if stream == "" {
		writeError(w, http.StatusBadRequest, "stream is required")
		return
	}
	src, ok := sourceParam(w, r)
	if !ok {
		return
	}
	sch, err := s.effective(serviceParam(r), record.StreamKey{Source: src, Name: stream})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	ops := map[schema.Type][]string{}
	for _, t := range allTypes {
		ops[t] = query.OpsFor(t)
	}
	writeJSON(w, http.StatusOK, struct {
		Schema schema.Schema            `json:"schema"`
		Ops    map[schema.Type][]string `json:"ops"`
	}{sch, ops})
}

// putSchema merges the body's fields, by path, into the service's saved
// field settings for the source; other saved fields are kept. The stream is
// not needed: field settings apply to every kind of the source.
func (s *server) putSchema(w http.ResponseWriter, r *http.Request) {
	src, ok := sourceParam(w, r)
	if !ok {
		return
	}
	svc, ok := saveService(w, r)
	if !ok {
		return
	}
	var body struct {
		Fields []schema.Field `json:"fields"`
	}
	if err := decode(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "bad fields: "+err.Error())
		return
	}
	s.writeMu.Lock()
	path, err := schema.MergeFields(s.svcDir(svc), src, body.Fields)
	s.writeMu.Unlock()
	if err != nil {
		writeError(w, saveStatus(err), err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"path": path})
}

// saveStatus maps a MergeFields error to 500 for file-system failures and 400
// for everything else (unsavable stream, invalid field).
func saveStatus(err error) int {
	var pe *fs.PathError
	var le *os.LinkError
	var se *os.SyscallError
	if errors.As(err, &pe) || errors.As(err, &le) || errors.As(err, &se) {
		return http.StatusInternalServerError
	}
	return http.StatusBadRequest
}

func (s *server) query(w http.ResponseWriter, r *http.Request) {
	var q query.Query
	if err := decode(r, &q); err != nil {
		writeError(w, http.StatusBadRequest, "bad query: "+err.Error())
		return
	}
	if q.Stream == "" {
		writeError(w, http.StatusBadRequest, "stream is required")
		return
	}
	var format export.Format
	if q.Format != "" {
		f, err := export.ParseFormat(q.Format)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		format = f
	}
	src, ok := record.ParseSource(q.Source)
	if !ok {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("unknown source %q", q.Source))
		return
	}
	q.Service = orDefault(q.Service)
	sch, err := s.effective(q.Service, record.StreamKey{Source: src, Name: q.Stream})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := q.Validate(sch); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	res, err := s.o.Store.Query(r.Context(), sch, q, s.o.Now())
	if err != nil {
		// Validate has refused what the request got wrong; what fails
		// here is the engine.
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	switch format {
	case "":
		writeJSON(w, http.StatusOK, res)
	case export.SQL:
		writeSQL(w, q, sch, &res)
	default:
		writeExport(w, format, &res, sch, q)
	}
}

// writeSQL answers a query with its DuckDB SQL, built from the query and its
// run (which resolves what the SQL freezes). A query the SQL cannot express
// exactly is a 400.
func writeSQL(w http.ResponseWriter, q query.Query, sch schema.Schema, res *query.Result) {
	text, err := query.ToSQL(q, sch, res)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	w.Header().Set("Content-Type", export.SQL.ContentType())
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, text)
}

// writeExport answers a query in an export format: the text with the format's
// Content-Type, and what the result left out in X-Loghorn-Note.
func writeExport(w http.ResponseWriter, f export.Format, res *query.Result, sch schema.Schema, q query.Query) {
	var b bytes.Buffer
	if err := export.Write(&b, f, res, sch); err != nil {
		code := http.StatusInternalServerError
		if errors.Is(err, export.ErrUnsupported) {
			code = http.StatusBadRequest
		}
		writeError(w, code, err.Error())
		return
	}
	w.Header().Set("Content-Type", f.ContentType())
	w.Header().Set("Cache-Control", "no-store")
	if note := export.Note(res, q); note != "" {
		w.Header().Set("X-Loghorn-Note", note)
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(b.Bytes())
}

// trace answers GET /api/trace?trace=<id>&span=<id>: the tree of one trace
// over every service's records. trace wins; with only span, the trace of that
// span is found first.
func (s *server) trace(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	traceID, spanID := q.Get("trace"), q.Get("span")
	if len(traceID) > 256 || len(spanID) > 256 {
		writeError(w, http.StatusBadRequest, "id too long")
		return
	}
	if traceID == "" && spanID == "" {
		writeError(w, http.StatusBadRequest, "trace or span is required")
		return
	}
	if traceID == "" {
		recs, err := s.o.Store.SpanRecords(spanID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		if traceID = trace.Find(recs, spanID); traceID == "" {
			writeError(w, http.StatusNotFound, "no record with that span id")
			return
		}
	}
	recs, err := s.o.Store.TraceRecords(traceID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	t, ok := trace.Build(recs, traceID, trace.Limit)
	if !ok {
		writeError(w, http.StatusNotFound, "no spans or logs with that trace id")
		return
	}
	writeJSON(w, http.StatusOK, t)
}

// aggregate answers GET /api/aggregate?name=&service=&from=&to=: the merge of
// the traces whose operation root span is named name. service ("" or "*": all)
// restricts the roots only; from/to are query range values.
func (s *server) aggregate(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	name := q.Get("name")
	if name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}
	if len(name) > 1024 {
		writeError(w, http.StatusBadRequest, "name too long")
		return
	}
	rng := query.Range{From: q.Get("from"), To: q.Get("to")}
	from, to, err := rng.Resolve(s.o.Now(), s.o.Store.RunStart())
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	recs, err := s.o.Store.AggregateRecords(name)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	op, ok := trace.Aggregate(recs, trace.AggOptions{Name: name, Service: q.Get("service"), From: from, To: to})
	if !ok {
		writeError(w, http.StatusNotFound, "no root spans with that name in range")
		return
	}
	writeJSON(w, http.StatusOK, op)
}

// record answers one record by ?seq=: the raw record pretty-printed and
// coloured, each sql field formatted and coloured, and each multiline field
// that holds more than one line (text) as it is, for the page's row detail.
func (s *server) record(w http.ResponseWriter, r *http.Request) {
	seq, err := strconv.Atoi(r.URL.Query().Get("seq"))
	if err != nil || seq < 1 {
		writeError(w, http.StatusBadRequest, "seq must be a positive integer")
		return
	}
	found, ok, err := s.o.Store.Record(seq)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !ok {
		writeError(w, http.StatusNotFound, "no record with that seq")
		return
	}
	rec := &found
	raw := string(rec.Raw)
	var buf bytes.Buffer
	if json.Indent(&buf, rec.Raw, "", "  ") == nil {
		raw = buf.String()
	}
	sch, err := s.effective(orDefault(rec.Service), rec.Key())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	sql := map[string]string{}
	tokens := map[string][]sqlcolor.Token{}
	text := map[string]string{} // multiline fields of several lines, as they are
	system := sqlfmt.SystemOf(rec.Fields)
	for _, f := range sch.Fields {
		v, ok := rec.Fields[f.Path].(string)
		if !ok {
			continue
		}
		if f.Visualizer == schema.VMultiline {
			// One line reads well enough in the raw record below.
			if schema.Multiline(v) {
				text[f.Path] = v
			}
			continue
		}
		if f.Visualizer != schema.VSQL {
			continue
		}
		if out, err := sqlfmt.Format(v, system); err == nil {
			sql[f.Path] = out
		} else {
			sql[f.Path] = v
		}
		tokens[f.Path] = sqlcolor.Tokens(sql[f.Path], system)
	}
	writeJSON(w, http.StatusOK, struct {
		Raw       string                      `json:"raw"`
		RawTokens []sqlcolor.Token            `json:"rawTokens,omitempty"`
		SQL       map[string]string           `json:"sql"`
		SQLTokens map[string][]sqlcolor.Token `json:"sqlTokens"`
		Text      map[string]string           `json:"text"`
	}{raw, jsoncolor.Tokens(raw), sql, tokens, text})
}

func (s *server) listFavorites(w http.ResponseWriter, r *http.Request) {
	svc, ok := readService(w, r)
	if !ok {
		return
	}
	favs, err := favorites.Load(s.svcDir(svc))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, favs)
}

// putFavorite stores the body (a query.Query) under the path's name, in the
// ?service= favorites (the query's own service is not saved).
func (s *server) putFavorite(w http.ResponseWriter, r *http.Request) {
	svc, ok := saveService(w, r)
	if !ok {
		return
	}
	var q query.Query
	if err := decode(r, &q); err != nil {
		writeError(w, http.StatusBadRequest, "bad query: "+err.Error())
		return
	}
	f := favorites.Favorite{Name: r.PathValue("name"), Query: q}
	s.writeMu.Lock()
	err := favorites.Put(s.svcDir(svc), f, r.URL.Query().Get("overwrite") == "1")
	s.writeMu.Unlock()
	switch {
	case errors.Is(err, favorites.ErrExists):
		writeError(w, http.StatusConflict, err.Error())
	case errors.Is(err, favorites.ErrBadName):
		writeError(w, http.StatusBadRequest, err.Error())
	case err != nil:
		writeError(w, http.StatusInternalServerError, err.Error())
	default:
		writeJSON(w, http.StatusOK, f)
	}
}

func (s *server) deleteFavorite(w http.ResponseWriter, r *http.Request) {
	svc, ok := saveService(w, r)
	if !ok {
		return
	}
	name := r.PathValue("name")
	s.writeMu.Lock()
	err := favorites.Delete(s.svcDir(svc), name)
	s.writeMu.Unlock()
	switch {
	case errors.Is(err, favorites.ErrNotFound):
		writeError(w, http.StatusNotFound, err.Error())
	case err != nil:
		writeError(w, http.StatusInternalServerError, err.Error())
	default:
		writeJSON(w, http.StatusOK, map[string]string{"deleted": name})
	}
}
