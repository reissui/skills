package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/reissui/clex/internal/version"
)

// runCLI is a test helper: it builds a minimal env writing to the returned
// buffers and dispatches through run, so top-level command routing is exercised
// exactly as main() would.
func runCLI(t *testing.T, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	var out, errOut bytes.Buffer
	e := newTestEnv(t)
	e.stdout = &out
	e.stderr = &errOut
	code = run(e, args)
	return code, out.String(), errOut.String()
}

func TestVersionCommand(t *testing.T) {
	code, out, errOut := runCLI(t, "version")
	if code != 0 {
		t.Fatalf("exit code = %d, want 0 (stderr: %s)", code, errOut)
	}
	if got := strings.TrimSpace(out); got != version.Version {
		t.Fatalf("version output = %q, want %q", got, version.Version)
	}
}

func TestHelpExitsZero(t *testing.T) {
	for _, arg := range []string{"help", ""} {
		var args []string
		if arg != "" {
			args = []string{arg}
		}
		code, out, _ := runCLI(t, args...)
		if code != 0 {
			t.Fatalf("run(%q) exit = %d, want 0", arg, code)
		}
		if !strings.Contains(out, "Usage:") {
			t.Fatalf("run(%q) output missing usage; got: %s", arg, out)
		}
	}
}

func TestUnknownCommand(t *testing.T) {
	code, _, errOut := runCLI(t, "bogus")
	if code != 2 {
		t.Fatalf("unknown command exit = %d, want 2", code)
	}
	if !strings.Contains(errOut, "unknown command") {
		t.Fatalf("expected error message; got: %s", errOut)
	}
}

// TestEveryCommandHasHelp asserts that each registered command responds to
// --help with exit 0 and prints its own usage (acceptance: every command exists
// with help text).
func TestEveryCommandHasHelp(t *testing.T) {
	for name := range commands {
		code, _, errOut := runCLI(t, name, "--help")
		if code != 0 {
			t.Errorf("%s --help exit = %d, want 0 (stderr: %s)", name, code, errOut)
		}
	}
}
