package web

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/jimbeveridge/loghorn/internal/export"
	"github.com/jimbeveridge/loghorn/internal/store"
)

// DefaultSQLLimit is how many rows a SQL answer has when the request names
// no limit.
const DefaultSQLLimit = 1000

// MaxSQLLimit is the most rows a SQL answer may ask for: serve holds every
// row, and the encoded answer, in memory outside DuckDB's memory limit.
const MaxSQLLimit = 100000

type sqlRequest struct {
	SQL    string `json:"sql"`
	Format string `json:"format"`
	Limit  int    `json:"limit"`
}

// sql answers POST /api/sql: one read-only query over the table model (see
// query.SQLHeader), its rows in format (default tsv), at most limit of them
// (default DefaultSQLLimit) with X-Loghorn-Note saying so when more matched.
// It is served on serve's unix socket only: the page never sends SQL.
func (s *server) sql(w http.ResponseWriter, r *http.Request) {
	if !onUnixSocket(r) {
		writeError(w, http.StatusForbidden, "SQL is answered only on serve's socket; use 'loghorn query -sql'")
		return
	}
	var req sqlRequest
	if err := decode(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad request: "+err.Error())
		return
	}
	if strings.TrimSpace(req.SQL) == "" {
		writeError(w, http.StatusBadRequest, "sql is required")
		return
	}
	f := export.TSV
	if req.Format != "" {
		var err error
		if f, err = export.ParseFormat(req.Format); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	switch {
	case req.Limit < 0:
		writeError(w, http.StatusBadRequest, "limit must be at least 1")
		return
	case req.Limit > MaxSQLLimit:
		writeError(w, http.StatusBadRequest, fmt.Sprintf("limit must be at most %d", MaxSQLLimit))
		return
	case req.Limit == 0:
		req.Limit = DefaultSQLLimit
	}
	res, err := s.o.Store.SQL(r.Context(), req.SQL, req.Limit)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			err = fmt.Errorf("the query ran longer than %v and was stopped", store.SQLTimeout)
		}
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	var b bytes.Buffer
	if err := export.WriteRows(&b, f, res.Columns, res.Rows); err != nil {
		code := http.StatusInternalServerError
		if errors.Is(err, export.ErrUnsupported) {
			code = http.StatusBadRequest
		}
		writeError(w, code, err.Error())
		return
	}
	w.Header().Set("Content-Type", f.ContentType())
	w.Header().Set("Cache-Control", "no-store")
	if res.Truncated {
		w.Header().Set("X-Loghorn-Note", fmt.Sprintf("showing the first %d rows; add a LIMIT or pass -limit for more", len(res.Rows)))
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(b.Bytes())
}
