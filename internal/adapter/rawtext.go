package adapter

import (
	"strings"

	"github.com/jimbeveridge/loghorn/internal/entry"
)

type RawTextAdapter struct{}

func (RawTextAdapter) Detect(line []byte) bool { return true }

func (RawTextAdapter) Parse(line []byte) (entry.Entry, error) {
	return entry.Entry{
		Raw:     append([]byte(nil), line...),
		Format:  entry.FormatRawText,
		Message: strings.TrimRight(string(line), "\r\n"),
	}, nil
}
