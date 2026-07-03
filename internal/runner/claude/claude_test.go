package claude

import (
	"bufio"
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/reissui/clex/internal/core"
)

// fixture returns the absolute path of a testdata file.
func fixture(t *testing.T, name string) string {
	t.Helper()
	abs, err := filepath.Abs(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("abs %s: %v", name, err)
	}
	return abs
}

// fakeConfig describes how the working-dir-driven fake binary should behave for
// a single run. All fields are optional.
type fakeConfig struct {
	fixture   string // fixture file the fake cats to stdout
	recordArg bool   // capture argv to .fake/argv-out
	recordEnv bool   // dump env to .fake/env-out
	exit      string // exit code (default 0)
}

// setupFake writes ./.fake/ control files into dir and returns the argv and env
// output paths (valid only when the corresponding record flag was set).
func setupFake(t *testing.T, dir string, cfg fakeConfig) (argvPath, envPath string) {
	t.Helper()
	fdir := filepath.Join(dir, ".fake")
	if err := os.MkdirAll(fdir, 0o700); err != nil {
		t.Fatalf("mkdir .fake: %v", err)
	}
	if cfg.fixture != "" {
		write(t, filepath.Join(fdir, "fixture"), cfg.fixture)
	}
	if cfg.recordArg {
		argvPath = filepath.Join(fdir, "argv-out")
		write(t, argvPath, "")
	}
	if cfg.recordEnv {
		envPath = filepath.Join(fdir, "env-out")
		write(t, envPath, "")
	}
	if cfg.exit != "" {
		write(t, filepath.Join(fdir, "exit"), cfg.exit)
	}
	return argvPath, envPath
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// collect drains a run channel into a slice, failing if it does not finish in
// time (a hung child would otherwise stall the whole suite).
func collect(t *testing.T, ch <-chan core.Event) []core.Event {
	t.Helper()
	var evs []core.Event
	timeout := time.After(10 * time.Second)
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				return evs
			}
			evs = append(evs, ev)
		case <-timeout:
			t.Fatal("run did not complete within timeout")
		}
	}
}

// TestRunStreamToEvents covers the primary path: a fixture stream-json file is
// parsed into ordered core.Events including usage tokens and the session id.
func TestRunStreamToEvents(t *testing.T) {
	dir := t.TempDir()
	setupFake(t, dir, fakeConfig{fixture: fixture(t, "stream-success.jsonl")})
	a := New(WithBinary(fixture(t, "fake-claude.sh")))

	ch, err := a.Run(context.Background(), core.Task{Prompt: "do it"}, dir)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	evs := collect(t, ch)

	// Expect, in order: text+usage (msg1), tool_use+usage (msg2),
	// text+usage (msg3), result. Each assistant line carries its own usage.
	wantTypes := []core.EventType{
		core.EventText, core.EventUsage,
		core.EventToolUse, core.EventUsage,
		core.EventText, core.EventUsage,
		core.EventResult,
	}
	if len(evs) != len(wantTypes) {
		t.Fatalf("got %d events, want %d: %+v", len(evs), len(wantTypes), evs)
	}
	for i, wt := range wantTypes {
		if evs[i].Type != wt {
			t.Errorf("event %d type = %q, want %q", i, evs[i].Type, wt)
		}
	}

	if evs[0].Text != "Let me inspect the file." {
		t.Errorf("first text = %q", evs[0].Text)
	}
	if evs[2].Text != "Read" {
		t.Errorf("tool_use name = %q, want Read", evs[2].Text)
	}
	if evs[1].Tokens != (core.Usage{In: 1200, Out: 8}) {
		t.Errorf("first usage = %+v, want {1200 8}", evs[1].Tokens)
	}

	last := evs[len(evs)-1]
	if last.Type != core.EventResult {
		t.Fatalf("terminal event type = %q", last.Type)
	}
	if last.SessionID != "sess-abc-123" {
		t.Errorf("session id = %q, want sess-abc-123", last.SessionID)
	}
	if last.Tokens != (core.Usage{In: 1330, Out: 42}) {
		t.Errorf("result tokens = %+v, want {1330 42}", last.Tokens)
	}
}

// TestRunResumeArgv asserts --resume <id> appears in argv exactly when
// ResumeID is set, and that --effort maps only recognized levels.
func TestRunResumeArgv(t *testing.T) {
	tests := []struct {
		name       string
		task       core.Task
		wantResume bool
		wantEffort string // "" means --effort must be absent
	}{
		{name: "fresh run", task: core.Task{Prompt: "p"}, wantResume: false},
		{name: "resume", task: core.Task{Prompt: "p", ResumeID: "sess-xyz"}, wantResume: true},
		{name: "effort high", task: core.Task{Prompt: "p", Effort: "high"}, wantEffort: "high"},
		{name: "effort bogus dropped", task: core.Task{Prompt: "p", Effort: "banana"}, wantEffort: ""},
		{name: "fast is no-op", task: core.Task{Prompt: "p", Fast: true}, wantEffort: ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			argvPath, _ := setupFake(t, dir, fakeConfig{
				fixture:   fixture(t, "stream-success.jsonl"),
				recordArg: true,
			})
			a := New(WithBinary(fixture(t, "fake-claude.sh")))

			ch, err := a.Run(context.Background(), tc.task, dir)
			if err != nil {
				t.Fatalf("Run: %v", err)
			}
			collect(t, ch)

			argv := readLines(t, argvPath)
			joined := strings.Join(argv, " ")

			for _, want := range []string{"-p", "--output-format", "stream-json", "--verbose"} {
				if !contains(argv, want) {
					t.Errorf("argv missing %q: %v", want, argv)
				}
			}

			if hasResume := contains(argv, "--resume"); hasResume != tc.wantResume {
				t.Errorf("--resume present = %v, want %v (argv %q)", hasResume, tc.wantResume, joined)
			}
			if tc.wantResume && !argFollowedBy(argv, "--resume", tc.task.ResumeID) {
				t.Errorf("--resume not followed by %q: %v", tc.task.ResumeID, argv)
			}

			wantEffort := tc.wantEffort != ""
			if hasEffort := contains(argv, "--effort"); hasEffort != wantEffort {
				t.Errorf("--effort present = %v, want %v (argv %q)", hasEffort, wantEffort, joined)
			}
			if wantEffort && !argFollowedBy(argv, "--effort", tc.wantEffort) {
				t.Errorf("--effort not followed by %q: %v", tc.wantEffort, argv)
			}
		})
	}
}

// TestChildEnvStripsAnthropicKeys is the security-critical test: even when the
// parent sets ANTHROPIC_API_KEY / ANTHROPIC_AUTH_TOKEN, the child must never see
// them, while a benign allowlisted var (PATH) passes through.
func TestChildEnvStripsAnthropicKeys(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-should-not-leak")
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "tok-should-not-leak")
	t.Setenv("CLEX_SECRET_JUNK", "should-not-pass-allowlist")

	dir := t.TempDir()
	_, envPath := setupFake(t, dir, fakeConfig{
		fixture:   fixture(t, "stream-success.jsonl"),
		recordEnv: true,
	})
	a := New(WithBinary(fixture(t, "fake-claude.sh")))

	ch, err := a.Run(context.Background(), core.Task{Prompt: "p"}, dir)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	collect(t, ch)

	names := envNames(readLines(t, envPath))
	for _, banned := range []string{"ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN"} {
		if names[banned] {
			t.Errorf("child env leaked %s", banned)
		}
	}
	if names["CLEX_SECRET_JUNK"] {
		t.Errorf("child env passed a non-allowlisted var: %v", names)
	}
	if !names["PATH"] {
		t.Errorf("child env missing PATH; got %v", names)
	}
}

// TestChildEnvUnit exercises childEnv directly, independent of process spawning.
func TestChildEnvUnit(t *testing.T) {
	parent := []string{
		"ANTHROPIC_API_KEY=leak",
		"ANTHROPIC_AUTH_TOKEN=leak",
		"PATH=/usr/bin",
		"HOME=/home/me",
		"GIT_AUTHOR_NAME=clex",
		"RANDOM_SECRET=nope",
		"malformed-no-equals",
	}
	set := envValues(childEnv(parent))
	if _, ok := set["ANTHROPIC_API_KEY"]; ok {
		t.Error("ANTHROPIC_API_KEY not stripped")
	}
	if _, ok := set["ANTHROPIC_AUTH_TOKEN"]; ok {
		t.Error("ANTHROPIC_AUTH_TOKEN not stripped")
	}
	if set["PATH"] != "/usr/bin" || set["HOME"] != "/home/me" {
		t.Errorf("allowlisted vars missing: %v", set)
	}
	if set["GIT_AUTHOR_NAME"] != "clex" {
		t.Errorf("GIT_ prefix var missing: %v", set)
	}
	if _, ok := set["RANDOM_SECRET"]; ok {
		t.Error("non-allowlisted RANDOM_SECRET passed through")
	}
}

// TestRunCancellationKillsChild proves ctx cancellation kills the child's whole
// process group, leaving no orphaned descendant (no zombie).
func TestRunCancellationKillsChild(t *testing.T) {
	dir := t.TempDir()
	// The sleep fake writes its child pid into .fake/childpid-out.
	if err := os.MkdirAll(filepath.Join(dir, ".fake"), 0o700); err != nil {
		t.Fatal(err)
	}
	pidFile := filepath.Join(dir, ".fake", "childpid-out")
	a := New(WithBinary(fixture(t, "fake-claude-sleep.sh")))

	ctx, cancel := context.WithCancel(context.Background())
	ch, err := a.Run(ctx, core.Task{Prompt: "p"}, dir)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	childPID := waitForPID(t, pidFile)
	cancel()

	select {
	case <-drainUntilClosed(ch):
	case <-time.After(10 * time.Second):
		t.Fatal("run channel did not close after cancel")
	}

	// Give the OS a beat to reap, then assert the descendant sleep is gone.
	if waitUntilDead(childPID, 5*time.Second) {
		return
	}
	t.Errorf("orphaned child pid %d still alive after cancellation", childPID)
}

// TestRunMalformedLine asserts a bad JSON line becomes an error event while the
// stream keeps flowing to its terminal result.
func TestRunMalformedLine(t *testing.T) {
	dir := t.TempDir()
	setupFake(t, dir, fakeConfig{fixture: fixture(t, "stream-malformed.jsonl")})
	a := New(WithBinary(fixture(t, "fake-claude.sh")))

	ch, err := a.Run(context.Background(), core.Task{Prompt: "p"}, dir)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	evs := collect(t, ch)

	var sawError, sawAfter, sawResult bool
	for _, ev := range evs {
		switch {
		case ev.Type == core.EventError && strings.Contains(ev.Err, "malformed"):
			sawError = true
		case ev.Type == core.EventText && ev.Text == "after":
			sawAfter = true
		case ev.Type == core.EventResult:
			sawResult = true
		}
	}
	if !sawError {
		t.Errorf("no malformed-line error event: %+v", evs)
	}
	if !sawAfter {
		t.Errorf("stream did not continue past malformed line: %+v", evs)
	}
	if !sawResult {
		t.Errorf("terminal result missing after malformed line: %+v", evs)
	}
}

// TestInjectSkills verifies named skill dirs are symlinked into the run's
// .claude/skills, while unknown skills and traversal attempts are skipped.
func TestInjectSkills(t *testing.T) {
	skillsRoot := t.TempDir()
	for _, s := range []string{"clex-plan", "to-issues"} {
		if err := os.MkdirAll(filepath.Join(skillsRoot, s), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	work := t.TempDir()
	setupFake(t, work, fakeConfig{fixture: fixture(t, "stream-success.jsonl")})
	a := New(WithBinary(fixture(t, "fake-claude.sh")), WithSkillsRoot(skillsRoot))

	task := core.Task{Prompt: "p", Skills: []string{"clex-plan", "to-issues", "does-not-exist", "../escape"}}
	ch, err := a.Run(context.Background(), task, work)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	collect(t, ch)

	skillsDir := filepath.Join(work, ".claude", "skills")
	for _, s := range []string{"clex-plan", "to-issues"} {
		target, err := os.Readlink(filepath.Join(skillsDir, s))
		if err != nil {
			t.Errorf("skill %s not linked: %v", s, err)
			continue
		}
		if target != filepath.Join(skillsRoot, s) {
			t.Errorf("skill %s links to %q", s, target)
		}
	}
	// Unknown skill and the basename of a traversal attempt must not be linked.
	for _, s := range []string{"does-not-exist", "escape"} {
		if _, err := os.Lstat(filepath.Join(skillsDir, s)); err == nil {
			t.Errorf("unexpected skill link created for %q", s)
		}
	}
	// No link should have escaped the skills dir into the worktree root.
	if _, err := os.Lstat(filepath.Join(work, ".claude", "escape")); err == nil {
		t.Error("traversal skill escaped into .claude/")
	}
}

// TestRunStartError surfaces a spawn failure (missing binary) as a Go error.
func TestRunStartError(t *testing.T) {
	a := New(WithBinary(filepath.Join(t.TempDir(), "nope-does-not-exist")))
	_, err := a.Run(context.Background(), core.Task{Prompt: "p"}, t.TempDir())
	if err == nil {
		t.Fatal("expected error starting a missing binary")
	}
}

// --- small helpers ---

func envNames(kvs []string) map[string]bool {
	names := map[string]bool{}
	for _, kv := range kvs {
		if name, _, ok := strings.Cut(kv, "="); ok {
			names[name] = true
		}
	}
	return names
}

func envValues(kvs []string) map[string]string {
	set := map[string]string{}
	for _, kv := range kvs {
		if k, v, ok := strings.Cut(kv, "="); ok {
			set[k] = v
		}
	}
	return set
}

func readLines(t *testing.T, path string) []string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer f.Close()
	var lines []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		lines = append(lines, sc.Text())
	}
	return lines
}

func contains(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}

func argFollowedBy(ss []string, flag, val string) bool {
	for i, s := range ss {
		if s == flag && i+1 < len(ss) && ss[i+1] == val {
			return true
		}
	}
	return false
}

func waitForPID(t *testing.T, pidFile string) int {
	t.Helper()
	deadline := time.After(8 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatal("fake child never recorded its pid")
		default:
		}
		if lines := tryReadLines(pidFile); len(lines) > 0 {
			if pid, err := strconv.Atoi(strings.TrimSpace(lines[0])); err == nil && pid > 0 {
				return pid
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func tryReadLines(path string) []string {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	var lines []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		lines = append(lines, sc.Text())
	}
	return lines
}

func drainUntilClosed(ch <-chan core.Event) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		for range ch {
		}
		close(done)
	}()
	return done
}

// waitUntilDead polls until pid is no longer signalable or the timeout elapses.
func waitUntilDead(pid int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if syscall.Kill(pid, 0) != nil {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return syscall.Kill(pid, 0) != nil
}
