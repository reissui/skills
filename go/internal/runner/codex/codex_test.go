package codex

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/reissui/clex/internal/core"
)

// fakeCodexPath returns the absolute path to the fake codex script, skipping on
// platforms where the bash script cannot run.
func fakeCodexPath(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake-codex.sh requires a POSIX shell")
	}
	p, err := filepath.Abs(filepath.Join("testdata", "fake-codex.sh"))
	if err != nil {
		t.Fatalf("abs fake path: %v", err)
	}
	return p
}

func TestNew_DefaultsAndModel(t *testing.T) {
	a := New("gpt-5-5")
	if a.Model() != "gpt-5-5" {
		t.Errorf("Model() = %q, want gpt-5-5", a.Model())
	}
	if a.binary != defaultBinary {
		t.Errorf("binary = %q, want %q", a.binary, defaultBinary)
	}

	// One adapter type serves multiple codex models via distinct constructions.
	if New("codex-mini").Model() != "codex-mini" {
		t.Errorf("second model not honored")
	}
}

func TestBuildArgs_FreshRun(t *testing.T) {
	a := New("gpt-5-5", WithBinary("/bin/true"))
	task := core.Task{Prompt: "do the thing", Effort: "high"}
	got := a.buildArgs(task, "/work/tree")

	want := []string{
		"exec", "--json",
		"--model", "gpt-5-5",
		"--cd", "/work/tree", "--skip-git-repo-check",
		"-c", "model_reasoning_effort=high",
		"do the thing",
	}
	assertArgs(t, want, got)

	// The prompt must be the final positional arg (never stdin) so the child
	// receives it deterministically.
	if got[len(got)-1] != "do the thing" {
		t.Errorf("prompt not last arg: %v", got)
	}
}

// When ResumeID is set, the resume subcommand and session id precede the prompt
// (spec acceptance: resume subcommand used when ResumeID set — assert argv).
func TestBuildArgs_ResumeSubcommand(t *testing.T) {
	a := New("gpt-5-5", WithBinary("/bin/true"))
	task := core.Task{Prompt: "continue please", ResumeID: "sess-42"}
	got := a.buildArgs(task, "/work/tree")

	// "resume" must appear, immediately followed by the id then the prompt.
	ri := indexOf(got, "resume")
	if ri < 0 {
		t.Fatalf("resume subcommand missing: %v", got)
	}
	if got[ri+1] != "sess-42" {
		t.Errorf("arg after resume = %q, want sess-42: %v", got[ri+1], got)
	}
	if got[ri+2] != "continue please" {
		t.Errorf("arg after session id = %q, want prompt: %v", got[ri+2], got)
	}
	// A fresh run's bare prompt-only tail must NOT be present.
	if got[len(got)-3] != "resume" {
		t.Errorf("expected trailing [resume id prompt], got %v", got[len(got)-3:])
	}
}

func TestBuildArgs_NoEffortWhenUnset(t *testing.T) {
	a := New("gpt-5-5", WithBinary("/bin/true"))
	got := a.buildArgs(core.Task{Prompt: "x"}, "/d")
	if indexOf(got, "model_reasoning_effort=") >= 0 || containsSubstr(got, "model_reasoning_effort") {
		t.Errorf("effort override should be absent when Effort empty: %v", got)
	}
	// Unknown effort is also dropped.
	got = a.buildArgs(core.Task{Prompt: "x", Effort: "nonsense"}, "/d")
	if containsSubstr(got, "model_reasoning_effort") {
		t.Errorf("unknown effort should be dropped: %v", got)
	}
}

func TestBuildArgs_NoModelFlagWhenEmpty(t *testing.T) {
	a := New("", WithBinary("/bin/true"))
	got := a.buildArgs(core.Task{Prompt: "x"}, "/d")
	if indexOf(got, "--model") >= 0 {
		t.Errorf("--model must be omitted when model id empty: %v", got)
	}
}

// WithExtraArgs injects global flags after the base config flags and before the
// prompt; the prompt stays the final positional. This is what the local adapter
// relies on to add --oss (spec: Runner adapters — local uses the same shape).
func TestBuildArgs_WithExtraArgs(t *testing.T) {
	a := New("qwen3-coder", WithBinary("/bin/true"), WithExtraArgs("--oss"))
	got := a.buildArgs(core.Task{Prompt: "build it"}, "/work/tree")

	if indexOf(got, "--oss") < 0 {
		t.Fatalf("--oss missing from argv: %v", got)
	}
	// --oss must precede the prompt, which stays last.
	if indexOf(got, "--oss") >= indexOf(got, "build it") {
		t.Errorf("--oss should precede the prompt: %v", got)
	}
	if got[len(got)-1] != "build it" {
		t.Errorf("prompt not last arg: %v", got)
	}

	// With a resume id, extra args still precede the resume subcommand and the
	// prompt remains last.
	got = a.buildArgs(core.Task{Prompt: "go on", ResumeID: "s-1"}, "/d")
	if indexOf(got, "--oss") >= indexOf(got, "resume") {
		t.Errorf("--oss should precede resume: %v", got)
	}
	if got[len(got)-1] != "go on" {
		t.Errorf("prompt not last arg on resume: %v", got)
	}
}

func TestReasoningEffort(t *testing.T) {
	cases := map[string]string{
		"minimal": "minimal",
		"low":     "low",
		"MEDIUM":  "medium",
		" High ":  "high",
		"max":     "high",
		"":        "",
		"weird":   "",
	}
	for in, want := range cases {
		if got := reasoningEffort(in); got != want {
			t.Errorf("reasoningEffort(%q) = %q, want %q", in, got, want)
		}
	}
}

// Run over the fake CLI produces the normalized event sequence and the resumable
// session id, and the argv the child received matches expectations.
func TestRun_StreamsEventsAndSessionID(t *testing.T) {
	fake := fakeCodexPath(t)
	dir := t.TempDir()
	argsFile := filepath.Join(t.TempDir(), "argv")

	env := []string{
		"PATH=" + os.Getenv("PATH"),
		"FAKE_CODEX_STREAM=" + filepath.Join(mustAbsTestdata(t), "run_basic.jsonl"),
		"FAKE_CODEX_ARGS_FILE=" + argsFile,
	}
	a := New("gpt-5-5", WithBinary(fake), WithEnv(env))

	ch, err := a.Run(context.Background(), core.Task{Prompt: "ping", Effort: "low"}, dir)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	got := drain(ch)

	want := []core.Event{
		{Type: core.EventText, Text: "pong"},
		{Type: core.EventUsage, Tokens: core.Usage{In: 17669, Out: 33}},
		{Type: core.EventResult, SessionID: "019f2786-4f6b-7981-9c4f-735482e90a37"},
	}
	assertEvents(t, want, got)

	// Assert the child actually received `exec --json … ping` with the effort
	// override, proving argv wiring end to end.
	argv := readLines(t, argsFile)
	if indexOf(argv, "exec") != 0 || indexOf(argv, "--json") < 0 {
		t.Errorf("child argv missing exec --json: %v", argv)
	}
	if indexOf(argv, "model_reasoning_effort=low") < 0 {
		t.Errorf("child argv missing effort override: %v", argv)
	}
	if argv[len(argv)-1] != "ping" {
		t.Errorf("child argv prompt = %q, want ping", argv[len(argv)-1])
	}
}

// Run with a fixture containing a malformed line surfaces an error event but the
// stream continues to a normal terminal result.
func TestRun_MalformedLineContinues(t *testing.T) {
	fake := fakeCodexPath(t)
	dir := t.TempDir()
	env := []string{
		"PATH=" + os.Getenv("PATH"),
		"FAKE_CODEX_STREAM=" + filepath.Join(mustAbsTestdata(t), "run_malformed.jsonl"),
	}
	a := New("gpt-5-5", WithBinary(fake), WithEnv(env))

	ch, err := a.Run(context.Background(), core.Task{Prompt: "x"}, dir)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	got := drain(ch)

	var sawErr, sawResult bool
	for _, ev := range got {
		switch ev.Type {
		case core.EventError:
			sawErr = true
		case core.EventResult:
			sawResult = true
		}
	}
	if !sawErr {
		t.Errorf("expected a malformed error event: %+v", got)
	}
	if !sawResult {
		t.Errorf("stream should continue to a terminal result: %+v", got)
	}
}

// Cancelling the context kills the child (and its group) promptly; the channel
// closes without hanging (spec acceptance: cancellation kills the child cleanly).
func TestRun_CancelKillsChild(t *testing.T) {
	fake := fakeCodexPath(t)
	dir := t.TempDir()
	env := []string{
		"PATH=" + os.Getenv("PATH"),
		"FAKE_CODEX_HANG=1",
	}
	a := New("gpt-5-5", WithBinary(fake), WithEnv(env))

	ctx, cancel := context.WithCancel(context.Background())
	ch, err := a.Run(ctx, core.Task{Prompt: "x"}, dir)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	// Read the first event (thread.started → nothing emitted yet; the hang emits
	// only a thread id which becomes the eventual result). Give the child a beat
	// to start, then cancel.
	time.Sleep(150 * time.Millisecond)
	cancel()

	done := make(chan struct{})
	go func() {
		drain(ch)
		close(done)
	}()
	select {
	case <-done:
		// Channel closed after cancellation — child was killed and reaped.
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not stop within 5s of cancellation")
	}
}

func TestRun_NoBinary(t *testing.T) {
	a := &Adapter{} // binary empty
	if _, err := a.Run(context.Background(), core.Task{Prompt: "x"}, t.TempDir()); err == nil {
		t.Fatal("expected error when no binary configured")
	}
}

func TestFilterEnv_AllowlistAndStrip(t *testing.T) {
	in := []string{
		"PATH=/usr/bin",
		"HOME=/home/me",
		"CODEX_HOME=/home/me/.codex",
		"OPENAI_API_KEY=sk-test",
		"LC_CTYPE=UTF-8",
		"ANTHROPIC_API_KEY=leak-me-not",
		"ANTHROPIC_AUTH_TOKEN=nope",
		"AWS_SECRET_ACCESS_KEY=should-drop",
		"RANDOM_SECRET=drop",
		"malformed-no-equals",
	}
	got := filterEnv(in)
	set := toSet(got)

	for _, keep := range []string{
		"PATH=/usr/bin", "HOME=/home/me", "CODEX_HOME=/home/me/.codex",
		"OPENAI_API_KEY=sk-test", "LC_CTYPE=UTF-8",
	} {
		if !set[keep] {
			t.Errorf("expected env kept: %q\n got: %v", keep, got)
		}
	}
	for _, key := range []string{"ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "AWS_SECRET_ACCESS_KEY", "RANDOM_SECRET"} {
		for _, kv := range got {
			if strings.HasPrefix(kv, key+"=") {
				t.Errorf("env %q must be stripped, present as %q", key, kv)
			}
		}
	}
}

// The default (parent-derived) child env strips secrets and keeps allowlisted
// vars — this is exactly what Run passes to the child via cmd.Env.
func TestChildEnv_DefaultFiltersParent(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "leak")
	t.Setenv("RANDOM_SECRET", "leak2")
	t.Setenv("CODEX_HOME", "/tmp/codexhome")
	t.Setenv("PATH", "/usr/bin")

	a := New("gpt-5-5") // no WithEnv → default filtered env
	child := a.childEnv()
	set := toSet(child)

	for _, kv := range child {
		if strings.HasPrefix(kv, "ANTHROPIC_API_KEY=") || strings.HasPrefix(kv, "RANDOM_SECRET=") {
			t.Errorf("secret leaked into child env: %q", kv)
		}
	}
	if !set["CODEX_HOME=/tmp/codexhome"] {
		t.Errorf("CODEX_HOME should pass through, child env: %v", child)
	}
	// childEnv is memoized: a second call returns the same slice.
	if &a.childEnv()[0] != &child[0] {
		t.Errorf("childEnv should be computed once and cached")
	}
}

// --- small helpers ---

func drain(ch <-chan core.Event) []core.Event {
	var got []core.Event
	for ev := range ch {
		got = append(got, ev)
	}
	return got
}

func mustAbsTestdata(t *testing.T) string {
	t.Helper()
	p, err := filepath.Abs("testdata")
	if err != nil {
		t.Fatalf("abs testdata: %v", err)
	}
	return p
}

func readLines(t *testing.T, path string) []string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	lines := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	return lines
}

func assertArgs(t *testing.T, want, got []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("argv len = %d, want %d\n got: %v\nwant: %v", len(got), len(want), got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("argv[%d] = %q, want %q\nfull: %v", i, got[i], want[i], got)
		}
	}
}

func indexOf(ss []string, target string) int {
	for i, s := range ss {
		if s == target {
			return i
		}
	}
	return -1
}

func containsSubstr(ss []string, sub string) bool {
	for _, s := range ss {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

func toSet(ss []string) map[string]bool {
	m := make(map[string]bool, len(ss))
	for _, s := range ss {
		m[s] = true
	}
	return m
}
