package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jimbeveridge/loghorn/internal/config"
	"github.com/jimbeveridge/loghorn/internal/store"
)

// TestStoreErrorsAreServerErrors: when DuckDB fails (here, a closed
// database), each endpoint answers 500 with the error, not 404 "no record
// with that seq", an empty list, or 400 (review §3).
func TestStoreErrorsAreServerErrors(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	st := store.New(store.ModeHistorical, time.Time{})
	if err := store.Preload(st, []string{pino}, config.Input{}); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(New(Options{Store: st, ConfigBase: t.TempDir()}))
	defer srv.Close()
	st.Close()
	for _, c := range []struct{ method, path, body string }{
		{"GET", "/api/streams", ""},
		{"GET", "/api/kinds?source=log", ""},
		{"GET", "/api/record?seq=1", ""},
		{"GET", "/api/trace?trace=0af7651916cd43dd8448eb211c80319c", ""},
		{"GET", "/api/trace?span=a000000000000001", ""},
		{"GET", "/api/aggregate?name=chat", ""},
		{"POST", "/api/query", `{"stream":"*","source":"log"}`},
	} {
		req, _ := http.NewRequest(c.method, srv.URL+c.path, strings.NewReader(c.body))
		req.Header.Set("Origin", srv.URL)
		resp, err := srv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusInternalServerError {
			t.Errorf("%s %s: %d, want 500", c.method, c.path, resp.StatusCode)
		}
	}
}

// TestSessionAnswersWhenCountFails: /api/session is how clients find serve
// (sock.Probe), so it answers 200 with serve's identity even when counting
// the records fails, and reports the failure in error.
func TestSessionAnswersWhenCountFails(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	st := store.New(store.ModeLive, time.Now())
	srv := httptest.NewServer(New(Options{Store: st, ConfigBase: t.TempDir(), PID: os.Getpid(), Port: 4380,
		Socket: "/tmp/x.sock", DataDir: "/tmp/data", Version: "v1", Started: time.Now()}))
	defer srv.Close()
	st.Close()
	resp, err := srv.Client().Get(srv.URL + "/api/session")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var got struct {
		PID      int        `json:"pid"`
		Port     int        `json:"port"`
		Socket   string     `json:"socket"`
		DataDir  string     `json:"dataDir"`
		Version  string     `json:"version"`
		Mode     string     `json:"mode"`
		RunStart *time.Time `json:"runStart"`
		Count    int        `json:"count"`
		Error    string     `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK || got.PID == 0 || got.Port == 0 || got.Error == "" {
		t.Fatalf("status %d, %+v", resp.StatusCode, got)
	}
	if got.Socket == "" || got.DataDir == "" || got.Version == "" || got.Mode == "" || got.RunStart == nil || got.Count != 0 {
		t.Errorf("identity incomplete: %+v", got)
	}
}
