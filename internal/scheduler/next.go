package scheduler

import (
	"fmt"
	"sort"

	"github.com/reissui/clex/internal/core"
)

// Next returns the set of issues to dispatch now, given the current state. It is
// deterministic: issues are considered in ascending number order, so among
// touches-conflicting eligible issues the earliest-numbered wins and the rest
// wait. Returned dispatches never exceed the global or per-provider caps.
//
// Next enforces, in order:
//  1. eligibility — issue is clex:approved and all its dependencies are closed;
//  2. touches serialization — an eligible issue whose globs overlap an already-
//     running issue, or an earlier eligible issue selected in this pass, waits;
//  3. caps — global MaxParallel (counting the running set) and per-provider.
//
// If the dependency graph contains a cycle, Next returns nil and the cycle is
// discoverable via Validate; Next itself does not panic on cycles.
func Next(state SchedulerState) []Dispatch {
	if err := Validate(state.Issues); err != nil {
		// A cyclic graph has no safe dispatch; the daemon surfaces the cycle
		// via Validate/Explain. Return nothing rather than risk a bad order.
		return nil
	}

	byNum := make(map[int]Issue, len(state.Issues))
	for _, is := range state.Issues {
		byNum[is.Number] = is
	}

	// Global headroom.
	globalSlots := state.Caps.MaxParallel - len(state.Running)
	if state.Caps.MaxParallel <= 0 {
		// A non-positive MaxParallel means "no global limit configured"; fall
		// back to the number of issues so per-provider caps still apply.
		globalSlots = len(state.Issues)
	}
	if globalSlots <= 0 {
		return nil
	}

	// Per-provider headroom, seeded from the running set.
	provUsed := make(map[string]int)
	for _, r := range state.Running {
		provUsed[r.Provider]++
	}

	// Glob sets already "claimed" — start with everything running.
	claimed := make([][]string, 0, len(state.Running)+len(state.Issues))
	for _, r := range state.Running {
		claimed = append(claimed, r.Touches)
	}

	// Consider eligible issues in ascending number order.
	eligible := eligibleSorted(state.Issues, byNum)

	var out []Dispatch
	for _, is := range eligible {
		if len(out) >= globalSlots {
			break
		}
		// Touches serialization against running + already-selected.
		if overlapsAny(is.Touches, claimed) {
			continue
		}
		// Per-provider cap (only when the issue has a known provider and a cap
		// is configured for it).
		if is.Provider != "" {
			if cap, ok := state.Caps.PerProvider[is.Provider]; ok {
				if provUsed[is.Provider] >= cap {
					continue
				}
			}
		}

		out = append(out, Dispatch{
			Issue:    is.Number,
			Provider: is.Provider,
			Reason:   dispatchReason(is),
		})
		claimed = append(claimed, is.Touches)
		if is.Provider != "" {
			provUsed[is.Provider]++
		}
	}
	return out
}

// eligibleSorted returns the eligible issues (open + approved + deps closed)
// sorted by ascending number for deterministic, earliest-wins selection. A
// closed issue is never eligible: closed means the work is done, whatever label
// it still carries.
func eligibleSorted(issues []Issue, byNum map[int]Issue) []Issue {
	var el []Issue
	for _, is := range issues {
		if !is.Closed && is.State == core.StateApproved && depsSatisfied(is, byNum) {
			el = append(el, is)
		}
	}
	sort.Slice(el, func(i, j int) bool { return el[i].Number < el[j].Number })
	return el
}

func dispatchReason(is Issue) string {
	if len(is.DependsOn) == 0 {
		return "eligible: approved, no dependencies"
	}
	return fmt.Sprintf("eligible: approved, dependencies %v all closed", sortedCopy(is.DependsOn))
}

func sortedCopy(in []int) []int {
	out := append([]int(nil), in...)
	sort.Ints(out)
	return out
}
