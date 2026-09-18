package headless

import (
	"bytes"
	"errors"
	"io"
	"slices"
	"strings"
	"testing"

	"github.com/jimbeveridge/loghorn/internal/config"
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
	if err := Run(strings.NewReader(input), &out, config.Default().Input, nil); err != nil {
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

// The log file must see the whole stream, not just what the filter prints, and
// multi-line YAML records must arrive whole.
func TestRunPassesEveryRecordToSink(t *testing.T) {
	for _, tc := range []struct {
		name, input string
		want        []string
	}{
		{
			name:  "lines",
			input: "{\"severity\":\"INFO\",\"textPayload\":\"routine\"}\nplain benign line\n{\"severity\":\"ERROR\",\"textPayload\":\"boom\"}\n",
			want: []string{
				`{"severity":"INFO","textPayload":"routine"}`,
				`plain benign line`,
				`{"severity":"ERROR","textPayload":"boom"}`,
			},
		},
		{
			name:  "yaml",
			input: "---\nseverity: INFO\n---\nseverity: ERROR\n",
			want:  []string{"---\nseverity: INFO", "---\nseverity: ERROR"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var got []string
			sink := func(rec []byte) { got = append(got, string(rec)) }
			if err := Run(strings.NewReader(tc.input), io.Discard, config.Default().Input, sink); err != nil {
				t.Fatalf("Run error: %v", err)
			}
			if !slices.Equal(got, tc.want) {
				t.Fatalf("sink got %q, want %q", got, tc.want)
			}
		})
	}
}

// errWriter always fails, so Run's write to w errors on the first important
// record.
type errWriter struct{ err error }

func (w errWriter) Write(p []byte) (int, error) { return 0, w.err }

// A write failure must not stop records from reaching the sink: the log file
// (the sink) is meant to keep the whole stream even when the filtered
// stdout writer has failed. Run still reports the write error once done.
func TestRunKeepsFeedingSinkAfterWriteError(t *testing.T) {
	input := strings.Join([]string{
		`{"severity":"ERROR","textPayload":"first"}`,
		`{"severity":"ERROR","textPayload":"second"}`,
		`{"severity":"ERROR","textPayload":"third"}`,
	}, "\n") + "\n"
	want := []string{
		`{"severity":"ERROR","textPayload":"first"}`,
		`{"severity":"ERROR","textPayload":"second"}`,
		`{"severity":"ERROR","textPayload":"third"}`,
	}

	var got []string
	sink := func(rec []byte) { got = append(got, string(rec)) }
	wantErr := errors.New("write failed")

	err := Run(strings.NewReader(input), errWriter{wantErr}, config.Default().Input, sink)
	if !errors.Is(err, wantErr) {
		t.Fatalf("Run error = %v, want %v", err, wantErr)
	}
	if !slices.Equal(got, want) {
		t.Fatalf("sink got %q, want %q", got, want)
	}
}
