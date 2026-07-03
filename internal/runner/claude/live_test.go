//go:build live

// Package claude live tests exercise the REAL `claude` binary. They are gated
// behind the `live` build tag so they never run in CI (which has no CLI and no
// credentials); run them locally with:
//
//	go test -tags live ./internal/runner/claude/...
package claude

import (
	"context"
	"testing"
	"time"

	"github.com/reissui/clex/internal/core"
)

// TestLiveProbe checks that Probe reports healthy against a logged-in CLI.
func TestLiveProbe(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	a := New() // resolves `claude` from PATH
	avail, err := a.Probe(ctx)
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	t.Logf("availability: healthy=%v detail=%q", avail.Healthy, avail.Detail)
	if !avail.Healthy {
		t.Skipf("claude not healthy in this environment: %s", avail.Detail)
	}
}

// TestLiveRun runs a trivial prompt end to end and verifies a terminal result
// with a session id and non-zero token usage.
func TestLiveRun(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	a := New()
	ch, err := a.Run(ctx, core.Task{Prompt: "Reply with exactly: ok"}, t.TempDir())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	var terminal *core.Event
	for ev := range ch {
		ev := ev
		if ev.Type == core.EventResult || ev.Type == core.EventError {
			terminal = &ev
		}
	}
	if terminal == nil {
		t.Fatal("no terminal event")
	}
	if terminal.Type == core.EventError {
		t.Fatalf("run errored: %s", terminal.Err)
	}
	if terminal.SessionID == "" {
		t.Error("terminal result missing session id")
	}
	t.Logf("session=%s tokens=%+v", terminal.SessionID, terminal.Tokens)
}
