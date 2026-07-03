package fake

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/reissui/clex/internal/core"
)

func TestAdapterRunsFakeRunnerScript(t *testing.T) {
	bin := buildFakeRunner(t)
	dir := t.TempDir()
	script := filepath.Join(dir, "script.json")
	if err := os.WriteFile(script, []byte(`{
  "session_id": "sess-test",
  "writes": [{"path": "out.txt", "content": "hello\n"}],
  "events": [
    {"type": "text", "text": "done"},
    {"type": "usage", "in": 3, "out": 4},
    {"type": "result"}
  ]
}`), 0o644); err != nil {
		t.Fatal(err)
	}

	r := New(WithBinary(bin), WithScript(script))
	ch, err := r.Run(context.Background(), core.Task{Issue: 12}, dir)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	var events []core.Event
	for ev := range ch {
		events = append(events, ev)
	}
	if len(events) != 3 {
		t.Fatalf("events = %d, want 3: %+v", len(events), events)
	}
	if events[0].Type != core.EventText || events[0].Text != "done" {
		t.Fatalf("first event = %+v, want text done", events[0])
	}
	if events[1].Tokens != (core.Usage{In: 3, Out: 4}) {
		t.Fatalf("usage = %+v, want 3/4", events[1].Tokens)
	}
	if events[2].Type != core.EventResult || events[2].SessionID != "sess-test" {
		t.Fatalf("result = %+v, want session sess-test", events[2])
	}
	if got, err := os.ReadFile(filepath.Join(dir, "out.txt")); err != nil || string(got) != "hello\n" {
		t.Fatalf("written file = %q err=%v, want hello", got, err)
	}

	av, err := r.Probe(context.Background())
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if !av.Healthy {
		t.Fatalf("Probe unhealthy: %+v", av)
	}
}

func buildFakeRunner(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "clex-fake-runner")
	cmd := exec.Command("go", "build", "-o", bin, "./cmd/clex-fake-runner")
	cmd.Dir = moduleRoot(t)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build fake runner: %v\n%s", err, out)
	}
	return bin
}

func moduleRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Clean(filepath.Join(wd, "..", "..", ".."))
}
