package duckstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// querier is a *sql.DB or *sql.Conn.
type querier interface {
	QueryRowContext(ctx context.Context, q string, args ...any) *sql.Row
}

// queryNodes are the query node types a read may contain: a data-modifying
// CTE (DELETE_QUERY_NODE and the like) parses as a SELECT, so every other
// node type is refused.
var queryNodes = map[string]bool{
	"SELECT_NODE": true, "SET_OPERATION_NODE": true, "RECURSIVE_CTE_NODE": true, "CTE_NODE": true,
}

// plainName is what a CTE may be called: a name that can never be read as
// a file (a replacement scan), so a CTE cannot disguise a file read.
var plainName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// allowedTableFunctions are the table functions SQL from the command line
// may call besides the duckdb_* catalog functions: none of them reads or
// writes files or runs other SQL.
var allowedTableFunctions = map[string]bool{
	"range": true, "generate_series": true, "unnest": true, "json_each": true, "json_tree": true,
}

// modelSchemas are the schemas a query may read tables from: the table model
// and DuckDB's catalog views.
var modelSchemas = map[string]bool{
	"logs": true, "spans": true, "loghorn": true, "information_schema": true, "pg_catalog": true,
}

// CheckReadOnly lets q through only when DuckDB's parser reads it as exactly
// one query (SELECT, WITH, FROM-first, DESCRIBE, SHOW, SUMMARIZE or a set
// operation of them) whose tables are the table model's or DuckDB's catalog
// views and whose table functions read no files. Everything else, including
// writes, ATTACH, INSTALL, SET, COPY and several statements, is refused.
func CheckReadOnly(ctx context.Context, db querier, q string) error {
	var text string
	if err := db.QueryRowContext(ctx, "SELECT CAST(json_serialize_sql(?) AS VARCHAR)", q).Scan(&text); err != nil {
		return err
	}
	var tree struct {
		Error      bool              `json:"error"`
		ErrorType  string            `json:"error_type"`
		Message    string            `json:"error_message"`
		Statements []json.RawMessage `json:"statements"`
	}
	if err := json.Unmarshal([]byte(text), &tree); err != nil {
		return err
	}
	if tree.Error && tree.ErrorType == "parser" {
		return errors.New(tree.Message)
	}
	if tree.Error || len(tree.Statements) != 1 {
		return errors.New("only one query (SELECT, WITH, FROM, DESCRIBE, SHOW or SUMMARIZE) is allowed; it reads the logs and spans tables")
	}
	var node any
	if err := json.Unmarshal(tree.Statements[0], &node); err != nil {
		return err
	}
	return checkRefs(node, nil)
}

// checkRefs refuses a table function or table q may not read. ctes are the
// WITH names in scope at x: a name stands for its CTE only inside the query
// node whose WITH defines it (and that node's children), and within the WITH
// clause only in the CTEs after it, so a WITH name can never unlock a table
// elsewhere in the query.
func checkRefs(x any, ctes map[string]bool) error {
	switch v := x.(type) {
	case map[string]any:
		typ, _ := v["type"].(string)
		if strings.HasSuffix(typ, "_NODE") && !queryNodes[typ] {
			return fmt.Errorf("%s is not allowed: -sql only reads", strings.TrimSuffix(strings.TrimSuffix(typ, "_NODE"), "_QUERY"))
		}
		switch typ {
		case "TABLE_FUNCTION":
			f, _ := v["function"].(map[string]any)
			name, _ := f["function_name"].(string)
			if !allowedTableFunctions[strings.ToLower(name)] && !strings.HasPrefix(strings.ToLower(name), "duckdb_") {
				return fmt.Errorf("table function %s is not allowed: read the logs and spans tables", name)
			}
		case "BASE_TABLE":
			catalog, _ := v["catalog_name"].(string)
			sch, _ := v["schema_name"].(string)
			table, _ := v["table_name"].(string)
			ok := (catalog == "" || strings.EqualFold(catalog, "memory")) && modelSchemas[strings.ToLower(sch)]
			if catalog == "" && sch == "" && ctes[strings.ToLower(table)] {
				ok = true
			}
			if !ok {
				name := table
				if sch != "" {
					name = sch + "." + name
				}
				if catalog != "" {
					name = catalog + "." + name
				}
				return fmt.Errorf("table %q is not allowed: read logs.\"<stream>\" or spans.\"<stream>\" (SELECT * FROM loghorn.tables lists them)", name)
			}
		case "RECURSIVE_CTE_NODE":
			// The recursive part reads the CTE itself; the anchor (left)
			// does not.
			name, _ := v["cte_name"].(string)
			inner := withName(ctes, name)
			for k, c := range v {
				scope := inner
				if k == "left" {
					scope = ctes
				}
				if err := checkRefs(c, scope); err != nil {
					return err
				}
			}
			return nil
		}
		if m, ok := v["cte_map"].(map[string]any); ok {
			list, _ := m["map"].([]any)
			for _, e := range list {
				e, _ := e.(map[string]any)
				k, _ := e["key"].(string)
				if !plainName.MatchString(k) {
					return fmt.Errorf("WITH name %q is not allowed: use a plain name (letters, digits, _)", k)
				}
				if err := checkRefs(e["value"], ctes); err != nil {
					return err
				}
				ctes = withName(ctes, k)
			}
		}
		for k, c := range v {
			if k == "cte_map" {
				continue
			}
			if err := checkRefs(c, ctes); err != nil {
				return err
			}
		}
	case []any:
		for _, c := range v {
			if err := checkRefs(c, ctes); err != nil {
				return err
			}
		}
	}
	return nil
}

// withName is ctes and name, leaving ctes as it was.
func withName(ctes map[string]bool, name string) map[string]bool {
	out := make(map[string]bool, len(ctes)+1)
	for k := range ctes {
		out[k] = true
	}
	out[strings.ToLower(name)] = true
	return out
}
