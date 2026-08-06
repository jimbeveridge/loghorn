package headless

import (
	"bytes"
	"strings"
	"testing"
)

func TestRunEmitsOnlyImportant(t *testing.T) {
	input := strings.Join([]string{
		`{"severity":"INFO","textPayload":"routine"}`,
		`{"severity":"ERROR","textPayload":"boom"}`,
		`plain benign line`,
		`{"httpRequest":{"status":200}}`,
		`{"httpRequest":{"status":500},"textPayload":"down"}`,
		`something with a Traceback in it`,
	}, "\n") + "\n"

	var out bytes.Buffer
	if err := Run(strings.NewReader(input), &out); err != nil {
		t.Fatalf("Run error: %v", err)
	}
	got := out.String()

	mustContain := []string{`"textPayload":"boom"`, `"textPayload":"down"`, "Traceback"}
	for _, s := range mustContain {
		if !strings.Contains(got, s) {
			t.Errorf("output missing important line %q\ngot:\n%s", s, got)
		}
	}
	if strings.Contains(got, "routine") || strings.Contains(got, "benign") {
		t.Errorf("output leaked a routine line\ngot:\n%s", got)
	}
}
