package codex

import (
	"bufio"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/reissui/clex/internal/core"
)

// codexEvent is one line of the `codex exec --json` JSONL stream. Only the
// fields clex normalizes are decoded; unknown event types and fields are
// ignored so a codex version bump that adds events degrades gracefully.
//
// Observed shapes (codex-cli exec --json):
//
//	{"type":"thread.started","thread_id":"<uuid>"}
//	{"type":"turn.started"}
//	{"type":"item.completed","item":{"type":"agent_message","text":"…"}}
//	{"type":"item.completed","item":{"type":"command_execution","command":"…","exit_code":0}}
//	{"type":"turn.completed","usage":{"input_tokens":N,"output_tokens":N,…}}
//	{"type":"error","message":"…"}
type codexEvent struct {
	Type     string          `json:"type"`
	ThreadID string          `json:"thread_id"`
	Item     *codexItem      `json:"item"`
	Usage    *codexUsage     `json:"usage"`
	Message  string          `json:"message"`
	Error    json.RawMessage `json:"error"`
}

// codexItem is the payload of an item.completed event.
type codexItem struct {
	Type    string `json:"type"`
	Text    string `json:"text"`
	Command string `json:"command"`
	Message string `json:"message"`
}

// codexUsage is the token accounting on a turn.completed event. Codex reports
// cached and reasoning tokens separately; clex folds them into the core in/out
// counts (input includes cached; output includes reasoning).
type codexUsage struct {
	InputTokens           int `json:"input_tokens"`
	CachedInputTokens     int `json:"cached_input_tokens"`
	OutputTokens          int `json:"output_tokens"`
	ReasoningOutputTokens int `json:"reasoning_output_tokens"`
}

// streamEvents reads the codex JSONL stream line by line, translating each line
// into zero or more core.Events sent on out. A malformed line yields an
// EventError but does not stop the stream (spec acceptance: malformed line →
// error event, stream continues). The session/thread id observed on
// thread.started is attached to the terminal EventResult so the pipeline can
// resume.
func streamEvents(r *bufio.Reader, out chan<- core.Event) {
	sc := bufio.NewScanner(r)
	// Codex lines (esp. command output) can exceed bufio's default 64 KiB.
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)

	var sessionID string
	var sawResult bool

	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}

		var ev codexEvent
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			emit(out, core.Event{
				Type: core.EventError,
				Err:  fmt.Sprintf("codex: malformed event: %v", err),
			})
			continue
		}

		switch ev.Type {
		case "thread.started":
			if ev.ThreadID != "" {
				sessionID = ev.ThreadID
			}
		case "item.completed":
			if e, ok := itemEvent(ev.Item); ok {
				emit(out, e)
			}
		case "turn.completed":
			if ev.Usage != nil {
				emit(out, core.Event{Type: core.EventUsage, Tokens: usageOf(ev.Usage)})
			}
			// turn.completed is the terminal marker of a run's work; the result
			// event below carries the resumable session id.
			emit(out, core.Event{Type: core.EventResult, SessionID: sessionID})
			sawResult = true
		case "error", "stream.error":
			emit(out, core.Event{Type: core.EventError, Err: errorText(ev)})
		default:
			// Unknown/unmapped event types (reasoning deltas, mcp progress, …)
			// are intentionally ignored.
		}
	}

	if err := sc.Err(); err != nil {
		emit(out, core.Event{
			Type: core.EventError,
			Err:  fmt.Sprintf("codex: read stream: %v", err),
		})
		return
	}

	// If the stream ended without a turn.completed (e.g. the CLI died mid-turn),
	// still surface a terminal result so consumers can resume with whatever
	// session id we captured.
	if !sawResult {
		emit(out, core.Event{Type: core.EventResult, SessionID: sessionID})
	}
}

// itemEvent maps an item.completed payload to a core.Event, reporting false for
// item types clex does not surface as events (e.g. reasoning summaries).
func itemEvent(item *codexItem) (core.Event, bool) {
	if item == nil {
		return core.Event{}, false
	}
	switch item.Type {
	case "agent_message":
		if item.Text == "" {
			return core.Event{}, false
		}
		return core.Event{Type: core.EventText, Text: item.Text}, true
	case "command_execution", "mcp_tool_call", "file_change", "web_search":
		return core.Event{Type: core.EventToolUse, Text: toolText(item)}, true
	case "error":
		msg := item.Message
		if msg == "" {
			msg = item.Text
		}
		return core.Event{Type: core.EventError, Err: msg}, true
	default:
		return core.Event{}, false
	}
}

// toolText renders a short label for a tool-use item.
func toolText(item *codexItem) string {
	switch item.Type {
	case "command_execution":
		return item.Command
	default:
		if item.Text != "" {
			return item.Text
		}
		return item.Type
	}
}

// usageOf folds codex's token breakdown into core.Usage. Cached input tokens
// are part of the input total; reasoning tokens are part of the output total.
func usageOf(u *codexUsage) core.Usage {
	return core.Usage{
		In:  u.InputTokens,
		Out: u.OutputTokens,
	}
}

// errorText extracts a human-readable message from an error event, preferring
// the structured message then any raw error payload.
func errorText(ev codexEvent) string {
	if ev.Message != "" {
		return ev.Message
	}
	if len(ev.Error) > 0 {
		return strings.TrimSpace(string(ev.Error))
	}
	return "codex reported an error"
}

// emit sends ev on out. A tiny helper so callers read cleanly and the send
// point is single-sourced.
func emit(out chan<- core.Event, ev core.Event) { out <- ev }
