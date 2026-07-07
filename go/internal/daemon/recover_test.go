package daemon

import (
	"context"
	"testing"
	"time"

	"github.com/reissui/clex/internal/core"
	"github.com/reissui/clex/internal/gh"
)

// --- Criterion: kill mid-build + restart → state reconstructed from labels,
// orphan reverted with comment.
//
// A clex:building issue with no live session is exactly the post-crash state:
// the process that owned the runner is gone, but the GitHub label still says
// "building". On startup recover() must revert it to clex:approved and post an
// explanatory comment, purely from labels.
func TestCrashRecoveryRevertsOrphan(t *testing.T) {
	stages := newFakeStages()
	// Hold any subsequent (re-)build so recovery is observed before a new
	// dispatch changes the label back to building.
	stages.holdBuild(50)
	h := newHarness(t, stages)

	// Seed an orphaned building issue (as if a prior daemon crashed mid-build).
	h.gh.seed(&gh.Issue{
		Number:      50,
		Title:       "orphan",
		AuthorLogin: "acme",
		State:       core.StateBuilding,
		Meta:        gh.Metadata{Touches: []string{"x/**"}, Difficulty: core.DifficultyStandard},
	})

	// Run recovery directly (deterministic, no dependence on loop timing).
	if err := h.d.recover(context.Background()); err != nil {
		t.Fatalf("recover: %v", err)
	}

	// The orphan reverted to approved.
	if got := h.gh.stateOf(50); got != core.StateApproved {
		t.Fatalf("orphan state = %s, want approved", got)
	}
	// A comment explaining the revert was posted.
	comments := h.gh.commentsOf(50)
	if len(comments) == 0 {
		t.Fatal("expected an orphan-revert comment")
	}
	found := false
	for _, c := range comments {
		if contains(c, "reverted to") && contains(c, "clex:approved") {
			found = true
		}
	}
	if !found {
		t.Fatalf("orphan comment missing revert explanation; got %v", comments)
	}
}

// TestCrashRecoveryThenRedispatch proves the full loop: after recovery reverts
// an orphan to approved, the normal reconcile path re-dispatches it (the
// pipeline's idempotent Build reuses the preserved worktree). Here the fake
// build simply succeeds, so the issue advances past building again.
func TestCrashRecoveryThenRedispatch(t *testing.T) {
	stages := newFakeStages()
	h := newHarness(t, stages)
	h.gh.seed(&gh.Issue{
		Number:      51,
		Title:       "orphan",
		AuthorLogin: "acme",
		State:       core.StateBuilding,
		Meta:        gh.Metadata{Touches: []string{"y/**"}, Difficulty: core.DifficultyStandard},
	})

	h.runDaemon(t)

	// After startup recovery + reconcile, the issue should have transitioned
	// building→approved (recovery) then approved→building (re-dispatch).
	if !waitFor(2*time.Second, func() bool {
		trans := h.gh.transitionsFor(51)
		return containsState(trans, core.StateApproved) && countState(trans, core.StateBuilding) >= 1
	}) {
		t.Fatalf("expected recovery revert then re-dispatch; transitions=%v", h.gh.transitionsFor(51))
	}
}

func containsState(ss []core.State, want core.State) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}

func countState(ss []core.State, want core.State) int {
	n := 0
	for _, s := range ss {
		if s == want {
			n++
		}
	}
	return n
}
