package scheduler

import (
	"reflect"
	"testing"

	"github.com/reissui/clex/internal/core"
)

func TestExplainReasons(t *testing.T) {
	issues := []Issue{
		{Number: 1, State: core.StateApproved, Closed: false, Touches: []string{"a/**"}},      // dispatchable
		{Number: 2, State: core.StatePlanned, Touches: []string{"b/**"}},                      // not approved
		approved(3, []int{10}, []string{"c/**"}),                                              // blocked by open #10
		{Number: 10, State: core.StateBuilding, Touches: []string{"x/**"}},                    // an open dependency
		approved(4, nil, []string{"a/**"}),                                                    // touches-conflicts with #1
		{Number: 5, State: core.StateApproved, Provider: "claude", Touches: []string{"e/**"}}, // cap-held below
		{Number: 6, State: core.StateApproved, Provider: "claude", Touches: []string{"f/**"}}, // cap-held below
	}
	state := SchedulerState{
		Issues: issues,
		Caps:   Caps{MaxParallel: 10, PerProvider: map[string]int{"claude": 1}},
	}

	// #2 not approved.
	if e := Explain(state, 2); e.Reason != ReasonNotApproved {
		t.Errorf("#2 reason = %q, want not_approved", e.Reason)
	}
	// #3 blocked by #10.
	e := Explain(state, 3)
	if e.Reason != ReasonBlocked || !reflect.DeepEqual(e.Blockers, []int{10}) {
		t.Errorf("#3 explain = %+v, want blocked by [10]", e)
	}
	// #4 conflicts with #1 on touches (both "a/**"); #1 is earlier so #4 waits.
	e = Explain(state, 4)
	if e.Reason != ReasonTouches || !contains(e.Conflicts, 1) {
		t.Errorf("#4 explain = %+v, want touches-conflict incl 1", e)
	}
	// #1 is dispatchable.
	if e := Explain(state, 1); e.Reason != ReasonDispatchable {
		t.Errorf("#1 reason = %q, want dispatchable", e.Reason)
	}
	// Among #5/#6 (both claude, cap 1), exactly one is dispatchable and the
	// other is cap-held.
	e5, e6 := Explain(state, 5), Explain(state, 6)
	reasons := []Reason{e5.Reason, e6.Reason}
	if !(contains2(reasons, ReasonDispatchable) && contains2(reasons, ReasonCap)) {
		t.Errorf("#5/#6 reasons = %v, want one dispatchable + one cap", reasons)
	}
}

func TestExplainRunningAndUnknown(t *testing.T) {
	issues := []Issue{approved(1, nil, []string{"a/**"})}
	state := SchedulerState{
		Issues:  issues,
		Running: []Running{{Issue: 1, Provider: "x", Touches: []string{"a/**"}}},
		Caps:    Caps{MaxParallel: 10},
	}
	if e := Explain(state, 1); e.Reason != ReasonRunning {
		t.Errorf("#1 reason = %q, want running", e.Reason)
	}
	if e := Explain(state, 999); e.Reason != ReasonUnknown {
		t.Errorf("#999 reason = %q, want unknown", e.Reason)
	}
}

func contains2(rs []Reason, want Reason) bool {
	for _, r := range rs {
		if r == want {
			return true
		}
	}
	return false
}
