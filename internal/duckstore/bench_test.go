package duckstore

import (
	"database/sql"
	"testing"
)

// BenchmarkDurationSQL: durationSQL over 1,000,000 single-unit integer
// strings, the shape most duration fields hold.
func BenchmarkDurationSQL(b *testing.B) {
	db, err := sql.Open("duckdb", "")
	if err != nil {
		b.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE d AS SELECT (i % 100000)::VARCHAR ||
		['ns', 'us', 'µs', 'ms', 's', 'm', 'h'][i % 7 + 1] AS s FROM range(1000000) t(i)`); err != nil {
		b.Fatal(err)
	}
	q := "SELECT sum(" + durationSQL("s") + ") FROM d"
	b.ResetTimer()
	for range b.N {
		var sum float64
		if err := db.QueryRow(q).Scan(&sum); err != nil {
			b.Fatal(err)
		}
		if sum <= 0 {
			b.Fatalf("sum %v", sum)
		}
	}
}
