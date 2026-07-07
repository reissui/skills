package claude

import (
	"encoding/json"

	"github.com/reissui/clex/internal/core"
)

// streamLine is the subset of a Claude Code `--output-format stream-json` line
// that the adapter cares about. The CLI emits one JSON object per line; many
// fields (hooks, diagnostics, mcp status) are ignored. Field shapes are taken
// from the real CLI (claude 2.x): assistant usage nests under message.usage,
// while the terminal result nests token counts under a top-level usage object.
type streamLine struct {
	Type      string `json:"type"`
	Subtype   string `json:"subtype"`
	SessionID string `json:"session_id"`

	// Assistant/user turns carry a nested message.
	Message *streamMessage `json:"message"`

	// Terminal result fields.
	IsError bool         `json:"is_error"`
	Result  string       `json:"result"`
	Usage   *streamUsage `json:"usage"`

	// Rate-limit heartbeat (used by Probe).
	RateLimitInfo *rateLimitInfo `json:"rate_limit_info"`
}

// streamMessage is the assistant/user message envelope. Content is a list of
// Anthropic content blocks (text, tool_use, tool_result, …).
type streamMessage struct {
	Role    string         `json:"role"`
	Content []contentBlock `json:"content"`
	Usage   *streamUsage   `json:"usage"`
}

// contentBlock is one Anthropic content block. Only the fields the adapter
// surfaces are decoded; tool inputs and results are left as raw JSON.
type contentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
	Name string `json:"name"` // tool name when Type == "tool_use"
}

// streamUsage mirrors the CLI's token accounting. clex only tracks in/out; the
// cache breakdown is recorded by the CLI but not needed here.
type streamUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

// rateLimitInfo is the payload of a rate_limit_event line.
type rateLimitInfo struct {
	Status                string `json:"status"` // "allowed" | "rejected"
	ResetsAt              int64  `json:"resetsAt"`
	RateLimitType         string `json:"rateLimitType"`
	OverageStatus         string `json:"overageStatus"`
	OverageDisabledReason string `json:"overageDisabledReason"`
}

// parseLine converts one raw stream-json line into zero or more normalized
// core.Events. A malformed line yields a single EventError so the caller can
// surface it and keep reading the stream. The returned sessionID is non-empty
// whenever the line carried one, letting the caller latch the session id even
// from non-terminal lines.
func parseLine(raw []byte) (events []core.Event, sessionID string) {
	var ln streamLine
	if err := json.Unmarshal(raw, &ln); err != nil {
		return []core.Event{{Type: core.EventError, Err: "malformed stream-json line: " + err.Error()}}, ""
	}
	sessionID = ln.SessionID

	switch ln.Type {
	case "assistant":
		if ln.Message == nil {
			return nil, sessionID
		}
		for _, block := range ln.Message.Content {
			switch block.Type {
			case "text":
				if block.Text != "" {
					events = append(events, core.Event{Type: core.EventText, Text: block.Text})
				}
			case "tool_use":
				events = append(events, core.Event{Type: core.EventToolUse, Text: block.Name})
			}
		}
		if u := ln.Message.Usage; u != nil && (u.InputTokens != 0 || u.OutputTokens != 0) {
			events = append(events, core.Event{
				Type:   core.EventUsage,
				Tokens: core.Usage{In: u.InputTokens, Out: u.OutputTokens},
			})
		}

	case "result":
		ev := core.Event{Type: core.EventResult, SessionID: ln.SessionID}
		if ln.Usage != nil {
			ev.Tokens = core.Usage{In: ln.Usage.InputTokens, Out: ln.Usage.OutputTokens}
		}
		if ln.IsError {
			// A failed run still terminates the stream, but as an error event
			// so the pipeline reverts the issue rather than treating the
			// partial output as success.
			ev.Type = core.EventError
			ev.Err = ln.Result
		} else {
			ev.Text = ln.Result
		}
		events = append(events, ev)

	// system (init/hooks) and user (tool_result) lines carry no event the
	// pipeline needs; they are consumed only for their session id.
	default:
	}

	return events, sessionID
}
