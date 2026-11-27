package duckstore

import (
	"context"
	"database/sql"
	"strings"
	"testing"
)

func TestCheckReadOnly(t *testing.T) {
	g := newRig(t)
	ok := []string{
		`SELECT 1`,
		`WITH a AS (SELECT 1) SELECT * FROM a`,
		`FROM range(3)`,
		`DESCRIBE logs."app"`,
		`SHOW ALL TABLES`,
		`SUMMARIZE logs."app"`,
		`SELECT * FROM information_schema.columns`,
		`SELECT * FROM logs."http.server" JOIN spans."*" USING (seq)`,
		`SELECT * FROM duckdb_columns()`,
		`SELECT loghorn.this_run_id()`,
		`SELECT 1 UNION ALL SELECT 2`,
	}
	for _, q := range ok {
		if err := CheckReadOnly(context.Background(), g.e.DB(), q); err != nil {
			t.Errorf("%s: refused: %v", q, err)
		}
	}
	refused := map[string]string{
		`DELETE FROM x`:                             "only one query",
		`DROP VIEW logs."app"`:                      "only one query",
		`COPY (SELECT 1) TO 'x.csv'`:                "only one query",
		`ATTACH 'x.db'`:                             "only one query",
		`INSTALL httpfs`:                            "only one query",
		`SET threads=1`:                             "only one query",
		`SELECT 1; SELECT 2`:                        "only one query",
		`SELEC 1`:                                   "syntax error",
		`SELECT * FROM read_text('/etc/hosts')`:     "table function read_text",
		`SELECT * FROM query('SELECT 1')`:           "table function query",
		`FROM 'x.csv'`:                              `table "x.csv"`,
		`SELECT * FROM loghorn_db.main.records`:     `table "loghorn_db.main.records"`,
		`SELECT * FROM all_records`:                 `table "all_records"`,
		`SELECT * FROM (SELECT * FROM all_records)`: `table "all_records"`,
		`SELECT (SELECT max(_id) FROM all_records)`: `table "all_records"`,
		`SELECT * FROM logs."app" WHERE seq IN (SELECT _id FROM memory.main.all_records)`: `table "memory.main.all_records"`,
	}
	for q, want := range refused {
		err := CheckReadOnly(context.Background(), g.e.DB(), q)
		if err == nil {
			t.Errorf("%s: allowed", q)
			continue
		}
		if !strings.Contains(err.Error(), want) {
			t.Errorf("%s: error %q lacks %q", q, err, want)
		}
	}
}

func TestCheckReadOnlyPointsToTablesList(t *testing.T) {
	g := newRig(t)
	err := CheckReadOnly(context.Background(), g.e.DB(), `SELECT * FROM logs`)
	if err == nil || !strings.Contains(err.Error(), "SELECT * FROM loghorn.tables") {
		t.Fatalf("error %v", err)
	}
}

// TestCheckReadOnlyRefusesWritesInCTEs: DuckDB parses a data-modifying CTE
// as a SELECT; the guard must refuse it, decoy CTE names or not.
func TestCheckReadOnlyRefusesWritesInCTEs(t *testing.T) {
	g := newRig(t)
	for _, q := range []string{
		`WITH d AS (DELETE FROM records RETURNING _id) SELECT count(*) FROM d`,
		`WITH d AS (DELETE FROM records RETURNING _id) SELECT (WITH records AS (SELECT 1) SELECT 1), count(*) FROM d`,
		`WITH u AS (UPDATE _fields SET n = 0 RETURNING 1) SELECT (WITH _fields AS (SELECT 1) SELECT 1), count(*) FROM u`,
		`WITH i AS (INSERT INTO records DEFAULT VALUES RETURNING 1) SELECT * FROM i`,
	} {
		if err := CheckReadOnly(context.Background(), g.e.DB(), q); err == nil {
			t.Errorf("allowed: %s", q)
		}
	}
}

// TestCheckReadOnlyRefusesFileNamedCTEs: a CTE named like a file must not
// make a file read elsewhere in the query look like a CTE reference.
func TestCheckReadOnlyRefusesFileNamedCTEs(t *testing.T) {
	g := newRig(t)
	q := `SELECT * FROM (SELECT (WITH "/abs/secret.csv" AS (SELECT 1 a) SELECT a FROM "/abs/secret.csv") AS x), '/abs/secret.csv'`
	if err := CheckReadOnly(context.Background(), g.e.DB(), q); err == nil {
		t.Fatal("allowed")
	}
	if err := CheckReadOnly(context.Background(), g.e.DB(), `WITH errs AS (SELECT 1 AS n) SELECT n FROM errs`); err != nil {
		t.Fatalf("plain CTE refused: %v", err)
	}
}

// TestEngineHasNoOutsideAccess: even a query the guard missed cannot read
// files, reach the network or load extensions.
func TestEngineHasNoOutsideAccess(t *testing.T) {
	g := newRig(t)
	if _, err := g.e.DB().Exec(`SELECT * FROM read_text('/etc/hosts')`); err == nil {
		t.Error("read_text read /etc/hosts")
	}
	for _, s := range []string{"autoinstall_known_extensions", "autoload_known_extensions", "enable_external_access"} {
		if v := g.one(`SELECT current_setting('` + s + `')`); v != false {
			t.Errorf("%s = %v", s, v)
		}
	}
}

// TestReadOnlyQueryCannotWrite: the query runs in a read-only transaction,
// so a write the guard missed fails and changes nothing.
func TestReadOnlyQueryCannotWrite(t *testing.T) {
	g := newRig(t)
	g.add(logRec(t, "api", "app", ``))
	err := g.e.ReadOnly(context.Background(), func(conn *sql.Conn) error {
		_, err := conn.ExecContext(context.Background(), `WITH d AS (DELETE FROM memory.main.records RETURNING _id) SELECT count(*) FROM d`)
		return err
	})
	if err == nil || !strings.Contains(err.Error(), "read-only") {
		t.Fatalf("error %v", err)
	}
	if n := g.one(`SELECT count(*) FROM memory.main.records`); n != int64(1) {
		t.Fatalf("records after the refused DELETE: %v", n)
	}
}

// TestGuardCTEScope: a WITH name stands for its CTE only inside the query
// that defines it, so it cannot unlock an internal table elsewhere.
func TestGuardCTEScope(t *testing.T) {
	g := newRig(t)
	for _, q := range []string{
		`SELECT * FROM (WITH records AS (SELECT 1) SELECT 1), records`,
		`SELECT (WITH records AS (SELECT 1) SELECT 1), (SELECT count(*) FROM records)`,
		`WITH records AS (SELECT * FROM records) SELECT * FROM records`,
		`WITH a AS (SELECT * FROM b), b AS (SELECT 1) SELECT * FROM a`,
		`WITH RECURSIVE records AS (SELECT * FROM records UNION ALL SELECT * FROM records) SELECT 1`,
	} {
		if err := CheckReadOnly(context.Background(), g.e.DB(), q); err == nil {
			t.Errorf("allowed: %s", q)
		}
	}
	for _, q := range []string{
		`WITH a AS (SELECT 1) SELECT * FROM a`,
		`WITH a AS (SELECT 1) SELECT * FROM (SELECT * FROM a)`,
		`WITH a AS (SELECT 1), b AS (SELECT * FROM a) SELECT * FROM b`,
		`WITH a AS (SELECT 1) SELECT (SELECT count(*) FROM a), * FROM a WHERE 1 IN (FROM a)`,
		`WITH RECURSIVE r AS (SELECT 1 x UNION ALL SELECT x+1 FROM r WHERE x < 3) SELECT * FROM r`,
	} {
		if err := CheckReadOnly(context.Background(), g.e.DB(), q); err != nil {
			t.Errorf("%s: refused: %v", q, err)
		}
	}
}
