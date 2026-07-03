package core

// EventType classifies a normalized runner event. Adapters parse each CLI's
// native JSON stream into this shape so the pipeline is provider-agnostic
// (spec: Runner adapters).
type EventType string

const (
	EventText    EventType = "text"     // assistant text output
	EventToolUse EventType = "tool_use" // a tool/command invocation
	EventUsage   EventType = "usage"    // incremental token usage
	EventResult  EventType = "result"   // terminal result (carries SessionID)
	EventError   EventType = "error"    // an error occurred
)

// Usage holds token counts for a runner event.
type Usage struct {
	In  int `json:"in"`  // input/prompt tokens
	Out int `json:"out"` // output/completion tokens
}

// Event is one normalized item streamed from a Runner until completion. The
// same struct crosses every adapter boundary; unused fields are left zero.
type Event struct {
	Type EventType `json:"type"`
	Text string    `json:"text,omitempty"`
	// Tokens carries in/out counts when Type is EventUsage or EventResult.
	Tokens Usage `json:"tokens,omitempty"`
	// SessionID is set on the terminal result event and is used to resume the
	// CLI session (--resume / codex exec resume) instead of a fresh run.
	SessionID string `json:"session_id,omitempty"`
	// Err is populated when Type is EventError.
	Err string `json:"err,omitempty"`
}

// Task is a unit of work handed to a Runner. It carries only what the stage
// needs — scoped context, not another stage's transcript (spec: Context & token
// economy).
type Task struct {
	Repo   string `json:"repo"`
	Prompt string `json:"prompt"`
	Issue  int    `json:"issue"`
	// Skills names the skills to inject for this run; the adapter owns the
	// injection mechanism (spec: Skills layer).
	Skills []string `json:"skills,omitempty"`
	// Effort is the thinking/reasoning level; the adapter translates it to the
	// CLI's native flag (spec: Thinking & fast modes are configuration).
	Effort string `json:"effort,omitempty"`
	Fast   bool   `json:"fast,omitempty"`
	// ResumeID, when set, resumes an existing CLI session instead of starting
	// fresh (spec: Resume, don't restart).
	ResumeID string `json:"resume_id,omitempty"`
}

// Availability is the result of probing a provider: whether it is healthy, a
// human-readable headroom detail, and any discovered model ids (for local
// providers whose model set is dynamic) — spec: Runner adapters, model registry.
type Availability struct {
	Healthy bool     `json:"healthy"`
	Detail  string   `json:"detail,omitempty"`
	Models  []string `json:"models,omitempty"`
}
