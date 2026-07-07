package pipeline

import (
	"errors"
	"testing"

	"github.com/reissui/clex/internal/core"
	"github.com/reissui/clex/internal/gh"
	"github.com/reissui/clex/internal/registry"
	"github.com/reissui/clex/internal/workspace"
)

// topTierIDs is the standard top tier used across review tests.
var topTierIDs = []string{"opus-4-8", "gpt-5-5", "fable-5"}

// TestSelectReviewerAllThreeCases covers the reviewer-selection policy in all
// three spec cases (issue #15 acceptance criterion):
//  1. below-top author        → mandatory top-tier reviewer
//  2. top author, 2 providers → cross-review by a DIFFERENT top provider
//  3. top author, 1 provider  → same-provider fresh-context review-only session
func TestSelectReviewerAllThreeCases(t *testing.T) {
	claudeTop := opt("opus-4-8", "claude", "top")
	codexTop := opt("gpt-5-5", "codex", "top")
	localOpt := opt("qwen3-coder", "ollama", "local")

	tests := []struct {
		name         string
		author       core.Model
		avail        []registry.RunOption
		wantKind     ReviewerKind
		wantProvider string
		wantFresh    bool
	}{
		{
			name:         "below-top author gets mandatory top-tier reviewer",
			author:       core.Model{ID: "sonnet-5", Provider: "claude"}, // mid, not top
			avail:        []registry.RunOption{claudeTop, codexTop, localOpt},
			wantKind:     ReviewerMandatoryTop,
			wantProvider: "claude", // first top in TopTier order
		},
		{
			name:         "top author with two providers gets a different top provider",
			author:       core.Model{ID: "opus-4-8", Provider: "claude"},
			avail:        []registry.RunOption{claudeTop, codexTop},
			wantKind:     ReviewerCrossProvider,
			wantProvider: "codex", // different from author's claude
		},
		{
			name:         "top author with only one provider gets same-provider fresh review",
			author:       core.Model{ID: "opus-4-8", Provider: "claude"},
			avail:        []registry.RunOption{claudeTop}, // only claude top available
			wantKind:     ReviewerSameProviderFresh,
			wantProvider: "claude",
			wantFresh:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := New(Deps{}, Config{TopTier: topTierIDs})
			choice, err := p.selectReviewer(tt.author, tt.avail)
			if err != nil {
				t.Fatalf("selectReviewer: %v", err)
			}
			if choice.Kind != tt.wantKind {
				t.Errorf("Kind = %q, want %q", choice.Kind, tt.wantKind)
			}
			if choice.Model.Provider != tt.wantProvider {
				t.Errorf("reviewer provider = %q, want %q", choice.Model.Provider, tt.wantProvider)
			}
			if choice.FreshContext != tt.wantFresh {
				t.Errorf("FreshContext = %v, want %v", choice.FreshContext, tt.wantFresh)
			}
			// Cross-provider must never pick the author's own provider.
			if tt.wantKind == ReviewerCrossProvider && choice.Model.Provider == tt.author.Provider {
				t.Errorf("cross-provider reviewer must differ from author provider %q", tt.author.Provider)
			}
		})
	}
}

// TestSelectReviewerNoTopAvailable errors when no top-tier reviewer exists.
func TestSelectReviewerNoTopAvailable(t *testing.T) {
	p := New(Deps{}, Config{TopTier: topTierIDs})
	_, err := p.selectReviewer(core.Model{ID: "sonnet-5", Provider: "claude"},
		[]registry.RunOption{opt("qwen3-coder", "ollama", "local")})
	if !errors.Is(err, ErrNoModel) {
		t.Fatalf("err = %v, want ErrNoModel", err)
	}
}

// reviewFixture wires a Review-ready pipeline with a PR seeded open.
func reviewFixture(t *testing.T, reviewerOut string) (*Pipeline, *fakeGH, *fakeWS, *scriptedRunner) {
	t.Helper()
	ghc := newFakeGH()
	ghc.seedPR(&gh.PullRequest{Number: 55, State: "open"})
	ws := newFakeWS(t.TempDir())
	runner := &scriptedRunner{scripts: [][]core.Event{textThenResult(reviewerOut)}}
	router := newFakeRouter()
	router.available[core.RoleReview] = []registry.RunOption{opt("opus-4-8", "claude", "top"), opt("gpt-5-5", "codex", "top")}
	p := New(Deps{
		GH:      ghc,
		WS:      ws,
		Router:  router,
		Runners: newFakeFactory(runner),
	}, Config{Repo: testRepo(), TopTier: topTierIDs})
	return p, ghc, ws, runner
}

// TestReviewApproveAndMerge: reviewer approves and verification is green → the
// issue PR is merged into the integration branch.
func TestReviewApproveAndMerge(t *testing.T) {
	p, ghc, _, _ := reviewFixture(t, "Looks correct.\nREVIEW: APPROVE")
	issue := &gh.Issue{Number: 7, Body: "criteria"}
	// author below top → mandatory top reviewer path.
	author := core.Model{ID: "qwen3-coder", Provider: "ollama"}

	res, err := p.Review(bg(), 1, issue, 55, author, "diff", true)
	if err != nil {
		t.Fatalf("Review: %v", err)
	}
	if res.Outcome != ReviewApproved {
		t.Errorf("Outcome = %q, want approved", res.Outcome)
	}
	if !res.Merged {
		t.Error("expected PR merged on approve + green verification")
	}
	if len(ghc.mergedPRs) != 1 || ghc.mergedPRs[0] != 55 {
		t.Errorf("mergedPRs = %v, want [55]", ghc.mergedPRs)
	}
	if got := ghc.reviews[55]; len(got) != 1 || got[0] != "APPROVE" {
		t.Errorf("reviews = %v, want [APPROVE]", got)
	}
}

// TestReviewRebasesBeforeMerge: on the happy path the issue branch is rebased
// onto the epic integration branch before the PR merges (spec: "Issue branches
// rebase onto the integration branch before merging").
func TestReviewRebasesBeforeMerge(t *testing.T) {
	p, _, ws, _ := reviewFixture(t, "REVIEW: APPROVE")
	issue := &gh.Issue{Number: 7, Title: "add thing", Body: "criteria"}
	if _, err := p.Review(bg(), 3, issue, 55, core.Model{ID: "qwen3-coder", Provider: "ollama"}, "diff", true); err != nil {
		t.Fatalf("Review: %v", err)
	}
	if len(ws.rebasedOntoEpic) != 1 {
		t.Errorf("expected exactly one rebase-onto-epic before merge, got %d", len(ws.rebasedOntoEpic))
	}
}

// TestReviewRebaseConflictBlocksMerge: a rebase conflict blocks the merge and
// surfaces as an error (the branch is left clean by the workspace manager).
func TestReviewRebaseConflictBlocksMerge(t *testing.T) {
	p, ghc, ws, _ := reviewFixture(t, "REVIEW: APPROVE")
	ws.failAlways["RebaseOntoEpic"] = workspace.ErrConflict
	issue := &gh.Issue{Number: 7, Title: "add thing", Body: "criteria"}
	_, err := p.Review(bg(), 3, issue, 55, core.Model{ID: "qwen3-coder", Provider: "ollama"}, "diff", true)
	if err == nil {
		t.Fatal("expected an error when rebase conflicts")
	}
	if len(ghc.mergedPRs) != 0 {
		t.Errorf("must not merge when rebase conflicts; mergedPRs=%v", ghc.mergedPRs)
	}
}

// TestReviewApproveButVerificationRed: approve but verification not green → no
// merge.
func TestReviewApproveButVerificationRed(t *testing.T) {
	p, ghc, _, _ := reviewFixture(t, "REVIEW: APPROVE")
	issue := &gh.Issue{Number: 7, Body: "criteria"}
	res, err := p.Review(bg(), 1, issue, 55, core.Model{ID: "qwen3-coder", Provider: "ollama"}, "diff", false)
	if err != nil {
		t.Fatalf("Review: %v", err)
	}
	if res.Merged {
		t.Error("must NOT merge when verification is red")
	}
	if len(ghc.mergedPRs) != 0 {
		t.Errorf("mergedPRs = %v, want none", ghc.mergedPRs)
	}
}

// TestReviewRequestChanges: reviewer requests changes → request-changes review,
// no merge.
func TestReviewRequestChanges(t *testing.T) {
	p, ghc, _, _ := reviewFixture(t, "Missing tests.\nREQUEST_CHANGES")
	issue := &gh.Issue{Number: 7, Body: "criteria"}
	res, err := p.Review(bg(), 1, issue, 55, core.Model{ID: "qwen3-coder", Provider: "ollama"}, "diff", true)
	if err != nil {
		t.Fatalf("Review: %v", err)
	}
	if res.Outcome != ReviewChangesRequested {
		t.Errorf("Outcome = %q, want changes_requested", res.Outcome)
	}
	if res.Merged {
		t.Error("must not merge when changes requested")
	}
	if got := ghc.reviews[55]; len(got) != 1 || got[0] != "REQUEST_CHANGES" {
		t.Errorf("reviews = %v, want [REQUEST_CHANGES]", got)
	}
}

// TestReviewIdempotentAlreadyMerged: re-running Review on an already-merged PR
// short-circuits without re-reviewing or re-merging (crash recovery).
func TestReviewIdempotentAlreadyMerged(t *testing.T) {
	p, ghc, _, runner := reviewFixture(t, "REVIEW: APPROVE")
	ghc.prs[55].Merged = true // simulate a prior successful merge

	issue := &gh.Issue{Number: 7, Body: "criteria"}
	res, err := p.Review(bg(), 1, issue, 55, core.Model{ID: "qwen3-coder", Provider: "ollama"}, "diff", true)
	if err != nil {
		t.Fatalf("Review re-run: %v", err)
	}
	if !res.Merged || res.Outcome != ReviewApproved {
		t.Errorf("expected merged/approved short-circuit, got %+v", res)
	}
	if runner.calls != 0 {
		t.Errorf("reviewer runner was invoked %d times on an already-merged PR; want 0", runner.calls)
	}
	if len(ghc.mergedPRs) != 0 {
		t.Errorf("must not re-merge; mergedPRs=%v", ghc.mergedPRs)
	}
}
