package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/reissui/clex/internal/version"
)

func TestVersionCommand(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := run([]string{"version"}, &out, &errOut); code != 0 {
		t.Fatalf("exit code = %d, want 0 (stderr: %s)", code, errOut.String())
	}
	if got := strings.TrimSpace(out.String()); got != version.Version {
		t.Fatalf("version output = %q, want %q", got, version.Version)
	}
}

func TestHelpExitsZero(t *testing.T) {
	for _, arg := range []string{"help", ""} {
		var out, errOut bytes.Buffer
		var args []string
		if arg != "" {
			args = []string{arg}
		}
		if code := run(args, &out, &errOut); code != 0 {
			t.Fatalf("run(%q) exit = %d, want 0", arg, code)
		}
		if !strings.Contains(out.String(), "Usage:") {
			t.Fatalf("run(%q) output missing usage; got: %s", arg, out.String())
		}
	}
}

func TestUnknownCommand(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := run([]string{"bogus"}, &out, &errOut); code != 2 {
		t.Fatalf("unknown command exit = %d, want 2", code)
	}
	if !strings.Contains(errOut.String(), "unknown command") {
		t.Fatalf("expected error message; got: %s", errOut.String())
	}
}
