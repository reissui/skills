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

// plannedChild seeds a clex:planned child of an epic.
func (h *harness) plannedChild(number, epic int, deps []int, title string) {
	h.gh.seed(&gh.Issue{
		Number:      number,
		Title:       title,
		AuthorLogin: "acme",
		State:       core.StatePlanned,
		Meta:        gh.Metadata{DependsOn: append([]int{epic}, deps...), Difficulty: core.DifficultyStandard},
	})
}

// --- Criterion: /plan <text> files a clex:idea issue that the daemon then
// plans ---

func TestPlanCommandFilesAndPlans(t *testing.T) {
	stages := newFakeStages()
	stages.planResult = pipeline.PlanResult{EpicNumber: 90}
	h := newHarness(t, stages)
	h.runDaemon(t)
	ctx := context.Background()

	h.tg.command(ctx, "plan", "add dark mode\nusers keep asking for it")
	if !waitFor(time.Second, func() bool { return h.tg.sentContains("idea #1 filed: add dark mode") }) {
		t.Fatalf("/plan reply = %q", h.tg.lastLine())
	}
	iss, err := h.gh.GetIssue(ctx, testRepo, 1)
	if err != nil {
		t.Fatalf("idea issue not created: %v", err)
	}
	if iss.State != core.StateIdea && iss.State != core.StateResearching && iss.State != core.StatePlanned {
		t.Fatalf("idea state = %q", iss.State)
	}
	// The daemon picks it up and plans it.
	if !waitFor(2*time.Second, func() bool { return h.gh.stateOf(1) == core.StatePlanned }) {
		t.Fatalf("filed idea never planned; state = %s", h.gh.stateOf(1))
	}
}

// --- Criterion: bare /plan without a conversation explains itself ---

func TestBarePlanWithoutChatIsUsage(t *testing.T) {
	stages := newFakeStages()
	h := newHarness(t, stages)
	h.runDaemon(t)

	h.tg.command(context.Background(), "plan", "")
	if !waitFor(time.Second, func() bool { return h.tg.sentContains("usage: /plan") }) {
		t.Fatalf("bare /plan reply = %q", h.tg.lastLine())
	}
}

// --- Criterion: /build <epic#> opens the gate — every planned child is
// approved and the scheduler dispatches them in dependency order ---

func TestBuildCommandApprovesEpicChildren(t *testing.T) {
	stages := newFakeStages()
	h := newHarness(t, stages)
	h.gh.seed(&gh.Issue{Number: 90, Title: "Epic: widgets", IsEpic: true, AuthorLogin: "acme"})
	h.plannedChild(91, 90, nil, "store")
	h.plannedChild(92, 90, []int{91}, "api")
	h.runDaemon(t)
	ctx := context.Background()

	h.tg.command(ctx, "build", "90")
	if !waitFor(time.Second, func() bool { return h.tg.sentContains("epic #90: 2 issues approved") }) {
		t.Fatalf("/build reply = %q", h.tg.lastLine())
	}
	// #91 (no unmet deps) builds; #92 waits on #91 — the epic dep itself must
	// NOT block anything (it stays open by design).
	if !waitFor(2*time.Second, func() bool {
		stages.mu.Lock()
		defer stages.mu.Unlock()
		return len(stages.buildCalls) >= 1 && stages.buildCalls[0] == 91
	}) {
		t.Fatal("first build never dispatched (epic dep wrongly blocking?)")
	}
}

// --- Criterion: /build on a non-planned issue refuses tersely ---

func TestBuildCommandRefusesUnplanned(t *testing.T) {
	stages := newFakeStages()
	// Planning fails, so the idea reverts to (and stays) clex:idea — the state
	// this test needs /build to refuse.
	stages.planErr = errors.New("nope")
	h := newHarness(t, stages)
	h.ideaIssue(10, "widgets")
	h.gh.seed(&gh.Issue{Number: 90, Title: "Epic: empty", IsEpic: true, AuthorLogin: "acme"})
	h.runDaemon(t)
	ctx := context.Background()

	if !waitFor(2*time.Second, func() bool { return stages.planCallCount() == 1 && h.gh.stateOf(10) == core.StateIdea }) {
		t.Fatal("idea never settled back to clex:idea")
	}
	h.tg.command(ctx, "build", "10")
	if !waitFor(time.Second, func() bool { return h.tg.sentContains("/build approves planned issues") }) {
		t.Fatalf("/build on idea reply = %q", h.tg.lastLine())
	}
	h.tg.command(ctx, "build", "90")
	if !waitFor(time.Second, func() bool { return h.tg.sentContains("no planned children") }) {
		t.Fatalf("/build on empty epic reply = %q", h.tg.lastLine())
	}
}

// --- Criterion: /merge merges the named PR and reports failures verbatim ---

func TestMergeCommand(t *testing.T) {
	stages := newFakeStages()
	h := newHarness(t, stages)
	h.runDaemon(t)
	ctx := context.Background()

	h.tg.command(ctx, "merge", "77")
	if !waitFor(time.Second, func() bool { return h.tg.sentContains("PR #77 merged") }) {
		t.Fatalf("/merge reply = %q", h.tg.lastLine())
	}
	if got := h.gh.merged(); len(got) != 1 || got[0] != 77 {
		t.Fatalf("merged PRs = %v, want [77]", got)
	}

	h.gh.mu.Lock()
	h.gh.mergeErr = errors.New("required status checks failing")
	h.gh.mu.Unlock()
	h.tg.command(ctx, "merge", "78")
	if !waitFor(time.Second, func() bool { return h.tg.sentContains("merge PR #78: required status checks failing") }) {
		t.Fatalf("/merge failure reply = %q", h.tg.lastLine())
	}
}
