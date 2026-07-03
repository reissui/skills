package core

import "context"

// Runner is the single interface every model adapter implements. Each adapter
// shells out to an official CLI binary (claude, codex, codex --oss), parses its
// JSON stream, and normalizes it into Events. No adapter ever makes a direct
// provider API call (spec: Runner adapters, Compliance note).
type Runner interface {
	// Run executes task in the working directory dir and streams normalized
	// events (assistant text, tool use, usage, result) until completion. The
	// returned channel is closed when the run finishes; the terminal event is
	// an EventResult (or EventError). Cancelling ctx must stop the child
	// process.
	Run(ctx context.Context, task Task, dir string) (<-chan Event, error)

	// Probe reports the adapter's current auth state and rate-limit headroom,
	// plus any dynamically discovered models (local providers).
	Probe(ctx context.Context) (Availability, error)
}
