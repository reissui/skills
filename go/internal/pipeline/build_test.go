package pipeline

import (
	"context"
	"errors"
	"testing"

	"github.com/reissui/clex/internal/core"
	"github.com/reissui/clex/internal/gh"
)

// buildFixture wires a Build-ready pipeline. verifyErr scripts the verification
// outcome; runnerErr scripts a runner transport failure.
func buildFixture(t *testing.T, verifyErr, runnerErr error) (*Pipeline, *fakeGH, *fakeWS, *fakeSkills) {
	t.Helper()
	ghc := newFakeGH()
	ws := newFakeWS(t.TempDir())
	sk := &fakeSkills{}
	runner := &scriptedRunner{scripts: [][]core.Event{textThenResult("built it")}, runErr: runnerErr}
	router := newFakeRouter()
	router.build = buildDecisionFor("qwen3-coder", "ollama")
	p := New(Deps{
		GH:      ghc,
		WS:      ws,
		Router:  router,
		Skills:  sk,
		Runners: newFakeFactory(runner),
	}, Config{Repo: testRepo(), RepoDir: t.TempDir(), Owner: "reissui", DefaultVerify: "go test ./..."})
	p.SetVerifierForTest(verifyFuncForTest(func(context.Context, string, string) error { return verifyErr }))
	return p, ghc, ws, sk
}

func buildIssue() *gh.Issue {
	return &gh.Issue{
		Number:      7,
		Title:       "Add rate limiter",
		Body:        "criteria",
		AuthorLogin: "reissui",
		State:       core.StateBuilding,
		Meta:        gh.Metadata{Verify: "go test ./internal/rl/...", Difficulty: core.DifficultyStandard, Touches: []string{"internal/rl/**"}},
	}
}

// TestBuildSuccess: happy path creates worktree, injects skills, runs, verifies,
// opens a PR to the integration branch, and moves the issue to review.
func TestBuildSuccess(t *testing.T) {
	p, ghc, ws, sk := buildFixture(t, nil, nil)
	issue := buildIssue()
	ghc.seedIssue(issue)

	res, err := p.Build(bg(), 1, issue, KnowledgeExcerpts{Map: "rl lives here"}, 0)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if res.PRNumber == 0 {
		t.Error("expected a PR opened")
	}
	if len(ws.epicBranches) != 1 || ws.epicBranches[0] != 1 {
		t.Errorf("epicBranches = %v, want [1]", ws.epicBranches)
	}
	if len(ws.worktrees) != 1 {
		t.Fatalf("worktrees = %d, want 1", len(ws.worktrees))
	}
	if sk.symlinked != 1 || sk.renderedMD != 1 {
		t.Errorf("skill injection not performed: symlink=%d render=%d", sk.symlinked, sk.renderedMD)
	}
	// Issue moved to review.
	if last := lastState(ghc, 7); last != core.StateReview {
		t.Errorf("final state = %q, want review", last)
	}
	// PR targets the epic integration branch.
	if len(ghc.openedPRs) != 1 || ghc.openedPRs[0].Base != "clex/epic-1" {
		t.Errorf("PR base = %v, want clex/epic-1", ghc.openedPRs)
	}
}

// TestBuildVerificationFailureReverts: verification fails → issue reverts to
// approved, failure comment posted, no PR opened, error is ErrVerificationFailed.
func TestBuildVerificationFailure(t *testing.T) {
	p, ghc, _, _ := buildFixture(t, errors.New("2 tests failed"), nil)
	issue := buildIssue()
	ghc.seedIssue(issue)

	_, err := p.Build(bg(), 1, issue, KnowledgeExcerpts{}, 0)
	if !errors.Is(err, ErrVerificationFailed) {
		t.Fatalf("err = %v, want ErrVerificationFailed", err)
	}
	if last := lastState(ghc, 7); last != core.StateApproved {
		t.Errorf("state = %q, want reverted to approved", last)
	}
	if len(ghc.comments[7]) == 0 {
		t.Error("expected a failure comment on the issue")
	}
	if len(ghc.openedPRs) != 0 {
		t.Error("no PR should be opened on verification failure")
	}
}

// TestBuildRunnerFailureReverts: the runner transport fails → revert + comment,
// error is ErrRunnerFailed.
func TestBuildRunnerFailure(t *testing.T) {
	p, ghc, _, _ := buildFixture(t, nil, errors.New("cli crashed"))
	issue := buildIssue()
	ghc.seedIssue(issue)

	_, err := p.Build(bg(), 1, issue, KnowledgeExcerpts{}, 0)
	if !errors.Is(err, ErrRunnerFailed) {
		t.Fatalf("err = %v, want ErrRunnerFailed", err)
	}
	if last := lastState(ghc, 7); last != core.StateApproved {
		t.Errorf("state = %q, want approved", last)
	}
}

// TestBuildIdempotentExistingPR: re-running after a crash where the PR already
// exists reuses it (no duplicate PR) and still lands the issue in review.
func TestBuildIdempotentExistingPR(t *testing.T) {
	p, ghc, _, _ := buildFixture(t, nil, nil)
	issue := buildIssue()
	ghc.seedIssue(issue)

	res, err := p.Build(bg(), 1, issue, KnowledgeExcerpts{}, 999) // existing PR #999
	if err != nil {
		t.Fatalf("Build re-run: %v", err)
	}
	if res.PRNumber != 999 {
		t.Errorf("PRNumber = %d, want reused 999", res.PRNumber)
	}
	if len(ghc.openedPRs) != 0 {
		t.Errorf("must not open a new PR when one exists; opened=%v", ghc.openedPRs)
	}
}

// TestBuildNoModel: router has no build-eligible model → ErrNoModel and a failure
// path (revert + comment).
func TestBuildNoModel(t *testing.T) {
	p, ghc, _, _ := buildFixture(t, nil, nil)
	// Override the router decision to "not ok".
	p.deps.Router.(*fakeRouter).build.Ok = false
	issue := buildIssue()
	ghc.seedIssue(issue)

	_, err := p.Build(bg(), 1, issue, KnowledgeExcerpts{}, 0)
	if !errors.Is(err, ErrNoModel) {
		t.Fatalf("err = %v, want ErrNoModel", err)
	}
}

// TestEscalateModelHook: Build.EscalateModel forwards to the registry ladder.
func TestEscalateModelHook(t *testing.T) {
	p, _, _, _ := buildFixture(t, nil, nil)
	p.deps.Router.(*fakeRouter).escalate = func(core.Model) (core.Model, bool) {
		return core.Model{ID: "opus-4-8"}, true
	}
	got, ok := p.EscalateModel(core.Model{ID: "qwen3-coder"})
	if !ok || got.ID != "opus-4-8" {
		t.Errorf("EscalateModel = %v,%v want opus-4-8,true", got, ok)
	}
}

// lastState returns the most recent state SetState recorded for issue n, or the
// issue's seeded state if none.
func lastState(f *fakeGH, n int) core.State {
	f.mu.Lock()
	defer f.mu.Unlock()
	var last core.State
	if iss, ok := f.issues[n]; ok {
		last = iss.State
	}
	for _, s := range f.setStates {
		if s.Issue == n {
			last = s.To
		}
	}
	return last
}
