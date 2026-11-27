package web

import (
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jimbeveridge/loghorn/internal/adapter"
	"github.com/jimbeveridge/loghorn/internal/config"
	"github.com/jimbeveridge/loghorn/internal/record"
	"github.com/jimbeveridge/loghorn/internal/store"
)

// sqlServer serves a store of n app records on a unix socket.
func sqlServer(t *testing.T, n int) (*http.Client, *store.Store) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	st := store.New(store.ModeLive, time.Date(2026, 11, 17, 17, 0, 0, 0, time.UTC))
	for i := 0; i < n; i++ {
		line := `{"time":"2026-11-17T17:00:0` + string(rune('0'+i%10)) + `Z","severity":"ERROR","message":"boom","labels":{"schema":"app"}}`
		r, ok := record.Normalize(adapter.New(config.Input{}).ParseLine([]byte(line)), config.Input{})
		if !ok {
			t.Fatal(line)
		}
		st.Add(r, time.Time{})
	}
	path := filepath.Join(shortTemp(t), "s.sock")
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: New(Options{Store: st, ConfigBase: t.TempDir()})}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })
	return unixClient(path), st
}

func postSQL(t *testing.T, c *http.Client, url, body string) (*http.Response, string) {
	t.Helper()
	resp, err := c.Post(url, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return resp, string(b)
}

func TestSQLOverSocket(t *testing.T) {
	c, _ := sqlServer(t, 3)
	resp, body := postSQL(t, c, "http://loghorn/api/sql", `{"sql":"SELECT severity, count(*) AS n FROM logs.\"app\" GROUP BY ALL"}`)
	if resp.StatusCode != 200 || body != "severity\tn\nERROR\t3\n" {
		t.Fatalf("%d %q", resp.StatusCode, body)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/tab-separated-values") {
		t.Fatalf("Content-Type %q", ct)
	}
	resp, body = postSQL(t, c, "http://loghorn/api/sql", `{"sql":"SELECT count(*) AS n FROM logs.\"app\"","format":"jsonl"}`)
	if resp.StatusCode != 200 || body != "{\"n\":3}\n" {
		t.Fatalf("jsonl: %d %q", resp.StatusCode, body)
	}
}

func TestSQLNoteWhenCut(t *testing.T) {
	c, _ := sqlServer(t, 5)
	resp, body := postSQL(t, c, "http://loghorn/api/sql", `{"sql":"SELECT seq FROM logs.\"app\" ORDER BY seq","limit":2}`)
	if resp.StatusCode != 200 || strings.Count(body, "\n") != 3 {
		t.Fatalf("%d %q", resp.StatusCode, body)
	}
	if note := resp.Header.Get("X-Loghorn-Note"); note != "showing the first 2 rows; add a LIMIT or pass -limit for more" {
		t.Fatalf("note %q", note)
	}
	resp, _ = postSQL(t, c, "http://loghorn/api/sql", `{"sql":"SELECT seq FROM logs.\"app\""}`)
	if note := resp.Header.Get("X-Loghorn-Note"); note != "" {
		t.Fatalf("note on an uncut answer: %q", note)
	}
}

func TestSQLErrors(t *testing.T) {
	c, _ := sqlServer(t, 1)
	for body, want := range map[string]string{
		`{"sql":"DROP VIEW logs.\"app\""}`:        "only one query",
		`{"sql":"SELECT nope FROM logs.\"app\""}`: "nope",
		`{"sql":"SELECT 1","format":"yaml"}`:      "not supported",
		`{"sql":""}`:                              "sql is required",
		`{"sql":"SELECT 1","limit":-1}`:           "limit",
		`not json`:                                "bad request",
	} {
		resp, got := postSQL(t, c, "http://loghorn/api/sql", body)
		if resp.StatusCode != http.StatusBadRequest || !strings.Contains(got, want) {
			t.Errorf("%s: %d %s (want 400 with %q)", body, resp.StatusCode, got, want)
		}
	}
}

func TestSQLRefusedOverTCP(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	st := store.New(store.ModeLive, time.Time{})
	srv := httptest.NewServer(New(Options{Store: st, ConfigBase: t.TempDir()}))
	defer srv.Close()
	host := strings.TrimPrefix(srv.URL, "http://")
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/sql", strings.NewReader(`{"sql":"SELECT 1"}`))
	req.Header.Set("Origin", "http://"+host)
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden || !strings.Contains(string(b), "socket") {
		t.Fatalf("%d %s", resp.StatusCode, b)
	}
}

func TestSQLLimitAboveMax(t *testing.T) {
	c, _ := sqlServer(t, 1)
	resp, got := postSQL(t, c, "http://loghorn/api/sql", `{"sql":"SELECT 1","limit":100001}`)
	if resp.StatusCode != http.StatusBadRequest || !strings.Contains(got, "limit must be at most 100000") {
		t.Fatalf("%d %s", resp.StatusCode, got)
	}
	if resp, got := postSQL(t, c, "http://loghorn/api/sql", `{"sql":"SELECT 1","limit":100000}`); resp.StatusCode != http.StatusOK {
		t.Fatalf("limit at the max: %d %s", resp.StatusCode, got)
	}
}
