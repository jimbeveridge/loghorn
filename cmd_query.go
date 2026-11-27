package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/jimbeveridge/loghorn/internal/export"
	"github.com/jimbeveridge/loghorn/internal/query"
	"github.com/jimbeveridge/loghorn/internal/sock"
	"github.com/jimbeveridge/loghorn/internal/web"
)

const queryUsage = `Usage: loghorn query [flags] <url | @file | @->
       loghorn query -sql <'SELECT …' | @file | @-> [flags]

Print the data of the web view a page URL names, as TSV (default), CSV,
Markdown, JSON, JSON lines, YAML or TOON, or its DuckDB SQL with -to-sql. The
URL is a loghorn page URL (http://127.0.0.1:PORT/?q=…), a host-less /?q=…
for the running serve, or @file / @- to read it from a file or stdin.

With -sql, run one read-only DuckDB query over the running serve's records
and print its rows (TSV by default; -format csv, md, json or jsonl). At most
1000 rows are printed unless -limit says more (at most 100000), and
a note on stderr says when rows were cut.

Tables: logs."<stream>" for log records, spans."<stream>" for spans, and
logs."*" / spans."*" for every stream at once. Each has seq (store order),
"time" (TIMESTAMPTZ, printed in UTC), service, raw (the original line),
then one typed column per field, named by its path. Quote dotted names and
keywords:
"http.route", "loghorn.run.id", "time". Streams have different fields; list
every table with its columns, ready to paste, or one table's columns
(SHOW TABLES lists internal tables that -sql cannot read):
  loghorn query -sql 'SELECT * FROM loghorn.tables'
  loghorn query -sql 'DESCRIBE logs."<stream>"'
Records from 'loghorn run' carry their run in "loghorn.run.id";
loghorn.this_run_id() is the latest run and loghorn.this_run_start() the
time of its first record:
  loghorn query -sql 'SELECT service, count(*) AS n FROM logs."*" WHERE "loghorn.run.id" = loghorn.this_run_id() GROUP BY ALL'
SQL string literals take single quotes, so pass such SQL on stdin:
  loghorn query -sql @- <<'SQL'
  SELECT "time", message FROM logs."*" WHERE level = 'error' ORDER BY seq DESC LIMIT 20
  SQL
`

func cmdQuery(args []string, env cliEnv) int {
	fs := newFlagSet("query", queryUsage, env)
	var o opts
	fs.StringVar(&o.out, "o", "", "write the output to this file; its extension picks the format")
	fs.StringVar(&o.format, "format", "", "output format: csv, tsv, md, json, jsonl, yaml, toon or sql (default tsv, or -o's extension)")
	fs.IntVar(&o.limit, "limit", 0, "rows: overrides the URL's limit (samples) or top (table, graph)")
	fs.BoolVar(&o.toSQL, "to-sql", false, "print the DuckDB SQL of the URL's query instead of its data")
	fs.StringVar(&o.sql, "sql", "", "run this read-only DuckDB query (or @file, @-) over serve's records instead of a URL")
	pos, code, ok := parseCmd(fs, args, true)
	if !ok {
		return code
	}
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "limit" {
			o.limitSet = true
		}
	})
	switch {
	case o.sql != "" && len(pos) > 0:
		return usageError(env, "-sql takes no URL; run 'loghorn help query'")
	case o.sql != "":
	case len(pos) != 1:
		return usageError(env, "query takes one URL (or @file, @-), or -sql; run 'loghorn help query'")
	default:
		o.url = pos[0]
	}
	return runQuery(o, queryEnv{stdin: env.stdin, stdout: env.stdout, stderr: env.stderr, socket: sock.DefaultPath()})
}

// queryEnv is what query reads and writes, injectable for tests. stdin is read
// only for @-; socket is serve's, for a host-less URL.
type queryEnv struct {
	stdin          io.Reader
	stdout, stderr io.Writer
	socket         string
}

// maxURLArg bounds what @- and @file read.
const maxURLArg = 1 << 20

// isQueryURL reports whether a positional argument is a loghorn page URL
// rather than a command: it starts with http://127.0.0.1 or http://localhost.
// The host is checked exactly later, so http://localhost.example.com is a
// refused query, never a command to launch.
func isQueryURL(s string) bool {
	return strings.HasPrefix(s, "http://127.0.0.1") || strings.HasPrefix(s, "http://localhost")
}

// readURLArg resolves query's argument: @- reads stdin, @path reads the file, both
// trimmed of surrounding whitespace; anything else is the URL itself.
func readURLArg(v string, stdin io.Reader) (string, error) {
	var b []byte
	var err error
	switch {
	case v == "@-":
		b, err = io.ReadAll(io.LimitReader(stdin, maxURLArg))
	case strings.HasPrefix(v, "@"):
		var f *os.File
		if f, err = os.Open(v[1:]); err == nil {
			b, err = io.ReadAll(io.LimitReader(f, maxURLArg))
			f.Close()
		}
	default:
		return v, nil
	}
	return strings.TrimSpace(string(b)), err
}

// runQuery sends the view a page URL names to that loghorn view's /api/query in an export format and writes the answer to -o
// or stdout. It never launches anything and reads stdin only for @-.
// Exit codes: 2 for usage errors, 1 for a failed query, 0 otherwise.
func runQuery(o opts, env queryEnv) int {
	if o.sql != "" {
		return runSQL(o, env)
	}
	usage := func(format string, a ...any) int {
		fmt.Fprintf(env.stderr, "loghorn: "+format+"\n", a...)
		return 2
	}
	fail := func(format string, a ...any) int {
		fmt.Fprintf(env.stderr, "loghorn: "+format+"\n", a...)
		return 1
	}
	raw, err := readURLArg(o.url, env.stdin)
	if err != nil {
		return usage("%s: %v", o.url, err)
	}
	if raw == "" {
		return usage("%s: no URL in it", o.url)
	}
	u, err := url.Parse(raw)
	if err != nil {
		return usage("bad URL: %v", err)
	}
	viaSocket := u.Scheme == "" && u.Host == ""
	if !viaSocket {
		if u.Scheme != "http" {
			return usage("loghorn serves only http://127.0.0.1 and http://localhost; refusing %s", u.Scheme+"://"+u.Host)
		}
		if h := u.Hostname(); h != "127.0.0.1" && h != "localhost" {
			return usage("loghorn serves only 127.0.0.1 and localhost; refusing host %q", h)
		}
	}

	params := u.Query()
	if params.Get("trace") != "" || params.Get("span") != "" {
		fmt.Fprintln(env.stderr, "loghorn: the URL's trace view is not exported; exporting the main view")
	}
	if params.Get("agg") != "" {
		fmt.Fprintln(env.stderr, "loghorn: the URL's aggregate view is not exported; exporting the main view")
	}
	q, err := decodeQ(params.Get("q"))
	if err != nil {
		return usage("%v", err)
	}

	f, err := queryFormat(o)
	if err != nil {
		return usage("%v", err)
	}
	q.Format = string(f)
	if o.limitSet {
		if o.limit < 1 {
			return usage("-limit must be at least 1")
		}
		switch q.View {
		case "heatmap":
			return usage("-limit does not apply to the heatmap, which has no row limit")
		case "table", "graph":
			q.Top = o.limit
		default:
			q.Limit = o.limit
		}
	}
	body, err := json.Marshal(q)
	if err != nil {
		return fail("%v", err)
	}

	client, endpoint, where := (*http.Client)(nil), "", ""
	if viaSocket {
		if _, err := sock.Probe(env.socket); err != nil {
			if errors.Is(err, sock.ErrNotRunning) {
				return fail("%s", notRunningMessage(env.socket))
			}
			return fail("%v", err)
		}
		client, endpoint, where = sock.Client(env.socket), "http://loghorn/api/query", "serve on "+env.socket
	} else {
		client = &http.Client{Transport: &http.Transport{
			Proxy:                 nil,
			DialContext:           (&net.Dialer{Timeout: 5 * time.Second}).DialContext,
			DisableKeepAlives:     true,
			ResponseHeaderTimeout: sock.ResponseTimeout,
		}}
		endpoint, where = "http://"+u.Host+"/api/query", u.Host
	}

	return post(o, env, client, endpoint, where, body)
}

// post sends a query body to endpoint and writes the answer to -o or stdout,
// and its note to stderr. Exit codes as runQuery's.
func post(o opts, env queryEnv, client *http.Client, endpoint, where string, body []byte) int {
	fail := func(format string, a ...any) int {
		fmt.Fprintf(env.stderr, "loghorn: "+format+"\n", a...)
		return 1
	}
	req, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return fail("%v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return fail("can't reach loghorn at %s: %v\nloghorn: is it still running? 'loghorn status' shows whether serve is", where, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		var e struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(b, &e) == nil && e.Error != "" {
			return fail("%s", e.Error)
		}
		return fail("%s answered %s: %s", where, resp.Status, strings.TrimSpace(string(b)))
	}

	if o.out != "" {
		// Created only now, so a failed query leaves an existing file alone.
		out, err := os.Create(o.out)
		if err != nil {
			return fail("%v", err)
		}
		_, err = io.Copy(out, resp.Body)
		if cerr := out.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return fail("%s: %v", o.out, err)
		}
	} else if _, err := io.Copy(env.stdout, resp.Body); err != nil {
		return fail("%v", err)
	}
	if note := resp.Header.Get("X-Loghorn-Note"); note != "" {
		fmt.Fprintln(env.stderr, "loghorn:", note)
	}
	return 0
}

// runSQL sends -sql's query to the running serve's /api/sql and writes the
// rows like runQuery. It talks only to serve's socket: SQL is not answered
// over TCP.
func runSQL(o opts, env queryEnv) int {
	usage := func(format string, a ...any) int {
		fmt.Fprintf(env.stderr, "loghorn: "+format+"\n", a...)
		return 2
	}
	switch {
	case o.url != "":
		return usage("-sql takes no URL; run 'loghorn help query'")
	case o.toSQL:
		return usage("-to-sql prints a URL's SQL; it can't be combined with -sql")
	case o.limitSet && o.limit < 1:
		return usage("-limit must be at least 1")
	case o.limitSet && o.limit > web.MaxSQLLimit:
		return usage("-limit must be at most %d", web.MaxSQLLimit)
	}
	text, err := readURLArg(o.sql, env.stdin)
	if err != nil {
		return usage("%s: %v", o.sql, err)
	}
	if text == "" {
		return usage("%s: no SQL in it", o.sql)
	}
	f, err := queryFormat(o)
	if err != nil {
		return usage("%v", err)
	}
	switch f {
	case export.TSV, export.CSV, export.MD, export.JSON, export.JSONL:
	default:
		return usage("-sql prints tsv, csv, md, json or jsonl, not %s", f)
	}
	req := struct {
		SQL    string `json:"sql"`
		Format string `json:"format"`
		Limit  int    `json:"limit,omitempty"`
	}{text, string(f), 0}
	if o.limitSet {
		req.Limit = o.limit
	}
	body, err := json.Marshal(req)
	if err != nil {
		fmt.Fprintf(env.stderr, "loghorn: %v\n", err)
		return 1
	}
	if _, err := sock.Probe(env.socket); err != nil {
		if errors.Is(err, sock.ErrNotRunning) {
			fmt.Fprintf(env.stderr, "loghorn: %s\n", notRunningMessage(env.socket))
		} else {
			fmt.Fprintf(env.stderr, "loghorn: %v\n", err)
		}
		return 1
	}
	return post(o, env, sock.Client(env.socket), "http://loghorn/api/sql", "serve on "+env.socket, body)
}

// decodeQ decodes the URL's q parameter as the page does: JSON, unknown
// fields ignored, a missing range end defaulting as in the page, and no
// batch offset.
func decodeQ(s string) (query.Query, error) {
	var q query.Query
	if s == "" {
		return q, errors.New("the URL has no ?q= query; copy it from the page's address bar")
	}
	if err := json.Unmarshal([]byte(s), &q); err != nil {
		return q, fmt.Errorf("the URL's q is not a loghorn query: %v", err)
	}
	if q.Range.From == "" {
		q.Range.From = "this-run"
	}
	if q.Range.To == "" {
		q.Range.To = "now"
	}
	q.Offset = 0
	return q, nil
}

// queryFormat picks the output format: -format, else -to-sql's sql, else -o's
// extension, else tsv.
func queryFormat(o opts) (export.Format, error) {
	switch {
	case o.format != "":
		f, err := export.ParseFormat(o.format)
		if err != nil {
			return "", err
		}
		if o.toSQL && f != export.SQL {
			return "", fmt.Errorf("-to-sql prints SQL; it can't be combined with -format %s", f)
		}
		return f, nil
	case o.toSQL:
		return export.SQL, nil
	case o.out != "":
		if f, ok := export.FormatFromPath(o.out); ok {
			return f, nil
		}
	}
	return export.TSV, nil
}
