package main

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jimbeveridge/loghorn/internal/sock"
)

// sqlSocket serves the pino fixture on a fresh socket and returns its path.
func sqlSocket(t *testing.T) string {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	path := sockPath(t)
	ln, err := sock.Listen(path)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: cliHandler(t, 4001, 5001, "")}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { srv.Close() })
	return path
}

const countDB = `SELECT count(*) AS n FROM logs."db"`

func TestQuerySQL(t *testing.T) {
	path := sqlSocket(t)
	r := runCLI(t, opts{sql: countDB}, nil, path)
	if r.code != 0 || r.stdout != "n\n3\n" || r.stderr != "" {
		t.Fatalf("exit %d; stdout %q; stderr %q", r.code, r.stdout, r.stderr)
	}
}

func TestQuerySQLFromFileAndStdin(t *testing.T) {
	path := sqlSocket(t)
	f := filepath.Join(t.TempDir(), "q.sql")
	os.WriteFile(f, []byte(countDB+"\n"), 0o600)
	if r := runCLI(t, opts{sql: "@" + f}, nil, path); r.code != 0 || r.stdout != "n\n3\n" {
		t.Fatalf("@file: exit %d; %q %q", r.code, r.stdout, r.stderr)
	}
	if r := runCLI(t, opts{sql: "@-"}, strings.NewReader(countDB), path); r.code != 0 || r.stdout != "n\n3\n" {
		t.Fatalf("@-: exit %d; %q %q", r.code, r.stdout, r.stderr)
	}
}

func TestQuerySQLFormats(t *testing.T) {
	path := sqlSocket(t)
	if r := runCLI(t, opts{sql: countDB, format: "jsonl"}, nil, path); r.stdout != "{\"n\":3}\n" {
		t.Fatalf("jsonl: %q %q", r.stdout, r.stderr)
	}
	out := filepath.Join(t.TempDir(), "out.md")
	if r := runCLI(t, opts{sql: countDB, out: out}, nil, path); r.code != 0 {
		t.Fatalf("-o: exit %d %q", r.code, r.stderr)
	}
	if b, _ := os.ReadFile(out); string(b) != "| n |\n| --- |\n| 3 |\n" {
		t.Fatalf("out.md = %q", b)
	}
}

func TestQuerySQLLimitNote(t *testing.T) {
	path := sqlSocket(t)
	r := runCLI(t, opts{sql: `SELECT seq FROM logs."db" ORDER BY seq`, limit: 2, limitSet: true}, nil, path)
	if r.code != 0 || tsvRows(r.stdout) != 2 || !strings.Contains(r.stderr, "showing the first 2 rows") {
		t.Fatalf("exit %d; stdout %q; stderr %q", r.code, r.stdout, r.stderr)
	}
}

func TestQuerySQLErrors(t *testing.T) {
	path := sqlSocket(t)
	r := runCLI(t, opts{sql: `DROP VIEW logs."db"`}, nil, path)
	if r.code != 1 || !strings.HasPrefix(r.stderr, "loghorn: only one query") {
		t.Fatalf("refused: exit %d; stderr %q", r.code, r.stderr)
	}
	r = runCLI(t, opts{sql: countDB}, nil, "")
	if r.code != 1 || !strings.Contains(r.stderr, "loghorn serve isn't running") {
		t.Fatalf("not running: exit %d; stderr %q", r.code, r.stderr)
	}
	for _, o := range []opts{
		{sql: countDB, url: "/?q=x"},
		{sql: countDB, toSQL: true},
		{sql: countDB, format: "yaml"},
		{sql: countDB, limit: 0, limitSet: true},
		{sql: "@-"},
	} {
		if r := runCLI(t, o, nil, path); r.code != 2 {
			t.Errorf("%+v: exit %d; stderr %q", o, r.code, r.stderr)
		}
	}
}

func TestQueryUsageTeachesSQL(t *testing.T) {
	for _, want := range []string{
		`SELECT * FROM loghorn.tables`, // discovery, not a stream that may not exist
		`"loghorn.run.id" = loghorn.this_run_id()`,
		`-sql @- <<'SQL'`, // quoting: SQL literals need single quotes
		"a note on stderr says when rows were cut",
		"printed in UTC",
	} {
		if !strings.Contains(queryUsage, want) {
			t.Errorf("help lacks %q", want)
		}
	}
	if strings.Contains(queryUsage, `DESCRIBE logs."app"`) {
		t.Error(`help still starts from logs."app", which may not exist`)
	}
}

func TestQuerySQLLimitAboveMax(t *testing.T) {
	r := runCLI(t, opts{sql: `SELECT 1`, limit: 100001, limitSet: true}, nil, "")
	if r.code != 2 || !strings.Contains(r.stderr, "limit must be at most 100000") {
		t.Fatalf("exit %d; stderr %q", r.code, r.stderr)
	}
}
