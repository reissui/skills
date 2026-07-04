package main

import (
	"bytes"
	"context"
	"errors"
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

// TestResolveGitHubToken covers the issue #40 fix-2 daemon fallback: env tokens
// win in order, and with neither set the daemon falls back to the gh CLI so
// authenticating gh alone is sufficient.
func TestResolveGitHubToken(t *testing.T) {
	ghCalled := false
	ghFallback := func(context.Context) (string, error) {
		ghCalled = true
		return "gho_fromCLI", nil
	}

	t.Run("GITHUB_TOKEN wins", func(t *testing.T) {
		ghCalled = false
		env := map[string]string{"GITHUB_TOKEN": "ghp_env", "GH_TOKEN": "ghp_other"}
		tok, err := resolveGitHubToken(func(k string) string { return env[k] }, ghFallback)
		if err != nil || tok != "ghp_env" {
			t.Fatalf("got (%q, %v), want ghp_env", tok, err)
		}
		if ghCalled {
			t.Fatal("gh fallback should not run when GITHUB_TOKEN is set")
		}
	})

	t.Run("GH_TOKEN fallback before gh", func(t *testing.T) {
		ghCalled = false
		env := map[string]string{"GH_TOKEN": "ghp_gh"}
		tok, err := resolveGitHubToken(func(k string) string { return env[k] }, ghFallback)
		if err != nil || tok != "ghp_gh" {
			t.Fatalf("got (%q, %v), want ghp_gh", tok, err)
		}
		if ghCalled {
			t.Fatal("gh fallback should not run when GH_TOKEN is set")
		}
	})

	t.Run("falls back to gh CLI when no env token", func(t *testing.T) {
		ghCalled = false
		tok, err := resolveGitHubToken(func(string) string { return "" }, ghFallback)
		if err != nil || tok != "gho_fromCLI" {
			t.Fatalf("got (%q, %v), want gho_fromCLI", tok, err)
		}
		if !ghCalled {
			t.Fatal("expected gh fallback to run when no env token is set")
		}
	})

	t.Run("actionable error when nothing resolves", func(t *testing.T) {
		failing := func(context.Context) (string, error) { return "", errors.New("gh not logged in") }
		_, err := resolveGitHubToken(func(string) string { return "" }, failing)
		if err == nil || !strings.Contains(err.Error(), "gh auth login") {
			t.Fatalf("expected an actionable gh-auth error, got %v", err)
		}
	})
}
