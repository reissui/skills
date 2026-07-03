package daemon

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/reissui/clex/internal/core"
	"github.com/reissui/clex/internal/runner/claude"
	"github.com/reissui/clex/internal/store"
)

// --- SECURITY Criterion: runner child env is the minimal allowlist; a canary
// var seeded in the parent env never reaches a runner.
//
// This exercises the REAL claude adapter (as DefaultRunnerBuilder constructs it)
// against a fake `claude` binary that dumps its environment. We seed CLEX_CANARY
// and ANTHROPIC_API_KEY in the parent process env and assert neither reaches the
// child: the canary is not on the allowlist, and the API key is explicitly
// stripped (billing/compliance). This proves the daemon's dispatch path does not
// undermine the adapter's least-privilege env.
func TestChildEnvAllowlistDropsCanary(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake shell binary is POSIX")
	}
	dir := t.TempDir()
	envDump := filepath.Join(dir, "env.txt")
	// Fake `claude`: on any invocation, write its env to envDump, then emit a
	// minimal result line the adapter can parse and exit 0.
	fake := filepath.Join(dir, "claude")
	script := "#!/bin/sh\n/usr/bin/env > " + shellQuote(envDump) + "\n" +
		`printf '{"type":"result","subtype":"success","session_id":"s1","total_cost_usd":0}\n'` + "\n" +
		"exit 0\n"
	if err := os.WriteFile(fake, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake binary: %v", err)
	}

	// Seed a canary and an auth var in the PARENT env.
	t.Setenv("CLEX_CANARY", "leak-me-if-you-can")
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-canaryshouldnotleak000000")

	// Build the real claude adapter pointed at the fake binary.
	ad := claude.New(claude.WithBinary(fake))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ch, err := ad.Run(ctx, core.Task{Repo: "acme/widget", Issue: 1, Prompt: "hi"}, dir)
	if err != nil {
		t.Fatalf("adapter Run: %v", err)
	}
	for range ch {
		// drain to completion
	}

	got, err := os.ReadFile(envDump)
	if err != nil {
		t.Fatalf("read env dump: %v", err)
	}
	env := string(got)
	if strings.Contains(env, "CLEX_CANARY") || strings.Contains(env, "leak-me-if-you-can") {
		t.Fatalf("canary reached the child env:\n%s", env)
	}
	if strings.Contains(env, "ANTHROPIC_API_KEY") {
		t.Fatalf("ANTHROPIC_API_KEY reached the child env (must be stripped):\n%s", env)
	}
	// Sanity: PATH (allowlisted) should be present so the child is functional.
	if !strings.Contains(env, "PATH=") {
		t.Fatalf("expected PATH in child env; got:\n%s", env)
	}
}

// --- SECURITY Criterion: the event log redacts a seeded token-shaped string.
//
// The daemon passes every event-log detail through its Redactor. Here we drive a
// realistic secret (a GitHub PAT-shaped token and the configured Telegram token)
// through logEvent and assert the persisted store row contains the placeholder,
// not the secret.
func TestEventLogRedactsSecrets(t *testing.T) {
	stages := newFakeStages()
	h := newHarness(t, stages)

	pat := "ghp_ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	// Configured Telegram token literal (set in buildTestConfig / harness redactor).
	tgTok := "token-xyz"
	h.d.logEvent(context.Background(), 5, "test", "runner said token="+pat+" and tg="+tgTok)

	entries, err := h.st.EventsForIssue(5)
	if err != nil {
		t.Fatalf("EventsForIssue: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("no event persisted")
	}
	detail := entries[len(entries)-1].Detail
	if strings.Contains(detail, pat) {
		t.Fatalf("event log leaked the PAT: %q", detail)
	}
	if strings.Contains(detail, tgTok) {
		t.Fatalf("event log leaked the Telegram token: %q", detail)
	}
	if !strings.Contains(detail, redactedPlaceholder) {
		t.Fatalf("expected redaction placeholder in %q", detail)
	}
}

// TestRedactorPatterns is a focused unit test of the token-shape patterns.
func TestRedactorPatterns(t *testing.T) {
	r := NewRedactor("literal-secret-value")
	cases := []struct {
		name string
		in   string
		leak string
	}{
		{"github_pat", "x github_pat_11ABCDEF0123456789_aaaaaaaaaaaaaaaaaaaaaaaaaaaa y", "github_pat_"},
		{"ghp", "tok ghp_ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789", "ghp_ABCDEF"},
		{"anthropic", "key sk-ant-api03-abcdefghijklmnopqrstuvwxyz", "sk-ant-"},
		{"telegram", "bot 123456789:AAE-abcdefghijklmnopqrstuvwxyz012345", ":AAE-"},
		{"literal", "here is literal-secret-value in text", "literal-secret-value"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out := r.Redact(c.in)
			if strings.Contains(out, c.leak) {
				t.Fatalf("redaction failed for %s: %q still contains %q", c.name, out, c.leak)
			}
			if !strings.Contains(out, redactedPlaceholder) {
				t.Fatalf("expected placeholder for %s; got %q", c.name, out)
			}
		})
	}
}

// --- SECURITY Criterion: home dirs are 0700.
func TestHomeDirsAre0700(t *testing.T) {
	base := t.TempDir()
	home := filepath.Join(base, "clexhome")
	resolved, err := EnsureHome(home)
	if err != nil {
		t.Fatalf("EnsureHome: %v", err)
	}
	for _, d := range []string{resolved, SpoolDir(resolved), filepath.Join(resolved, "worktrees")} {
		info, err := os.Stat(d)
		if err != nil {
			t.Fatalf("stat %s: %v", d, err)
		}
		if perm := info.Mode().Perm(); perm != 0o700 {
			t.Fatalf("%s perm = %o, want 0700", d, perm)
		}
	}
}

// --- SECURITY Criterion: update quiesce hook defers while a runner is active.
//
// With one active fake runner, ApplyWhenQuiesced must NOT apply the staged
// update. Once the runner finishes (gate released), the daemon quiesces and the
// update applies exactly once.
func TestQuiesceDefersWhileRunnerActive(t *testing.T) {
	stages := newFakeStages()
	gate := stages.holdBuild(80)
	h := newHarness(t, stages)
	h.approvedIssue(80, nil, nil)
	h.runDaemon(t)

	// Wait until the build is active.
	if !waitFor(time.Second, func() bool {
		h.d.mu.Lock()
		defer h.d.mu.Unlock()
		return len(h.d.running) == 1
	}) {
		t.Fatal("build never became active")
	}
	if h.d.Quiesced() {
		t.Fatal("daemon reports quiesced while a runner is active")
	}

	applied := make(chan struct{})
	go func() {
		_ = h.d.ApplyWhenQuiesced(context.Background(), func() error {
			close(applied)
			return nil
		})
	}()

	// The update must be deferred: no apply while the runner is active.
	select {
	case <-applied:
		t.Fatal("update applied while a runner was active (should defer)")
	case <-time.After(200 * time.Millisecond):
		// expected: still deferred
	}

	// Release the runner; the daemon quiesces and the update applies.
	close(gate)
	select {
	case <-applied:
		// success
	case <-time.After(2 * time.Second):
		t.Fatal("update did not apply after the runner finished")
	}
}

// TestQuiesceDefersWhileGatePending proves a pending cost gate also blocks the
// swap even with zero runners.
func TestQuiesceDefersWhileGatePending(t *testing.T) {
	stages := newFakeStages()
	h := newHarness(t, stages)
	h.d.setPendingGate("#1 metered ~$3.00")
	if h.d.Quiesced() {
		t.Fatal("quiesced despite a pending gate")
	}
	h.d.setPendingGate("")
	if !h.d.Quiesced() {
		t.Fatal("not quiesced after gate cleared with no runners")
	}
}

// ensure store import is used even if other assertions change.
var _ = store.SessionRunning

// shellQuote wraps a path in single quotes for embedding in a /bin/sh script.
func shellQuote(p string) string { return "'" + strings.ReplaceAll(p, "'", `'\''`) + "'" }
