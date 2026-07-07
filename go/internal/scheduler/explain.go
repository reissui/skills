package scheduler

import (
	"fmt"
	"sort"
	"strings"

	"github.com/reissui/clex/internal/core"
)

// Reason classifies why an issue is (not) dispatchable, for /status and bot Q&A.
type Reason string

const (
	ReasonDispatchable Reason = "dispatchable" // would be dispatched now
	ReasonNotApproved  Reason = "not_approved" // not in clex:approved
	ReasonBlocked      Reason = "blocked"      // has unclosed dependencies
	ReasonTouches      Reason = "touches"      // overlaps a running/eligible issue
	ReasonCap          Reason = "cap"          // held back by a concurrency cap
	ReasonRunning      Reason = "running"      // already in the running set
	ReasonUnknown      Reason = "unknown"      // issue number not in state
)

// Explanation is a structured answer to "why is issue N not running?".
type Explanation struct {
	Issue  int
	Reason Reason
	Detail string
	// Blockers is the list of unclosed dependency issue numbers when Reason is
	// ReasonBlocked.
	Blockers []int
	// Conflicts is the list of issue numbers whose touches overlap this one when
	// Reason is ReasonTouches.
	Conflicts []int
}

// Explain reports why a given issue is or is not running, in the context of the
// supplied state. It mirrors Next's precedence so the explanation matches the
// actual decision.
func Explain(state SchedulerState, issue int) Explanation {
	byNum := make(map[int]Issue, len(state.Issues))
	for _, is := range state.Issues {
		byNum[is.Number] = is
	}
	is, ok := byNum[issue]
	if !ok {
		return Explanation{Issue: issue, Reason: ReasonUnknown, Detail: "issue not part of this epic's scheduler state"}
	}

	// Already running?
	for _, r := range state.Running {
		if r.Issue == issue {
			return Explanation{Issue: issue, Reason: ReasonRunning, Detail: "already dispatched"}
		}
	}

	// Closed issues are done, whatever label they carry.
	if is.Closed {
		return Explanation{Issue: issue, Reason: ReasonNotApproved, Detail: "issue is closed/merged"}
	}

	// Approved?
	if is.State != core.StateApproved {
		return Explanation{
			Issue:  issue,
			Reason: ReasonNotApproved,
			Detail: fmt.Sprintf("state is %q, not %q", is.State, core.StateApproved),
		}
	}

	// Dependencies closed?
	var blockers []int
	for _, d := range is.DependsOn {
		if dep, ok := byNum[d]; ok && !dep.Closed {
			blockers = append(blockers, d)
		}
	}
	if len(blockers) > 0 {
		sort.Ints(blockers)
		return Explanation{
			Issue:    issue,
			Reason:   ReasonBlocked,
			Detail:   fmt.Sprintf("waiting on unclosed dependencies %v", blockers),
			Blockers: blockers,
		}
	}

	// Touches conflict against running or lower-numbered eligible issues?
	conflicts := touchesConflicts(state, is, byNum)
	if len(conflicts) > 0 {
		sort.Ints(conflicts)
		return Explanation{
			Issue:     issue,
			Reason:    ReasonTouches,
			Detail:    fmt.Sprintf("touches overlap with %v (earliest-numbered runs first)", conflicts),
			Conflicts: conflicts,
		}
	}

	// Would a cap hold it back? Recompute headroom the way Next does, excluding
	// this issue, and see whether a slot remains.
	if heldByCap(state, is, byNum) {
		return Explanation{
			Issue:  issue,
			Reason: ReasonCap,
			Detail: capDetail(state, is),
		}
	}

	return Explanation{Issue: issue, Reason: ReasonDispatchable, Detail: dispatchReason(is)}
}

// touchesConflicts returns the running issues, and lower-numbered eligible
// issues, whose globs overlap is.
func touchesConflicts(state SchedulerState, is Issue, byNum map[int]Issue) []int {
	var out []int
	for _, r := range state.Running {
		if overlaps(is.Touches, r.Touches) {
			out = append(out, r.Issue)
		}
	}
	for _, other := range eligibleSorted(state.Issues, byNum) {
		if other.Number >= is.Number {
			break
		}
		if overlaps(is.Touches, other.Touches) {
			out = append(out, other.Number)
		}
	}
	return out
}

// heldByCap reports whether, after eligibility and touches, the global or
// per-provider cap would prevent is from being among the issues Next dispatches.
func heldByCap(state SchedulerState, is Issue, byNum map[int]Issue) bool {
	for _, d := range Next(state) {
		if d.Issue == is.Number {
			return false // it *would* be dispatched → not cap-held
		}
	}
	// Not dispatched, but it is approved+unblocked+conflict-free (caller checked)
	// → the remaining reason is a cap.
	return true
}

func capDetail(state SchedulerState, is Issue) string {
	var parts []string
	if state.Caps.MaxParallel > 0 && len(state.Running) >= state.Caps.MaxParallel {
		parts = append(parts, fmt.Sprintf("global cap %d reached (%d running)", state.Caps.MaxParallel, len(state.Running)))
	}
	if is.Provider != "" {
		if cap, ok := state.Caps.PerProvider[is.Provider]; ok {
			used := 0
			for _, r := range state.Running {
				if r.Provider == is.Provider {
					used++
				}
			}
			if used >= cap {
				parts = append(parts, fmt.Sprintf("provider %q cap %d reached (%d running)", is.Provider, cap, used))
			}
		}
	}
	if len(parts) == 0 {
		return "held back by a concurrency cap"
	}
	return strings.Join(parts, "; ")
}
