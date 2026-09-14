package sqlfmt

import (
	"strings"
	"sync"
	"testing"
)

const oneLine = "SELECT u.id, u.email FROM users u WHERE u.created_at > $1 AND u.region IN (SELECT r.id FROM regions r) ORDER BY u.id LIMIT 50"

// A one-line statement comes back laid out a clause to a line, with nothing lost.
func TestFormatLaysOutClauses(t *testing.T) {
	got, err := Format(oneLine)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(got, "\n")
	for _, want := range []string{"SELECT", "FROM", "WHERE", "ORDER BY", "LIMIT"} {
		found := false
		for _, ln := range lines {
			if strings.TrimSpace(ln) == want {
				found = true
			}
		}
		if !found {
			t.Errorf("no line holding just %q:\n%s", want, got)
		}
	}
	if strings.Join(strings.Fields(got), "") != strings.Join(strings.Fields(oneLine), "") {
		t.Fatalf("formatting changed the statement's text:\n%s", got)
	}
	if strings.HasSuffix(got, "\n") {
		t.Fatalf("output should not end in a newline: %q", got)
	}
}

// SQL the formatter cannot parse is an error, not a silently mangled statement.
func TestFormatReportsParseErrors(t *testing.T) {
	if got, err := Format("SELECT 'unterminated FROM t"); err == nil {
		t.Fatalf("want an error, got:\n%s", got)
	}
}

// Instances don't share state: concurrent calls each get their own statement back.
func TestFormatIsSafeConcurrently(t *testing.T) {
	stmts := []string{"SELECT a FROM t1", "SELECT b FROM t2", "SELECT c FROM t3", "SELECT d FROM t4"}
	var wg sync.WaitGroup
	for _, s := range stmts {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got, err := Format(s)
			if err != nil {
				t.Error(err)
				return
			}
			if strings.Join(strings.Fields(got), " ") != s {
				t.Errorf("Format(%q) = %q", s, got)
			}
		}()
	}
	wg.Wait()
}
