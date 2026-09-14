// Package sqlfmt pretty-prints SQL with sql-formatter
// (github.com/sql-formatter-org/sql-formatter), run in-process.
//
// sql-formatter is JavaScript. github.com/wasilibs/go-sql-formatter packages it
// with QuickJS as WebAssembly, but ships only a command-line tool — its runner is
// an internal package — so the .wasm is vendored here and driven through wazero
// the same way. Licenses for both projects sit alongside it.
//
// The formatter lays SQL out one clause and one select item per line, so its
// output is narrow on its own; it has no line-width setting to pass through.
package sqlfmt

import (
	"bytes"
	"context"
	"crypto/rand"
	_ "embed"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing/fstest"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/imports/wasi_snapshot_preview1"
	"github.com/tetratelabs/wazero/sys"
)

// From github.com/wasilibs/go-sql-formatter v15.8.2, internal/wasm.
//
//go:embed sql-formatter-cli.wasm
var wasmBytes []byte

var (
	compileOnce sync.Once
	runtime     wazero.Runtime
	compiled    wazero.CompiledModule
	compileErr  error
)

// compile builds the module once per process. It is the expensive half — around
// 200ms — and every Format after it only pays for instantiating QuickJS.
func compile() {
	ctx := context.Background()
	runtime = wazero.NewRuntime(ctx)
	wasi_snapshot_preview1.MustInstantiate(ctx, runtime)
	compiled, compileErr = runtime.CompileModule(ctx, wasmBytes)
}

// Format returns sql laid out by sql-formatter's generic SQL dialect, without a
// trailing newline. It errors on SQL the formatter cannot parse.
//
// It is slow — around 100ms a statement, plus the one-time compile on first use —
// so keep it off the UI goroutine. It is safe to call concurrently: each call is
// its own module instance.
func Format(sql string) (string, error) {
	compileOnce.Do(compile)
	if compileErr != nil {
		return "", fmt.Errorf("sqlfmt: compile: %w", compileErr)
	}

	// The CLI reads the statement from a file: given a non-terminal stdin that
	// isn't a real file, it prints its usage instead of formatting.
	fsys := fstest.MapFS{
		"in.sql":      {Data: []byte(sql)},
		"config.json": {Data: []byte(config)},
	}
	var stdout, stderr bytes.Buffer
	cfg := wazero.NewModuleConfig().
		WithName(""). // anonymous, so concurrent instances don't collide
		WithArgs("sql-formatter-cli", "--config", "config.json", "in.sql").
		WithStdout(&stdout).
		WithStderr(&stderr).
		WithSysNanotime().
		WithSysWalltime().
		WithSysNanosleep().
		WithRandSource(rand.Reader).
		WithFSConfig(wazero.NewFSConfig().WithFSMount(fsys, "/"))

	ctx := context.Background()
	mod, err := runtime.InstantiateModule(ctx, compiled, cfg)
	if mod != nil {
		defer mod.Close(ctx) // a clean return leaves the instance open
	}
	var exit *sys.ExitError
	failed := err != nil && !(errors.As(err, &exit) && exit.ExitCode() == 0)
	out := strings.TrimRight(stdout.String(), "\n")
	// A parse error exits cleanly: the message goes to stderr and nothing to
	// stdout, so empty output is the failure signal as much as the exit code.
	if failed || out == "" {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" && err != nil {
			msg = err.Error()
		}
		if msg == "" {
			msg = "no output"
		}
		return "", fmt.Errorf("sqlfmt: %s", msg)
	}
	return out, nil
}

// config selects the generic SQL dialect — loghorn doesn't know which database
// wrote a statement — and accepts every common bind-parameter style, which the
// generic dialect otherwise rejects as a parse error: ?, $1, ?1, :1, :name, @name.
var config = `{
  "language": "sql",
  "paramTypes": {"positional": true, "numbered": ["$", "?", ":"], "named": [":", "@"]}
}`
