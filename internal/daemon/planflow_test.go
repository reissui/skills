package daemon

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/reissui/clex/internal/core"
	"github.com/reissui/clex/internal/gh"
	"github.com/reissui/clex/internal/pipeline"
)

// ideaIssue seeds a clex:idea issue.
func (h *harness) ideaIssue(number int, title string) {
	h.gh.seed(&gh.Issue{
		Number:      number,
		Title:       title,
		Body:        "please build " + title,
		AuthorLogin: "acme",
		State:       core.StateIdea,
	})
}

// --- Criterion: a clex:idea issue is planned by the daemon and gated at
// clex:planned (no build without /build) ---

func TestIdeaPlansThroughGate(t *testing.T) {
	stages := newFakeStages()
	stages.planResult = pipeline.PlanResult{
		EpicNumber:   90,
		IssueNumbers: []int{91, 92},
		Questions:    []pipeline.Question{{Text: "SQLite or Postgres?", Proposed: "SQLite"}},
	}
	h := newHarness(t, stages)
	h.ideaIssue(10, "widgets")
	// The epic + planned children the (fake) Plan "created".
	h.gh.seed(&gh.Issue{Number: 90, Title: "Epic: widgets", IsEpic: true, Body: pipeline.PlannedFromMarker(10)})
	h.gh.seed(&gh.Issue{Number: 91, Title: "store", State: core.StatePlanned, Meta: gh.Metadata{DependsOn: []int{90}}})
	h.gh.seed(&gh.Issue{Number: 92, Title: "api", State: core.StatePlanned, Meta: gh.Metadata{DependsOn: []int{90, 91}}})
	h.runDaemon(t)

	if !waitFor(2*time.Second, func() bool { return h.gh.stateOf(10) == core.StatePlanned }) {
		t.Fatalf("idea state = %s, want %s", h.gh.stateOf(10), core.StatePlanned)
	}
	// idea → researching → planned, exactly.
	if tr := h.gh.transitionsFor(10); len(tr) != 2 || tr[0] != core.StateResearching || tr[1] != core.StatePlanned {
		t.Errorf("idea transitions = %v, want [researching planned]", tr)
	}
	// Gate summary posted with the epic, children, question, and /build hint.
	for _, want := range []string{"plan ready: epic #90", "#91 store", "#92 api", "SQLite or Postgres?", "/build 90"} {
		if !waitFor(time.Second, func() bool { return h.tg.sentContains(want) }) {
			t.Errorf("gate summary missing %q", want)
		}
	}
	// The idea links its epic in a comment.
	if !waitFor(time.Second, func() bool {
		for _, c := range h.gh.commentsOf(10) {
			if contains(c, "epic #90") {
				return true
			}
		}
		return false
	}) {
		t.Error("idea missing planned-as-epic comment")
	}
	// The gate holds: children stay planned, no build dispatches.
	time.Sleep(100 * time.Millisecond)
	if got := len(stages.buildCalls); got != 0 {
		t.Errorf("builds dispatched through closed gate: %d", got)
	}
	if n := stages.planCallCount(); n != 1 {
		t.Errorf("plan ran %d times, want 1", n)
	}
}

// --- Criterion: a failing plan reverts to clex:idea once (no hot loop) and
// /steer re-arms it ---

func TestPlanFailureRevertsWithoutLooping(t *testing.T) {
	stages := newFakeStages()
	stages.planErr = errors.New("planner exploded")
	h := newHarness(t, stages)
	h.ideaIssue(10, "widgets")
	h.runDaemon(t)

	if !waitFor(2*time.Second, func() bool { return h.tg.sentContains("planning failed") }) {
		t.Fatal("no failure notification")
	}
	if !waitFor(time.Second, func() bool { return h.gh.stateOf(10) == core.StateIdea }) {
		t.Fatalf("idea state = %s, want reverted to %s", h.gh.stateOf(10), core.StateIdea)
	}
	// Several reconcile ticks later planning must NOT have re-run.
	time.Sleep(120 * time.Millisecond)
	if n := stages.planCallCount(); n != 1 {
		t.Fatalf("plan ran %d times after failure, want 1", n)
	}

	// /steer the idea: deliberate retry → plans again.
	stages.mu.Lock()
	stages.planErr = nil
	stages.planResult = pipeline.PlanResult{EpicNumber: 90}
	stages.mu.Unlock()
	h.tg.command(context.Background(), "steer", "10 keep it simple")
	if !waitFor(2*time.Second, func() bool { return stages.planCallCount() == 2 }) {
		t.Fatalf("plan did not re-run after steer; calls = %d", stages.planCallCount())
	}
}

// --- Criterion: crash recovery reverts orphaned clex:researching to clex:idea
// and a resumed plan is handed the already-created epic ---

func TestRecoverResearchingResumesExistingEpic(t *testing.T) {
	stages := newFakeStages()
	stages.planResult = pipeline.PlanResult{EpicNumber: 90}
	h := newHarness(t, stages)
	// Orphaned mid-plan: the idea is researching and the epic already exists
	// with the provenance marker (crash after epic creation).
	h.gh.seed(&gh.Issue{Number: 10, Title: "widgets", State: core.StateResearching, AuthorLogin: "acme"})
	h.gh.seed(&gh.Issue{Number: 90, Title: "Epic: widgets", IsEpic: true, Body: "prd\n\n" + pipeline.PlannedFromMarker(10)})
	h.runDaemon(t)

	if !waitFor(2*time.Second, func() bool { return h.gh.stateOf(10) == core.StatePlanned }) {
		t.Fatalf("idea state = %s, want %s after recovery replan", h.gh.stateOf(10), core.StatePlanned)
	}
	stages.mu.Lock()
	defer stages.mu.Unlock()
	if len(stages.planExisting) == 0 || stages.planExisting[0] != 90 {
		t.Fatalf("plan existingEpic = %v, want [90] (resume, not duplicate)", stages.planExisting)
	}
}
