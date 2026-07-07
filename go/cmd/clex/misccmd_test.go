package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGCNoWorktreeRoot(t *testing.T) {
	e := newTestEnv(t) // home is a fresh temp dir; no worktrees/ subdir
	code := run(e, []string{"gc"})
	if code != exitOK {
		t.Fatalf("gc exit = %d, want 0 (stderr: %s)", code, errBuf(e))
	}
	if !strings.Contains(outBuf(e).String(), "no stale worktrees") {
		t.Fatalf("expected empty-root message; got:\n%s", outBuf(e))
	}
}

func TestGCSkipsNonWorktreeDirs(t *testing.T) {
	e := newTestEnv(t)
	// A plain directory (no .git) under the worktree root must be left untouched.
	root := e.worktreeRoot()
	plain := filepath.Join(root, "not-a-worktree")
	if err := os.MkdirAll(plain, 0o755); err != nil {
		t.Fatal(err)
	}
	code := run(e, []string{"gc"})
	if code != exitOK {
		t.Fatalf("gc exit = %d, want 0", code)
	}
	if _, err := os.Stat(plain); err != nil {
		t.Fatalf("gc removed a non-worktree dir: %v", err)
	}
	if !strings.Contains(outBuf(e).String(), "no stale worktrees") {
		t.Fatalf("expected no-stale message; got:\n%s", outBuf(e))
	}
}

func TestGCJSON(t *testing.T) {
	e := newTestEnv(t)
	code := run(e, []string{"gc", "--json"})
	if code != exitOK {
		t.Fatalf("gc --json exit = %d, want 0", code)
	}
	var got struct {
		OK     bool     `json:"ok"`
		Pruned []string `json:"pruned"`
	}
	if err := json.Unmarshal(outBuf(e).Bytes(), &got); err != nil {
		t.Fatalf("gc --json invalid: %v\n%s", err, outBuf(e))
	}
	if !got.OK {
		t.Fatalf("unexpected gc json: %+v", got)
	}
}

func TestUpdateStubNoOp(t *testing.T) {
	e := newTestEnv(t)
	code := run(e, []string{"update"})
	if code != exitOK {
		t.Fatalf("update exit = %d, want 0", code)
	}
	if !strings.Contains(outBuf(e).String(), "not wired") {
		t.Fatalf("expected 'not wired' stub message; got:\n%s", outBuf(e))
	}
}

func TestUpdateJSON(t *testing.T) {
	e := newTestEnv(t)
	code := run(e, []string{"update", "--json"})
	if code != exitOK {
		t.Fatalf("update --json exit = %d, want 0", code)
	}
	var got struct {
		OK    bool `json:"ok"`
		Wired bool `json:"wired"`
	}
	if err := json.Unmarshal(outBuf(e).Bytes(), &got); err != nil {
		t.Fatalf("update --json invalid: %v\n%s", err, outBuf(e))
	}
	if !got.OK || got.Wired {
		t.Fatalf("unexpected update json: %+v (want ok=true, wired=false)", got)
	}
}

func TestRepoFromRemote(t *testing.T) {
	cases := []struct {
		in       string
		wantRepo string
		wantOK   bool
	}{
		{"git@github.com:acme/widgets.git", "acme/widgets", true},
		{"https://github.com/acme/widgets.git", "acme/widgets", true},
		{"https://github.com/acme/widgets", "acme/widgets", true},
		{"ssh://git@github.com/acme/widgets.git", "acme/widgets", true},
		{"git@github.com:acme/widgets", "acme/widgets", true},
		{"", "", false},
		{"not-a-url", "", false},
	}
	for _, tc := range cases {
		got, ok := repoFromRemote(tc.in)
		if ok != tc.wantOK || got != tc.wantRepo {
			t.Errorf("repoFromRemote(%q) = %q,%v; want %q,%v", tc.in, got, ok, tc.wantRepo, tc.wantOK)
		}
	}
}

func TestParseIssueTarget(t *testing.T) {
	cases := []struct {
		in      string
		want    int
		wantErr bool
	}{
		{"14", 14, false},
		{"#14", 14, false},
		{"epic", 0, false},
		{"EPIC", 0, false},
		{"abc", 0, true},
	}
	for _, tc := range cases {
		got, err := parseIssueTarget(tc.in)
		if (err != nil) != tc.wantErr || got != tc.want {
			t.Errorf("parseIssueTarget(%q) = %d,%v; want %d,err=%v", tc.in, got, err, tc.want, tc.wantErr)
		}
	}
}
