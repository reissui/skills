package pipeline

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/reissui/clex/internal/core"
	"github.com/reissui/clex/internal/gh"
)

func assembleFixture(t *testing.T, verifyErr error) (*Pipeline, *fakeGH, *fakeWS) {
	t.Helper()
	ghc := newFakeGH()
	ws := newFakeWS(t.TempDir())
	p := New(Deps{GH: ghc, WS: ws}, Config{Repo: testRepo(), RepoDir: t.TempDir()})
	p.SetVerifierForTest(verifyFuncForTest(func(context.Context, string, string) error { return verifyErr }))
	return p, ghc, ws
}

func sampleAssembleInput(autoMerge bool) AssembleInput {
	return AssembleInput{
		EpicTitle: "Widget subsystem",
		Children:  []int{101, 102},
		Summary:   "Adds the widget store and API.",
		Verifications: []IssueVerification{
			{Issue: 101, Command: "go test ./internal/widget/...", Passed: true},
			{Issue: 102, Command: "go test ./...", Passed: true, Substituted: true, ReviewerFlags: "watch the cache size"},
		},
		AutoMerge: autoMerge,
	}
}

// TestAssembleOpensSinglePRNoAutoMerge is the acceptance criterion: exactly ONE
// PR to main with the summary comment, and auto-merge OFF by default.
func TestAssembleOpensSinglePRNoAutoMerge(t *testing.T) {
	p, ghc, ws := assembleFixture(t, nil)

	res, err := p.Assemble(bg(), 1, true, sampleAssembleInput(false), "go build ./...", 0)
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	// Exactly one PR opened, targeting main from the epic integration branch.
	if len(ghc.openedPRs) != 1 {
		t.Fatalf("opened %d PRs, want exactly 1", len(ghc.openedPRs))
	}
	pr := ghc.openedPRs[0]
	if pr.Base != "main" || pr.Head != "clex/epic-1" {
		t.Errorf("PR head/base = %s→%s, want clex/epic-1→main", pr.Head, pr.Base)
	}
	// Auto-merge OFF: PR must not be merged.
	if res.Merged || len(ghc.mergedPRs) != 0 {
		t.Errorf("auto-merge must be OFF by default; merged=%v mergedPRs=%v", res.Merged, ghc.mergedPRs)
	}
	// The integration branch was rebased onto main and epic verification ran.
	if len(ws.rebasedEpicMain) != 1 {
		t.Errorf("expected epic rebased onto main once, got %v", ws.rebasedEpicMain)
	}
	// Summary comment posted with per-issue verification + reviewer flags.
	c := ghc.comments[res.PRNumber]
	if len(c) != 1 {
		t.Fatalf("summary comments = %d, want 1", len(c))
	}
	body := c[0]
	for _, want := range []string{"#101", "#102", "per-issue verification", "watch the cache size", "repo-default command"} {
		if !strings.Contains(strings.ToLower(body), strings.ToLower(want)) {
			t.Errorf("summary missing %q\n%s", want, body)
		}
	}
}

// TestAssembleAutoMergeExplicit: when AutoMerge is explicitly true, the final PR
// is merged.
func TestAssembleAutoMergeExplicit(t *testing.T) {
	p, ghc, _ := assembleFixture(t, nil)
	res, err := p.Assemble(bg(), 1, true, sampleAssembleInput(true), "", 0)
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	if !res.Merged {
		t.Error("expected auto-merge when explicitly enabled")
	}
	if len(ghc.mergedPRs) != 1 {
		t.Errorf("mergedPRs = %v, want one", ghc.mergedPRs)
	}
}

// TestAssembleNotReady: not all children landed → ErrNotReady, no side effects.
func TestAssembleNotReady(t *testing.T) {
	p, ghc, ws := assembleFixture(t, nil)
	_, err := p.Assemble(bg(), 1, false, sampleAssembleInput(false), "go build ./...", 0)
	if !errors.Is(err, ErrNotReady) {
		t.Fatalf("err = %v, want ErrNotReady", err)
	}
	if len(ghc.openedPRs) != 0 || len(ws.rebasedEpicMain) != 0 {
		t.Error("no side effects expected when not ready")
	}
}

// TestAssembleEpicVerificationFailure: epic-level verification fails → error, no
// PR opened.
func TestAssembleEpicVerificationFailure(t *testing.T) {
	p, ghc, _ := assembleFixture(t, errors.New("build broke"))
	_, err := p.Assemble(bg(), 1, true, sampleAssembleInput(false), "go build ./...", 0)
	if !errors.Is(err, ErrVerificationFailed) {
		t.Fatalf("err = %v, want ErrVerificationFailed", err)
	}
	if len(ghc.openedPRs) != 0 {
		t.Error("no PR should open when epic verification fails")
	}
}

// TestLandedCount: counts issues that have left the pipeline states (closed/
// merged) as landed, and pipeline-state issues as not landed.
func TestLandedCount(t *testing.T) {
	ghc := newFakeGH()
	// #101 merged (no clex state), #102 still building, #103 merged.
	ghc.seedIssue(&gh.Issue{Number: 101, State: ""})
	ghc.seedIssue(&gh.Issue{Number: 102, State: core.StateBuilding})
	ghc.seedIssue(&gh.Issue{Number: 103, State: ""})
	p := New(Deps{GH: ghc}, Config{Repo: testRepo()})

	got, err := p.LandedCountForTest(bg(), []int{101, 102, 103})
	if err != nil {
		t.Fatalf("landedCount: %v", err)
	}
	if got != 2 {
		t.Errorf("landedCount = %d, want 2", got)
	}
}

// TestAssembleIdempotentExistingPR: re-running with an existing final PR does not
// open a second PR (guarantees exactly one PR to main across re-runs).
func TestAssembleIdempotentExistingPR(t *testing.T) {
	p, ghc, _ := assembleFixture(t, nil)
	ghc.seedPR(&gh.PullRequest{Number: 900, Head: "clex/epic-1", Base: "main", State: "open"})

	res, err := p.Assemble(bg(), 1, true, sampleAssembleInput(false), "", 900)
	if err != nil {
		t.Fatalf("Assemble re-run: %v", err)
	}
	if res.PRNumber != 900 {
		t.Errorf("PRNumber = %d, want reused 900", res.PRNumber)
	}
	if len(ghc.openedPRs) != 0 {
		t.Errorf("must not open a second PR to main; opened=%v", ghc.openedPRs)
	}
	// Summary still (re)posted to the existing PR — idempotent surface.
	if len(ghc.comments[900]) != 1 {
		t.Errorf("expected summary comment on existing PR")
	}
}
