package duckstore

import (
	"strconv"
	"strings"

	"github.com/jimbeveridge/loghorn/internal/schema"
)

// sqlType is the DuckDB type of a loghorn type (D-014, D-017).
func sqlType(t schema.Type) string {
	switch t {
	case schema.TInt:
		return "BIGINT"
	case schema.TFloat, schema.TDuration:
		return "DOUBLE"
	case schema.TBool:
		return "BOOLEAN"
	case schema.TTimestamp:
		return "TIMESTAMP_NS"
	}
	return "VARCHAR" // string, json, and an unset type
}

// norm treats an unset type as string, as query.lookupField does.
func norm(t schema.Type) schema.Type {
	if t == "" {
		return schema.TString
	}
	return t
}

// ident quotes an identifier.
func ident(s string) string { return `"` + strings.ReplaceAll(s, `"`, `""`) + `"` }

// lit quotes a string literal.
func lit(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }

// flit is a DOUBLE literal that reads back as exactly f.
func flit(f float64) string {
	return "CAST(" + lit(strconv.FormatFloat(f, 'g', -1, 64)) + " AS DOUBLE)"
}

func nullOf(t schema.Type) string { return "CAST(NULL AS " + sqlType(t) + ")" }

const (
	rfc3339Re = `^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}(\.[0-9]+)?(Z|[+-][0-9]{2}:[0-9]{2})$`
	durPart   = `([0-9]+\.?[0-9]*|\.[0-9]+)(ns|us|µs|μs|ms|s|m|h)`
)

// conv is the SQL giving schema.Convert's value for a value c of type from
// (a string, json.Number, bool or JSON text), read as field to. NULL means
// not convertible.
func conv(c string, from schema.Type, to schema.Field) string {
	from, t := norm(from), norm(to.Type)
	if from == t {
		if t == schema.TFloat || t == schema.TDuration {
			// JSON's 1e400 reads as infinity; schema.Convert finds it bad.
			return "(CASE WHEN isfinite(" + c + ") THEN " + c + " END)"
		}
		return c
	}
	num := from == schema.TInt || from == schema.TFloat
	switch t {
	case schema.TString, schema.TJSON:
		if from == schema.TJSON || from == schema.TString {
			return c
		}
		return "CAST(" + c + " AS VARCHAR)"
	case schema.TInt:
		switch {
		case from == schema.TFloat:
			return "(CASE WHEN " + c + " = trunc(" + c + ") AND abs(" + c + ") < 9.2e18 THEN CAST(" + c + " AS BIGINT) END)"
		case from == schema.TString:
			return "(CASE WHEN regexp_full_match(" + trimSQL(c) + ", '[+-]?[0-9]+') THEN TRY_CAST(" + trimSQL(c) + " AS BIGINT) END)"
		}
	case schema.TFloat:
		switch {
		case from == schema.TInt:
			return "CAST(" + c + " AS DOUBLE)"
		case from == schema.TString:
			return "(CASE WHEN isfinite(TRY_CAST(" + trimSQL(c) + " AS DOUBLE)) THEN TRY_CAST(" + trimSQL(c) + " AS DOUBLE) END)"
		}
	case schema.TBool:
		if from == schema.TString {
			return "(CASE WHEN " + trimSQL(c) + " IN ('1','t','T','TRUE','true','True') THEN true WHEN " + trimSQL(c) + " IN ('0','f','F','FALSE','false','False') THEN false END)"
		}
	case schema.TDuration:
		switch {
		case num:
			return "(CASE WHEN isfinite(CAST(" + c + " AS DOUBLE)) THEN CAST(" + c + " AS DOUBLE) END)"
		case from == schema.TString:
			return durationSQL(c)
		}
	case schema.TTimestamp:
		switch {
		case from == schema.TInt:
			return epochIntSQL(c, to.Unit)
		case from == schema.TFloat:
			return "(CASE WHEN " + c + " = trunc(" + c + ") AND abs(" + c + ") < 9.2e18 THEN " + epochIntSQL("CAST("+c+" AS BIGINT)", to.Unit) + " ELSE " + epochFloatSQL(c, to.Unit) + " END)"
		case from == schema.TString:
			s := trimSQL(c)
			return "(CASE WHEN regexp_full_match(" + s + ", " + lit(rfc3339Re) + ") THEN " + rfc3339SQL(s) + " END)"
		}
	}
	return nullOf(t)
}

// rfc3339SQL is an RFC 3339 string (already matched) as UTC TIMESTAMP_NS,
// keeping nanoseconds: TIMESTAMPTZ would round to microseconds.
func rfc3339SQL(s string) string {
	local := "TRY_CAST(replace(regexp_extract(" + s + ", '^(.*?)(Z|[+-][0-9]{2}:[0-9]{2})$', 1), 'T', ' ') AS TIMESTAMP_NS)"
	off := "regexp_extract(" + s + ", '(Z|[+-][0-9]{2}:[0-9]{2})$', 1)"
	secs := "(CASE WHEN " + off + " = 'Z' THEN 0 ELSE (CASE WHEN starts_with(" + off + ", '-') THEN -1 ELSE 1 END) * " +
		"(CAST(substr(" + off + ", 2, 2) AS INTEGER) * 3600 + CAST(substr(" + off + ", 5, 2) AS INTEGER) * 60) END)"
	return "(" + local + " - to_seconds(" + secs + "))"
}

// trimSQL is strings.TrimSpace of c. DuckDB's one-argument trim removes
// only ' '.
func trimSQL(c string) string { return "trim(" + c + ", " + lit(schema.Space) + ")" }

// durationSQL parses Go duration syntax or a bare number (seconds), as
// schema.ParseDuration does, to the same float64: time.ParseDuration's
// whole nanoseconds, then Duration.Seconds(). Each part is its integer
// times its unit, plus its fraction as Go adds it (the fraction's first 18
// or 19 digits, float64(f) * (float64(unit) / scale), truncated); the sum
// must fit an int64. Past that Go falls back to strconv.ParseFloat, which
// takes no unit. c is the untrimmed string: a value of one integer part
// (durOne) holds no space, so it is parsed as it stands, and only the
// rest pay for trimming.
func durationSQL(c string) string {
	s := trimSQL(c)
	unit := "(CASE regexp_extract(x, '[a-zµμ]+$') WHEN 'ns' THEN 1 WHEN 'us' THEN 1000 WHEN 'µs' THEN 1000 WHEN 'μs' THEN 1000" +
		" WHEN 'ms' THEN 1000000 WHEN 's' THEN 1000000000 WHEN 'm' THEN 60000000000 WHEN 'h' THEN 3600000000000 END)"
	whole := "ltrim(regexp_extract(x, '^[0-9]*'), '0')"
	frac := "regexp_extract(x, '\\.([0-9]*)', 1)"
	digits := "(CASE WHEN length(" + frac + ") >= 19 AND TRY_CAST(left(" + frac + ", 19) AS HUGEINT) <= 9223372036854775807" +
		" THEN left(" + frac + ", 19) ELSE left(" + frac + ", 18) END)"
	fracNs := "(CASE WHEN " + digits + " = '' THEN 0 ELSE CAST(trunc(CAST(" + digits + " AS DOUBLE) * (CAST(" + unit + " AS DOUBLE) / CAST('1e' || length(" + digits + ") AS DOUBLE))) AS HUGEINT) END)"
	part := "(CASE WHEN length(" + whole + ") <= 20 THEN CAST(coalesce(nullif(" + whole + ", ''), '0') AS HUGEINT) * " + unit + " + " + fracNs + " END)"
	parts := "list_transform(regexp_extract_all(" + s + ", " + lit(durPart) + ", 0), lambda x: " + part + ")"
	limit := "(CASE WHEN starts_with(" + s + ", '-') THEN CAST(9223372036854775808 AS HUGEINT) ELSE CAST(9223372036854775807 AS HUGEINT) END)"
	secs := "(CAST(d // 1000000000 AS DOUBLE) + CAST(d % 1000000000 AS DOUBLE) / CAST(1e9 AS DOUBLE))"
	dur := "list_transform([" + parts + "], lambda p: list_transform([list_sum(p)], lambda d:" +
		" CASE WHEN len(list_filter(p, lambda v: v IS NULL)) = 0 AND d <= " + limit +
		" THEN (CASE WHEN starts_with(" + s + ", '-') THEN CAST(0 AS DOUBLE) - " + secs + " ELSE " + secs + " END) END)[1])[1]"
	return "(CASE WHEN regexp_full_match(" + c + ", " + lit(durOne) + ") THEN " + durOneSQL(c) +
		" WHEN " + s + " IN ('0', '+0', '-0') THEN CAST(0 AS DOUBLE)" +
		" WHEN regexp_full_match(" + s + ", " + lit(`[-+]?(`+durPart+`)+`) + ") THEN " + dur +
		" WHEN isfinite(TRY_CAST(" + s + " AS DOUBLE)) THEN TRY_CAST(" + s + " AS DOUBLE) END)"
}

// durOne is a duration of one integer part, the common case, which
// durOneSQL parses without the general path's lists and lambdas. 19
// digits always fit a HUGEINT times the largest unit; longer strings take
// the general path.
const durOne = `[-+]?[0-9]{1,19}(ns|us|µs|μs|ms|s|m|h)`

// durOneSQL is durationSQL's answer for s matching durOne: the digits
// times the unit, NULL past the general path's limit (int64 overflow, as
// Go refuses), then the general path's seconds, negated for a leading '-'.
func durOneSQL(s string) string {
	limit := "(CASE WHEN starts_with(" + s + ", '-') THEN CAST(9223372036854775808 AS HUGEINT) ELSE CAST(9223372036854775807 AS HUGEINT) END)"
	unit := "(CASE right(" + s + ", 2) WHEN 'ns' THEN 1 WHEN 'us' THEN 1000 WHEN 'µs' THEN 1000 WHEN 'μs' THEN 1000" +
		" WHEN 'ms' THEN 1000000 ELSE (CASE right(" + s + ", 1) WHEN 's' THEN 1000000000 WHEN 'm' THEN 60000000000" +
		" WHEN 'h' THEN 3600000000000 END) END)"
	d := "(CAST(ltrim(rtrim(" + s + ", 'nsuµμmh'), '+-') AS HUGEINT) * " + unit + ")"
	secs := "(CAST(" + d + " // 1000000000 AS DOUBLE) + CAST(" + d + " % 1000000000 AS DOUBLE) / CAST(1e9 AS DOUBLE))"
	return "(CASE WHEN " + d + " <= " + limit + " THEN (CASE WHEN starts_with(" + s + ", '-') THEN CAST(0 AS DOUBLE) - " +
		secs + " ELSE " + secs + " END) END)"
}

// epochIntSQL is schema's epoch() for an integer in unit (default ms).
func epochIntSQL(c, unit string) string {
	mul, limit := "1000000", "9223372036854"
	switch unit {
	case schema.UnitS:
		mul, limit = "1000000000", "9223372036"
	case schema.UnitUs:
		mul, limit = "1000", "9223372036854775"
	case schema.UnitNs:
		return "make_timestamp_ns(" + c + ")"
	}
	return "(CASE WHEN abs(" + c + ") <= " + limit + " THEN make_timestamp_ns(" + c + " * " + mul + ") END)"
}

// epochFloatSQL is schema's epoch() for a non-integer number in unit.
func epochFloatSQL(c, unit string) string {
	per := "1e6"
	switch unit {
	case schema.UnitS:
		per = "1e9"
	case schema.UnitUs:
		per = "1e3"
	case schema.UnitNs:
		per = "1e0"
	}
	ns := "(" + c + " * " + per + ")"
	return "(CASE WHEN abs(" + ns + ") < 9.2e18 THEN make_timestamp_ns(CAST(trunc(" + ns + ") AS BIGINT)) END)"
}
