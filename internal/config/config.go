// Package config reads loghorn's configuration file.
//
// The file is .config/loghorn/config.toml. One name serves both project and
// user-level configuration, because ~/.config/loghorn/config.toml is simply
// what the upward walk finds on reaching the home directory — there is no
// second mechanism and no separate project filename. The first file found
// wins entirely and nothing is merged across levels, so one file explains all
// of loghorn's configured behaviour and -config can name it.
package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/BurntSushi/toml"
)

// RelPath is the config file's path relative to each directory the walk tests.
const RelPath = ".config/loghorn/config.toml"

// Format selects how ingest frames a byte stream into records.
type Format string

const (
	FormatAuto Format = "auto"
	FormatJSON Format = "json"
	FormatYAML Format = "yaml"
	FormatText Format = "text"
)

// FieldNaming selects which spelling of a LogEntry field name is accepted.
type FieldNaming string

const (
	NamingAuto  FieldNaming = "auto"
	NamingCamel FieldNaming = "camel"
	NamingSnake FieldNaming = "snake"
)

// SeverityMode selects which encoding of a severity value is accepted.
type SeverityMode string

const (
	SeverityAuto    SeverityMode = "auto"
	SeverityString  SeverityMode = "string"
	SeverityNumeric SeverityMode = "numeric"
)

// Input is the [input] section: how a producer's bytes become entries.
type Input struct {
	Format      Format       `toml:"format"`
	FieldNaming FieldNaming  `toml:"field-naming"`
	Severity    SeverityMode `toml:"severity"`
}

// Config is the whole file. Later sections (saved filters, keybindings) become
// sibling fields here.
type Config struct {
	Input Input `toml:"input"`
}

// Default is the configuration used when no file is found: every key "auto".
func Default() Config {
	return Config{Input: Input{
		Format:      FormatAuto,
		FieldNaming: NamingAuto,
		Severity:    SeverityAuto,
	}}
}

// String renders the effective settings the way the file would spell them, for
// -config.
func (c Config) String() string {
	return fmt.Sprintf("[input]\nformat       = %q\nfield-naming = %q\nseverity     = %q\n",
		string(c.Input.Format), string(c.Input.FieldNaming), string(c.Input.Severity))
}

// Loader resolves the config file. Its fields exist so tests can aim the walk
// and the user-level stop at a temporary tree rather than the real working
// directory and the developer's own home.
type Loader struct {
	Dir  string       // directory the upward walk starts from
	XDG  string       // XDG_CONFIG_HOME; empty means unset
	Home string       // home directory; empty means unknown
	Warn func(string) // one call per unknown setting; nil discards them
}

// Load reads the configuration that applies to dir. The returned string is the
// path actually read, empty when no file was found and defaults were used.
func Load(dir string) (Config, string, error) {
	home, _ := os.UserHomeDir()
	return Loader{
		Dir:  dir,
		XDG:  os.Getenv("XDG_CONFIG_HOME"),
		Home: home,
		Warn: func(msg string) { fmt.Fprintln(os.Stderr, "loghorn:", msg) },
	}.Load()
}

// Load resolves and parses the configuration. See the package comment.
func (l Loader) Load() (Config, string, error) {
	path, data, err := l.find()
	if err != nil {
		return Config{}, "", err
	}
	if path == "" {
		return Default(), "", nil
	}
	cfg, err := l.parse(path, data)
	if err != nil {
		return Config{}, "", err
	}
	return cfg, path, nil
}

// find walks up from l.Dir returning the first config file's path and contents,
// then falls through to the user-level file. A file that exists but can't be
// read is an error rather than a miss: the nearest file decides, so one made
// unreadable by permissions must not be skipped in favour of a more distant
// one that might say something different. This mirrors logfile.SourceTree.
func (l Loader) find() (string, []byte, error) {
	dir, err := filepath.Abs(l.Dir)
	if err != nil {
		return "", nil, err
	}
	for {
		p := filepath.Join(dir, RelPath)
		switch data, err := os.ReadFile(p); {
		case err == nil:
			return p, data, nil
		case !errors.Is(err, fs.ErrNotExist):
			return "", nil, fmt.Errorf("%s: %w", p, err)
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return l.userLevel()
		}
		dir = parent
	}
}

// userLevel is the walk's final stop. Without it, personal defaults would
// apply only when the working directory happened to sit under the home
// directory: started from /opt/service the walk never passes through it. When
// the working directory is already under home, the walk has tested this path
// already and this stop changes nothing.
func (l Loader) userLevel() (string, []byte, error) {
	var p string
	switch {
	case l.XDG != "":
		p = filepath.Join(l.XDG, "loghorn", "config.toml")
	case l.Home != "":
		p = filepath.Join(l.Home, ".config", "loghorn", "config.toml")
	default:
		return "", nil, nil
	}
	switch data, err := os.ReadFile(p); {
	case err == nil:
		return p, data, nil
	case !errors.Is(err, fs.ErrNotExist):
		return "", nil, fmt.Errorf("%s: %w", p, err)
	}
	return "", nil, nil
}

// parse decodes one file over the defaults, so an absent key keeps its
// default, then warns about settings this binary doesn't know and rejects
// values it can't honour.
func (l Loader) parse(path string, data []byte) (Config, error) {
	cfg := Default()
	md, err := toml.Decode(string(data), &cfg)
	if err != nil {
		return Config{}, fmt.Errorf("%s: %w", path, err)
	}
	// An unknown section or key warns rather than fails, so an older binary
	// tolerates a file written for a newer one.
	for _, key := range md.Undecoded() {
		l.warn(fmt.Sprintf("%s: unknown setting %q, ignored", path, key.String()))
	}
	if err := cfg.validate(path); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func (l Loader) warn(msg string) {
	if l.Warn != nil {
		l.Warn(msg)
	}
}

// validate rejects a value outside a key's permitted set. Unlike an unknown
// key, an unknown value can't be ignored: the user asked for behaviour this
// binary doesn't have, and guessing which of the permitted values they meant
// would be worse than stopping.
func (c Config) validate(path string) error {
	for _, k := range []struct {
		key     string
		got     string
		allowed []string
	}{
		{"input.format", string(c.Input.Format), []string{"auto", "json", "yaml", "text"}},
		{"input.field-naming", string(c.Input.FieldNaming), []string{"auto", "camel", "snake"}},
		{"input.severity", string(c.Input.Severity), []string{"auto", "string", "numeric"}},
	} {
		if !slices.Contains(k.allowed, k.got) {
			return fmt.Errorf("%s: %s = %q is not one of %s",
				path, k.key, k.got, strings.Join(k.allowed, ", "))
		}
	}
	return nil
}
