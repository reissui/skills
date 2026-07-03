package core

import "testing"

// legalTransitions enumerates every from→to pair the spec's state machine
// permits. The test asserts CanTransition returns true for exactly these and
// false for everything else.
var legalTransitions = []struct{ from, to State }{
	{StateIdea, StateResearching},
	{StateResearching, StatePlanned},
	{StateResearching, StateFailed},
	{StatePlanned, StateApproved},
	{StatePlanned, StateResearching},
	{StateApproved, StateBuilding},
	{StateApproved, StateResearching},
	{StateBuilding, StateReview},
	{StateBuilding, StateApproved},
	{StateBuilding, StateFailed},
	{StateReview, StateApproved},
	{StateReview, StateFailed},
	{StateFailed, StateApproved},
}

func isLegal(from, to State) bool {
	for _, tr := range legalTransitions {
		if tr.from == from && tr.to == to {
			return true
		}
	}
	return false
}

func TestCanTransitionLegal(t *testing.T) {
	for _, tr := range legalTransitions {
		if !CanTransition(tr.from, tr.to) {
			t.Errorf("CanTransition(%q, %q) = false, want true", tr.from, tr.to)
		}
	}
}

// TestCanTransitionExhaustive walks the full cross-product of pipeline states
// and checks CanTransition against the legal set — this catches both missing
// legal edges and accidental extra ones.
func TestCanTransitionExhaustive(t *testing.T) {
	all := []State{
		StateIdea, StateResearching, StatePlanned, StateApproved,
		StateBuilding, StateReview, StateFailed,
	}
	for _, from := range all {
		for _, to := range all {
			got := CanTransition(from, to)
			want := isLegal(from, to)
			if got != want {
				t.Errorf("CanTransition(%q, %q) = %v, want %v", from, to, got, want)
			}
		}
	}
}

func TestCanTransitionIllegal(t *testing.T) {
	// A representative set of explicitly illegal moves (at least 5 required).
	illegal := []struct{ from, to State }{
		{StateIdea, StateBuilding},        // can't skip research/plan/approve
		{StateIdea, StatePlanned},         // can't skip research
		{StateApproved, StateReview},      // must build before review
		{StateResearching, StateApproved}, // must be planned first
		{StateReview, StateBuilding},      // no backward edge review→building
		{StatePlanned, StateBuilding},     // must be approved first
		{StateBuilding, StateIdea},        // no path back to idea
		{StateFailed, StateBuilding},      // retry re-enters via approved, not building
	}
	for _, tr := range illegal {
		if CanTransition(tr.from, tr.to) {
			t.Errorf("CanTransition(%q, %q) = true, want false", tr.from, tr.to)
		}
	}
}

func TestCanTransitionIdentityIsIllegal(t *testing.T) {
	for s := range pipelineStates {
		if CanTransition(s, s) {
			t.Errorf("CanTransition(%q, %q) identity = true, want false", s, s)
		}
	}
}

func TestEpicMarkerHasNoTransitions(t *testing.T) {
	// The epic marker is not a pipeline state and participates in no moves.
	if IsPipelineState(StateEpic) {
		t.Error("StateEpic must not be a pipeline state")
	}
	for _, to := range []State{StateIdea, StateResearching, StateApproved, StateBuilding} {
		if CanTransition(StateEpic, to) {
			t.Errorf("CanTransition(epic, %q) = true, want false", to)
		}
		if CanTransition(to, StateEpic) {
			t.Errorf("CanTransition(%q, epic) = true, want false", to)
		}
	}
}

func TestCanTransitionUnknownState(t *testing.T) {
	unknown := State("clex:bogus")
	if IsPipelineState(unknown) {
		t.Error("unknown label must not be reported as a pipeline state")
	}
	if CanTransition(unknown, StateApproved) {
		t.Error("transition from unknown state must be illegal")
	}
	if CanTransition(StateApproved, unknown) {
		t.Error("transition to unknown state must be illegal")
	}
}

func TestIsPipelineState(t *testing.T) {
	for s := range pipelineStates {
		if !IsPipelineState(s) {
			t.Errorf("IsPipelineState(%q) = false, want true", s)
		}
	}
}
