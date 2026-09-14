package adapter

import (
	"bytes"

	"github.com/jimbeveridge/loghorn/internal/entry"
	"gopkg.in/yaml.v3"
)

// YAMLAdapter parses gcloud logging read's `--format=yaml` output: one
// LogEntry per document. ingest.Records already splits the stream into
// per-document records on the "---" separator between them, keeping each
// record's leading separator, so Detect only needs to check for that prefix.
type YAMLAdapter struct{}

func (YAMLAdapter) Detect(rec []byte) bool {
	t := bytes.TrimSpace(rec)
	return len(t) >= 2 && t[0] == '-' && t[1] == '-'
}

func (YAMLAdapter) Parse(rec []byte) (entry.Entry, error) {
	var obj map[string]any
	if err := yaml.Unmarshal(rec, &obj); err != nil {
		return entry.Entry{}, err
	}
	return fromLogEntryObject(rec, normalizeYAML(obj).(map[string]any)), nil
}

// normalizeYAML aligns yaml.v3's decoded types with encoding/json's, so every
// downstream consumer — HTTPStatus's type assertion, the detail pane's number
// colouring — treats an entry the same regardless of source format. Plain
// YAML integers decode as int (json.Unmarshal always produces float64);
// everything else already matches.
func normalizeYAML(v any) any {
	switch t := v.(type) {
	case map[string]any:
		for k, val := range t {
			t[k] = normalizeYAML(val)
		}
		return t
	case []any:
		for i, val := range t {
			t[i] = normalizeYAML(val)
		}
		return t
	case int:
		return float64(t)
	case int64:
		return float64(t)
	case uint64:
		return float64(t)
	default:
		return v
	}
}
