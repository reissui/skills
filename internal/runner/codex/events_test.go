package codex

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reissui/clex/internal/core"
)

// collectStream runs streamEvents over r and returns every emitted event.
func collectStream(r *bufio.Reader) []core.Event {
	out := make(chan core.Event)
	go func() {
		defer close(out)
		streamEvents(r, out)
	}()
	var got []core.Event
	for ev := range out {
		got = append(got, ev)
	}
	return got
}

// streamFixture reads a testdata JSONL fixture and returns the parsed events.
func streamFixture(t *testing.T, name string) []core.Event {
	t.Helper()
	f, err := os.Open(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("open fixture %s: %v", name, err)
	}
	t.Cleanup(func() { f.Close() })
	return collectStream(bufio.NewReader(f))
}

func TestStreamEvents_Basic(t *testing.T) {
	got := streamFixture(t, "run_basic.jsonl")

	// Expected normalized order: assistant text, usage, terminal result with id.
	want := []core.Event{
		{Type: core.EventText, Text: "pong"},
		{Type: core.EventUsage, Tokens: core.Usage{In: 17669, Out: 33}},
		{Type: core.EventResult, SessionID: "019f2786-4f6b-7981-9c4f-735482e90a37"},
	}
	assertEvents(t, want, got)
}

func TestStreamEvents_ToolUse(t *testing.T) {
	got := streamFixture(t, "run_tools.jsonl")

	want := []core.Event{
		{Type: core.EventText, Text: "I'll run the command."},
		{Type: core.EventToolUse, Text: "echo hello"},
		{Type: core.EventText, Text: "Done: hello"},
		{Type: core.EventUsage, Tokens: core.Usage{In: 20481, Out: 58}},
		{Type: core.EventResult, SessionID: "019f2786-d4f3-7c91-8788-4cbeb332ae46"},
	}
	assertEvents(t, want, got)
}

// A malformed JSONL line must produce an error event yet let the stream
// continue, ending in a normal result (spec acceptance criterion).
func TestStreamEvents_MalformedLineContinues(t *testing.T) {
	got := streamFixture(t, "run_malformed.jsonl")

	if len(got) != 5 {
		t.Fatalf("event count = %d, want 5: %+v", len(got), got)
	}
	if got[0] != (core.Event{Type: core.EventText, Text: "before the bad line"}) {
		t.Errorf("event[0] = %+v", got[0])
	}
	if got[1].Type != core.EventError || !strings.Contains(got[1].Err, "malformed") {
		t.Errorf("event[1] = %+v, want malformed error", got[1])
	}
	if got[2] != (core.Event{Type: core.EventText, Text: "after the bad line"}) {
		t.Errorf("event[2] = %+v, want text after bad line", got[2])
	}
	if got[3].Type != core.EventUsage {
		t.Errorf("event[3] = %+v, want usage", got[3])
	}
	last := got[len(got)-1]
	if last.Type != core.EventResult || last.SessionID != "019f2790-aaaa-bbbb-cccc-000000000001" {
		t.Errorf("terminal event = %+v, want result with session id", last)
	}
}

// A stream that ends without turn.completed still yields a terminal result so
// consumers can resume with the captured session id.
func TestStreamEvents_NoTurnCompletedStillResults(t *testing.T) {
	in := strings.Join([]string{
		`{"type":"thread.started","thread_id":"sess-xyz"}`,
		`{"type":"turn.started"}`,
		`{"type":"item.completed","item":{"type":"agent_message","text":"hi"}}`,
	}, "\n")
	got := collectStream(bufio.NewReader(strings.NewReader(in)))

	if len(got) != 2 {
		t.Fatalf("event count = %d, want 2: %+v", len(got), got)
	}
	if got[0].Type != core.EventText {
		t.Errorf("event[0] = %+v, want text", got[0])
	}
	if got[1].Type != core.EventResult || got[1].SessionID != "sess-xyz" {
		t.Errorf("terminal event = %+v, want result with sess-xyz", got[1])
	}
}

func TestStreamEvents_ErrorEvent(t *testing.T) {
	in := strings.Join([]string{
		`{"type":"thread.started","thread_id":"s"}`,
		`{"type":"error","message":"rate limit exceeded"}`,
		`{"type":"turn.completed","usage":{"input_tokens":1,"output_tokens":1}}`,
	}, "\n")
	got := collectStream(bufio.NewReader(strings.NewReader(in)))

	var sawErr bool
	for _, ev := range got {
		if ev.Type == core.EventError && strings.Contains(ev.Err, "rate limit") {
			sawErr = true
		}
	}
	if !sawErr {
		t.Errorf("expected a rate-limit error event, got %+v", got)
	}
}

// Blank lines and unmapped event types are ignored, not turned into errors.
func TestStreamEvents_IgnoresBlankAndUnknown(t *testing.T) {
	in := strings.Join([]string{
		`{"type":"thread.started","thread_id":"s"}`,
		``,
		`   `,
		`{"type":"turn.started"}`,
		`{"type":"mcp_startup_update","progress":0.5}`,
		`{"type":"item.completed","item":{"type":"reasoning","text":"thinking"}}`,
		`{"type":"turn.completed","usage":{"input_tokens":2,"output_tokens":3}}`,
	}, "\n")
	got := collectStream(bufio.NewReader(strings.NewReader(in)))

	for _, ev := range got {
		if ev.Type == core.EventError {
			t.Errorf("unexpected error event: %+v", ev)
		}
		if ev.Type == core.EventText {
			t.Errorf("reasoning should not surface as text: %+v", ev)
		}
	}
	// Should still produce usage + result.
	if len(got) != 2 || got[0].Type != core.EventUsage || got[1].Type != core.EventResult {
		t.Fatalf("got %+v, want [usage, result]", got)
	}
}

// assertEvents compares event slices field-by-field with a readable failure.
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
