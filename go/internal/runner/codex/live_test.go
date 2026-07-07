//go:build live

// These tests exercise the real `codex` binary and are excluded from CI (they
// need a live, authenticated CLI). Run explicitly with:
//
//	go test -tags live ./internal/runner/codex/...
package codex

import (
	"context"
	"testing"
	"time"

	"github.com/reissui/clex/internal/core"
)

// TestLive_Probe hits the installed codex binary's version + auth check.
func TestLive_Probe(t *testing.T) {
	a := New("")
	av, err := a.Probe(context.Background())
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	t.Logf("codex availability: healthy=%v detail=%q", av.Healthy, av.Detail)
}

// TestLive_Run drives a trivial real turn and asserts a session id comes back.
func TestLive_Run(t *testing.T) {
	dir := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	a := New("")
	ch, err := a.Run(ctx, core.Task{Prompt: "Reply with the single word: pong."}, dir)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	var sawResult bool
	for ev := range ch {
		t.Logf("event: %+v", ev)
		if ev.Type == core.EventResult && ev.SessionID != "" {
			sawResult = true
		}
	}
	if !sawResult {
		t.Errorf("expected a terminal result with a session id")
	}
}
