package scheduler

import (
	"errors"
	"reflect"
	"sort"
	"testing"

	"github.com/reissui/clex/internal/core"
)

// approved is a helper to build an approved issue.
func approved(num int, deps []int, touches []string) Issue {
	return Issue{Number: num, State: core.StateApproved, DependsOn: deps, Touches: touches}
}

func dispatchedNums(ds []Dispatch) []int {
	out := make([]int, 0, len(ds))
	for _, d := range ds {
		out = append(out, d.Issue)
	}
	sort.Ints(out)
	return out
}

// TestDiamondDependency: A→B, A→C, B+C→D. With A closed, B and C dispatch
// together; D only after both B and C close.
func TestDiamondDependency(t *testing.T) {
	// A = 1 (closed), B = 2, C = 3, D = 4 depends on 2 and 3.
	issues := []Issue{
		{Number: 1, State: core.StateApproved, Closed: true},
		approved(2, []int{1}, []string{"b/**"}),
		approved(3, []int{1}, []string{"c/**"}),
		approved(4, []int{2, 3}, []string{"d/**"}),
	}
	state := SchedulerState{Issues: issues, Caps: Caps{MaxParallel: 10}}
	got := dispatchedNums(Next(state))
	want := []int{2, 3}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("first pass dispatched %v, want %v (D must wait for B and C)", got, want)
	}

	// Close B only: D still waits.
	issues[1].Closed = true
	got = dispatchedNums(Next(SchedulerState{Issues: issues, Caps: Caps{MaxParallel: 10}}))
	if reflect.DeepEqual(got, []int{4}) || contains(got, 4) {
		t.Fatalf("D dispatched with only B closed: %v", got)
	}

	// Close C too: now D is eligible.
	issues[2].Closed = true
	got = dispatchedNums(Next(SchedulerState{Issues: issues, Caps: Caps{MaxParallel: 10}}))
	if !contains(got, 4) {
		t.Fatalf("D not dispatched after B and C closed: %v", got)
	}
}

func TestCycleDetection(t *testing.T) {
	// 1→2→3→1 cycle.
	issues := []Issue{
		approved(1, []int{3}, nil),
		approved(2, []int{1}, nil),
		approved(3, []int{2}, nil),
	}
	err := Validate(issues)
	if err == nil {
		t.Fatal("expected cycle error, got nil")
	}
	var ce *CycleError
	if !errors.As(err, &ce) {
		t.Fatalf("expected *CycleError, got %T: %v", err, err)
	}
	// All three members must be named.
	sort.Ints(ce.Members)
	if !reflect.DeepEqual(ce.Members, []int{1, 2, 3}) {
		t.Fatalf("cycle members = %v, want [1 2 3]", ce.Members)
	}
	// Next must not panic and must dispatch nothing on a cyclic graph.
	if ds := Next(SchedulerState{Issues: issues, Caps: Caps{MaxParallel: 10}}); len(ds) != 0 {
		t.Fatalf("Next on cyclic graph dispatched %v, want none", ds)
	}
}

func TestTouchesSerialization(t *testing.T) {
	// Two eligible issues with overlapping globs → only the earliest dispatches.
	overlapping := []Issue{
		approved(1, nil, []string{"internal/gh/**"}),
		approved(2, nil, []string{"internal/gh/client.go"}), // under internal/gh
	}
	got := dispatchedNums(Next(SchedulerState{Issues: overlapping, Caps: Caps{MaxParallel: 10}}))
	if !reflect.DeepEqual(got, []int{1}) {
		t.Fatalf("overlapping globs both dispatched: %v, want [1]", got)
	}

	// Disjoint globs → both dispatch.
	disjoint := []Issue{
		approved(1, nil, []string{"internal/gh/**"}),
		approved(2, nil, []string{"internal/store/**"}),
	}
	got = dispatchedNums(Next(SchedulerState{Issues: disjoint, Caps: Caps{MaxParallel: 10}}))
	if !reflect.DeepEqual(got, []int{1, 2}) {
		t.Fatalf("disjoint globs not both dispatched: %v, want [1 2]", got)
	}

	// Wildcard/empty touches overlaps everything.
	wildcard := []Issue{
		approved(1, nil, nil), // empty = touches everything
		approved(2, nil, []string{"internal/store/**"}),
	}
	got = dispatchedNums(Next(SchedulerState{Issues: wildcard, Caps: Caps{MaxParallel: 10}}))
	if !reflect.DeepEqual(got, []int{1}) {
		t.Fatalf("wildcard touches did not serialize: %v, want [1]", got)
	}
}

func TestGlobalCap(t *testing.T) {
	// 5 eligible, disjoint globs, cap 2 → exactly 2 dispatches.
	var issues []Issue
	for i := 1; i <= 5; i++ {
		issues = append(issues, approved(i, nil, []string{globN(i)}))
	}
	got := Next(SchedulerState{Issues: issues, Caps: Caps{MaxParallel: 2}})
	if len(got) != 2 {
		t.Fatalf("cap 2 produced %d dispatches, want 2", len(got))
	}
	// Earliest-numbered win.
	if !reflect.DeepEqual(dispatchedNums(got), []int{1, 2}) {
		t.Fatalf("cap selected %v, want [1 2]", dispatchedNums(got))
	}
}

func TestGlobalCapCountsRunning(t *testing.T) {
	issues := []Issue{
		approved(1, nil, []string{"a/**"}),
		approved(2, nil, []string{"b/**"}),
	}
	// One already running, cap 2 → only one more slot.
	state := SchedulerState{
		Issues:  issues,
		Running: []Running{{Issue: 99, Provider: "x", Touches: []string{"z/**"}}},
		Caps:    Caps{MaxParallel: 2},
	}
	got := Next(state)
	if len(got) != 1 {
		t.Fatalf("with 1 running and cap 2, dispatched %d, want 1", len(got))
	}
}

func TestPerProviderCap(t *testing.T) {
	// Three eligible issues all on provider "claude", per-provider cap 2.
	issues := []Issue{
		{Number: 1, State: core.StateApproved, Provider: "claude", Touches: []string{"a/**"}},
		{Number: 2, State: core.StateApproved, Provider: "claude", Touches: []string{"b/**"}},
		{Number: 3, State: core.StateApproved, Provider: "claude", Touches: []string{"c/**"}},
	}
	state := SchedulerState{
		Issues: issues,
		Caps:   Caps{MaxParallel: 10, PerProvider: map[string]int{"claude": 2}},
	}
	got := Next(state)
	if len(got) != 2 {
		t.Fatalf("per-provider cap 2 produced %d dispatches, want 2", len(got))
	}
	// A different provider is unaffected.
	issues = append(issues, Issue{Number: 4, State: core.StateApproved, Provider: "codex", Touches: []string{"d/**"}})
	state = SchedulerState{Issues: issues, Caps: Caps{MaxParallel: 10, PerProvider: map[string]int{"claude": 2}}}
	got = Next(state)
	if !contains(dispatchedNums(got), 4) {
		t.Fatalf("codex issue held back by claude cap: %v", dispatchedNums(got))
	}
}

func TestNotApprovedNotDispatched(t *testing.T) {
	issues := []Issue{
		{Number: 1, State: core.StateBuilding, Touches: []string{"a/**"}},
		{Number: 2, State: core.StatePlanned, Touches: []string{"b/**"}},
	}
	if ds := Next(SchedulerState{Issues: issues, Caps: Caps{MaxParallel: 10}}); len(ds) != 0 {
		t.Fatalf("non-approved issues dispatched: %v", ds)
	}
}

// helpers

func contains(xs []int, v int) bool {
	for _, x := range xs {
		if x == v {
			return true
		}
	}
	return false
}

func globN(i int) string {
	return "pkg" + string(rune('a'+i)) + "/**"
}
