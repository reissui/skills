package local

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/reissui/clex/internal/core"
)

// fixtureRunner returns a commandRunner that serves the named testdata fixture
// for `ollama list`, or an error to simulate the daemon being unreachable.
func fixtureRunner(t *testing.T, fixture string, fail bool) commandRunner {
	t.Helper()
	return func(_ context.Context, name string, args ...string) ([]byte, error) {
		if filepath.Base(name) != "ollama" && !strings.Contains(name, "ollama") {
			t.Fatalf("unexpected command %q; local Detect must only run ollama", name)
		}
		if len(args) == 0 || args[0] != "list" {
			t.Fatalf("expected `ollama list`, got args %v", args)
		}
		if fail {
			return nil, errors.New("could not connect to ollama app")
		}
		return readFixture(t, fixture), nil
	}
}

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return b
}

func TestNew_DefaultsAndModel(t *testing.T) {
	a := New("qwen3-coder")
	if a.Model() != "qwen3-coder" {
		t.Errorf("Model() = %q, want qwen3-coder", a.Model())
	}
	if a.ollamaBin != defaultOllama {
		t.Errorf("ollamaBin = %q, want %q", a.ollamaBin, defaultOllama)
	}
	if a.exec == nil {
		t.Fatal("wrapped codex adapter must be constructed")
	}
	// The wrapped codex adapter runs the same model id (passed through as --model).
	if a.exec.Model() != "qwen3-coder" {
		t.Errorf("wrapped codex model = %q, want qwen3-coder", a.exec.Model())
	}
}

// Detect parses `ollama list` fixture output into model ids (the NAME column).
func TestDetect_ParsesModels(t *testing.T) {
	a := New("qwen3-coder", WithCommandRunner(fixtureRunner(t, "ollama_list_populated.txt", false)))
	av := a.Detect(context.Background())

	if !av.Healthy {
		t.Errorf("expected healthy with models installed, got %+v", av)
	}
	want := []string{"qwen3-coder:latest", "llama3.2:3b", "deepseek-r1:14b"}
	assertModels(t, want, av.Models)
	if !strings.Contains(av.Detail, "3") {
		t.Errorf("Detail should report the model count, got %q", av.Detail)
	}
}

// Detect skips the header row and blank lines; an empty install yields zero
// models and an unhealthy-but-not-error availability.
func TestDetect_EmptyInstall(t *testing.T) {
	a := New("qwen3-coder", WithCommandRunner(fixtureRunner(t, "ollama_list_empty.txt", false)))
	av := a.Detect(context.Background())

	if av.Healthy {
		t.Errorf("expected unhealthy with no models, got %+v", av)
	}
	if len(av.Models) != 0 {
		t.Errorf("expected no models, got %v", av.Models)
	}
}

// A missing/unreachable Ollama is a clean not-available: Healthy false, no
// models, and crucially no error (spec: absence is not an error).
func TestDetect_NotAvailableNoError(t *testing.T) {
	a := New("qwen3-coder", WithCommandRunner(fixtureRunner(t, "", true)))
	av := a.Detect(context.Background())

	if av.Healthy {
		t.Errorf("expected unhealthy when ollama unavailable, got %+v", av)
	}
	if len(av.Models) != 0 {
		t.Errorf("expected no models when unavailable, got %v", av.Models)
	}
	// Probe wraps Detect and must likewise never return an error for absence.
	if _, err := a.Probe(context.Background()); err != nil {
		t.Errorf("Probe must not error on ollama absence, got %v", err)
	}
}

// With no ollama binary configured, Detect reports not-available without
// attempting to run anything.
func TestDetect_NoBinary(t *testing.T) {
	a := New("qwen3-coder", WithOllamaBinary(""), WithCommandRunner(func(context.Context, string, ...string) ([]byte, error) {
		t.Fatal("runner must not be called when no binary is configured")
		return nil, nil
	}))
	// New re-defaults an empty ollamaBin, so force it empty post-construction to
	// exercise the guard.
	a.ollamaBin = ""
	av := a.Detect(context.Background())
	if av.Healthy {
		t.Errorf("expected unhealthy with no binary, got %+v", av)
	}
}

// Probe is unhealthy at zero models and healthy once at least one is installed —
// both driven from fixtures, no live Ollama.
func TestProbe_HealthyOnlyWithModels(t *testing.T) {
	empty := New("m", WithCommandRunner(fixtureRunner(t, "ollama_list_empty.txt", false)))
	av, err := empty.Probe(context.Background())
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if av.Healthy {
		t.Errorf("expected unhealthy at zero models, got %+v", av)
	}

	populated := New("m", WithCommandRunner(fixtureRunner(t, "ollama_list_populated.txt", false)))
	av, err = populated.Probe(context.Background())
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if !av.Healthy {
		t.Errorf("expected healthy with models, got %+v", av)
	}
	if !strings.Contains(av.Detail, "3") {
		t.Errorf("Detail should list the model count, got %q", av.Detail)
	}
}

// Detect over the default execCommand path with a fake ollama binary proves the
// real spawn+parse path (not just the injected runner) discovers models.
func TestDetect_FakeBinaryEndToEnd(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake-ollama.sh requires a POSIX shell")
	}
	fake := mustAbs(t, filepath.Join("testdata", "fake-ollama.sh"))
	argsFile := filepath.Join(t.TempDir(), "argv")
	t.Setenv("FAKE_OLLAMA_LIST", mustAbs(t, filepath.Join("testdata", "ollama_list_populated.txt")))
	t.Setenv("FAKE_OLLAMA_ARGS_FILE", argsFile)

	a := New("qwen3-coder", WithOllamaBinary(fake))
	av := a.Detect(context.Background())
	if !av.Healthy || len(av.Models) != 3 {
		t.Fatalf("expected healthy with 3 models via fake binary, got %+v", av)
	}
	// The fake was invoked as `ollama list`.
	argv := readLines(t, argsFile)
	if len(argv) == 0 || argv[0] != "list" {
		t.Errorf("fake ollama argv = %v, want [list]", argv)
	}
}

// Run streams codex events through the wrapped adapter and the child argv proves
// --oss was injected. This simultaneously demonstrates the parsing is reused
// from the codex package (the event shapes are produced by codex's streamEvents,
// not re-implemented here) — acceptance: no duplicated parsing.
func TestRun_WrapsCodexWithOSS(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake-codex.sh requires a POSIX shell")
	}
	fakeCodex := mustAbs(t, filepath.Join("testdata", "fake-codex.sh"))
	argsFile := filepath.Join(t.TempDir(), "argv")
	env := []string{
		"PATH=" + os.Getenv("PATH"),
		"FAKE_CODEX_STREAM=" + mustAbs(t, filepath.Join("testdata", "run_basic.jsonl")),
		"FAKE_CODEX_ARGS_FILE=" + argsFile,
	}
	a := New("qwen3-coder", WithCodexBinary(fakeCodex), WithEnv(env))

	// task.Fast is set to prove Run neutralizes it (best-effort no-op locally).
	ch, err := a.Run(context.Background(), core.Task{Prompt: "ping", Fast: true}, t.TempDir())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	got := drain(ch)

	// Events are exactly what the codex parser produces from the fixture — proof
	// the codex machinery is reused verbatim.
	want := []core.Event{
		{Type: core.EventText, Text: "pong"},
		{Type: core.EventUsage, Tokens: core.Usage{In: 17669, Out: 33}},
		{Type: core.EventResult, SessionID: "019f2786-4f6b-7981-9c4f-735482e90a37"},
	}
	assertEvents(t, want, got)

	// The child must have received --oss before the prompt, and the prompt last.
	argv := readLines(t, argsFile)
	if indexOf(argv, ossFlag) < 0 {
		t.Fatalf("child argv missing %s: %v", ossFlag, argv)
	}
	if indexOf(argv, "exec") != 0 {
		t.Errorf("child argv should start with exec: %v", argv)
	}
	if indexOf(argv, ossFlag) >= indexOf(argv, "ping") {
		t.Errorf("%s should precede the prompt: %v", ossFlag, argv)
	}
	if argv[len(argv)-1] != "ping" {
		t.Errorf("prompt must be the last arg: %v", argv)
	}
	// The model id flows through to codex as --model.
	if mi := indexOf(argv, "--model"); mi < 0 || argv[mi+1] != "qwen3-coder" {
		t.Errorf("child argv missing --model qwen3-coder: %v", argv)
	}
}

func TestRun_NotConstructed(t *testing.T) {
	a := &Adapter{} // no New → exec nil
	if _, err := a.Run(context.Background(), core.Task{Prompt: "x"}, t.TempDir()); err == nil {
		t.Fatal("expected error when adapter not constructed with New")
	}
}

func TestParseOllamaList_Table(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []string
	}{
		{"header only", "NAME  ID  SIZE  MODIFIED\n", nil},
		{"blank lines skipped", "NAME ID\n\nfoo:latest x\n\n", []string{"foo:latest"}},
		{"lowercase name not header", "name:tag id\n", []string{"name:tag"}},
		{"empty input", "", nil},
		{"trailing whitespace", "NAME\n  bar:7b   deadbeef  \n", []string{"bar:7b"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := parseOllamaList([]byte(tc.in))
			assertModels(t, tc.want, got)
		})
	}
}

// --- helpers ---

func drain(ch <-chan core.Event) []core.Event {
	var got []core.Event
	for ev := range ch {
		got = append(got, ev)
	}
	return got
}

func assertEvents(t *testing.T, want, got []core.Event) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("event count = %d, want %d\n got: %+v\nwant: %+v", len(got), len(want), got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("event[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func assertModels(t *testing.T, want, got []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("model count = %d, want %d\n got: %v\nwant: %v", len(got), len(want), got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("model[%d] = %q, want %q\nfull: %v", i, got[i], want[i], got)
		}
	}
}

func mustAbs(t *testing.T, p string) string {
	t.Helper()
	abs, err := filepath.Abs(p)
	if err != nil {
		t.Fatalf("abs %s: %v", p, err)
	}
	return abs
}

func readLines(t *testing.T, path string) []string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return strings.Split(strings.TrimRight(string(b), "\n"), "\n")
}

func indexOf(ss []string, target string) int {
	for i, s := range ss {
		if s == target {
			return i
		}
	}
	return -1
}
