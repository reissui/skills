// Package scheduler is the pure decision logic for what runs when in a clex
// epic. It has no I/O: it consumes already-parsed issue structs plus the current
// running set and caps, and emits dispatch decisions. The daemon (#16) performs
// the actual dispatch. Everything here is deterministic and unit-testable.
//
// Spec section: Scheduler.
package scheduler

import (
	"fmt"

	"github.com/reissui/clex/internal/core"
)

// Issue is the scheduler's view of a GitHub issue. It is populated by the caller
// (the daemon parses the issue body via internal/gh); the scheduler treats it as
// immutable data.
type Issue struct {
	Number int
	// State is the issue's current pipeline state (its clex:* label).
	State core.State
	// DependsOn lists issue numbers this issue is blocked by.
	DependsOn []int
	// Touches is the set of file globs this issue may modify. An empty slice
	// means "touches everything" (the spec default for missing metadata) and
	// therefore overlaps every other issue.
	Touches []string
	// Closed reports whether the issue is closed/merged (a satisfied dependency).
	Closed bool
	// Provider is the runner provider this issue will dispatch to, when known.
	// Used for per-provider cap accounting. May be empty before routing.
	Provider string
	// Difficulty is the planner's estimate; carried for logging/telemetry.
	Difficulty core.Difficulty
}

// Caps bounds concurrency. PerProvider is keyed by provider name; a provider
// absent from the map is treated as unbounded (only MaxParallel applies).
type Caps struct {
	MaxParallel int
	PerProvider map[string]int
}

// Running describes an in-flight dispatch the scheduler must account for when
// computing remaining headroom and touches conflicts.
type Running struct {
	Issue    int
	Provider string
	// Touches is the running issue's glob set, so pending issues that overlap an
	// already-running one are held back.
	Touches []string
}

// SchedulerState is the full input to Next: the epic's issues, what is currently
// running, and the caps.
type SchedulerState struct {
	Issues  []Issue
	Running []Running
	Caps    Caps
}

// Dispatch is a decision to start an issue, with a human-readable reason for
// logs and Telegram.
type Dispatch struct {
	Issue    int
	Provider string
	Reason   string
}

// CycleError reports a dependency cycle, naming its members in a stable order.
type CycleError struct {
	Members []int
}

func (e *CycleError) Error() string {
	return fmt.Sprintf("dependency cycle among issues %v", e.Members)
}

// Validate builds the dependency graph and returns a *CycleError if any cycle
// exists. Next also calls this; callers may call it directly to surface cycles
// at plan time.
func Validate(issues []Issue) error {
	_, err := topoOrder(issues)
	return err
}
