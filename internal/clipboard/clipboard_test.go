package clipboard

import (
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// The tests exercise tool selection rather than Copy: copying for real would
// clobber whatever the person running the tests has on their clipboard.

func TestToolPicksThePlatformHelper(t *testing.T) {
	want := map[string]string{"darwin": "pbcopy", "windows": "clip"}[runtime.GOOS]
	if want == "" {
		t.Skipf("no single expected helper on %s", runtime.GOOS)
	}
	if _, err := exec.LookPath(want); err != nil {
		t.Skipf("%s not installed", want)
	}
	argv, err := tool(runtime.GOOS)
	if err != nil {
		t.Fatal(err)
	}
	if got := filepath.Base(argv[0]); got != want {
		t.Fatalf("on %s want %s, got %v", runtime.GOOS, want, argv)
	}
}

// The helper is resolved to an absolute path, so Copy does not re-search PATH.
func TestToolReturnsAnAbsolutePath(t *testing.T) {
	argv, err := tool(runtime.GOOS)
	if err != nil {
		t.Skipf("no clipboard tool on this machine: %v", err)
	}
	if !filepath.IsAbs(argv[0]) {
		t.Fatalf("want an absolute path, got %q", argv[0])
	}
}

// Linux keeps its flags, and prefers Wayland's tool — an X helper under Wayland
// can target a different session than the one the user is looking at.
func TestLinuxCandidateOrderAndFlags(t *testing.T) {
	got := tools["linux"]
	if len(got) < 3 || got[0][0] != "wl-copy" {
		t.Fatalf("wl-copy should be tried first, got %v", got)
	}
	if strings.Join(got[1], " ") != "xclip -selection clipboard" {
		t.Fatalf("xclip needs the clipboard selection flag, got %v", got[1])
	}
	if strings.Join(got[2], " ") != "xsel --clipboard --input" {
		t.Fatalf("xsel needs its clipboard flags, got %v", got[2])
	}
}

// An unknown platform is an error, not a panic or a silent no-op.
func TestUnknownPlatformErrors(t *testing.T) {
	_, err := tool("plan9")
	if err == nil {
		t.Fatalf("expected an error for an unsupported platform")
	}
	if !strings.Contains(err.Error(), "plan9") {
		t.Fatalf("the error should name the platform, got %v", err)
	}
}

// A platform whose helpers are all absent reports which ones it looked for, so
// the message tells you what to install.
func TestMissingToolNamesCandidates(t *testing.T) {
	orig := tools
	t.Cleanup(func() { tools = orig })
	tools = map[string][][]string{"testos": {{"loghorn-no-such-tool-a"}, {"loghorn-no-such-tool-b"}}}

	_, err := tool("testos")
	if err == nil {
		t.Fatalf("expected an error when no helper is installed")
	}
	for _, want := range []string{"loghorn-no-such-tool-a", "loghorn-no-such-tool-b"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error should name %q, got %v", want, err)
		}
	}
}
