package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/reissui/clex/internal/version"
)

func TestVersionFlag(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := run([]string{"--version"}, &out, &errOut); code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
	if got := strings.TrimSpace(out.String()); got != version.Version {
		t.Fatalf("version = %q, want %q", got, version.Version)
	}
}

func TestHelpFlagExitsZero(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := run([]string{"--help"}, &out, &errOut); code != 0 {
		t.Fatalf("--help exit = %d, want 0", code)
	}
	if !strings.Contains(errOut.String(), "Usage:") {
		t.Fatalf("--help missing usage; got: %s", errOut.String())
	}
}

func TestNoArgsPrintsUsage(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := run(nil, &out, &errOut); code != 0 {
		t.Fatalf("no-args exit = %d, want 0", code)
	}
	if !strings.Contains(out.String(), "clexd") {
		t.Fatalf("no-args output missing banner; got: %s", out.String())
	}
}
