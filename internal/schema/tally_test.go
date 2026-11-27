package schema_test

import (
	"encoding/json"
	"testing"

	"github.com/jimbeveridge/loghorn/internal/schema"
)

func TestTallyType(t *testing.T) {
	int1 := schema.Classify(jsonNumber("5"))
	flt := schema.Classify(jsonNumber("1.5"))
	str := schema.Classify("x")
	none := schema.Classify(nil)
	cases := []struct {
		path string
		k    schema.KindSet
		want schema.Type
	}{
		{"a", int1, schema.TInt},
		{"a", int1 | flt, schema.TFloat},
		{"a", int1 | str, schema.TString},
		{"a", none, schema.TString},
		{"a", int1 | none, schema.TInt},
		{"timestamp", int1, schema.TTimestamp},
		{"receiveTimestamp", str, schema.TTimestamp},
	}
	for _, c := range cases {
		if got := schema.TallyType(c.path, c.k); got != c.want {
			t.Errorf("TallyType(%s, %b) = %s, want %s", c.path, c.k, got, c.want)
		}
	}
}

func jsonNumber(s string) any { return json.Number(s) }
