package pipeline

import (
	"context"
	"strings"
	"testing"

	"github.com/reissui/clex/internal/core"
	"github.com/reissui/clex/internal/gh"
	"github.com/reissui/clex/internal/registry"
)

// TestLifecyclePlanBuildReviewAssemble drives an idea end-to-end through all four
// stages with fakes and asserts the whole flow, culminating in LOG.md gaining one
// line per landed issue (issue #15 lifecycle acceptance criterion).
//
// It uses a single-child plan to keep the wiring legible; the multi-child
// dependency wiring is covered in plan_test.go and the single-PR guarantee in
// assemble_test.go.
func TestLifecyclePlanBuildReviewAssemble(t *testing.T) {
	repoDir := t.TempDir()
	ghc := newFakeGH()
	ws := newFakeWS(t.TempDir())
	sk := &fakeSkills{}

	// One planner run emitting a one-issue plan.
	oneIssuePlan := `===CLEX-PLAN v1===
===EPIC===
TITLE: Rate limiting
Add a rate limiter.
===ISSUE===
TITLE: Add token bucket
DIFFICULTY: standard
TOUCHES: internal/rl/**
VERIFY: go test ./internal/rl/...
BODY:
Implement a token-bucket limiter. Acceptance: tests pass.
===END===`

	// opus-4-8 is the single top model, used for BOTH plan and the mandatory-top
	// review. Its scripted runner therefore serves two calls: first the plan
	// output, then the review verdict.
	topRunner := &scriptedRunner{scripts: [][]core.Event{
		textThenResult(oneIssuePlan),
		textThenResult("LGTM\nREVIEW: APPROVE"),
	}}
	lint := &scriptedLint{fn: func(string) string { return "LINT: PASS" }}
	builder := &scriptedRunner{scripts: [][]core.Event{textThenResult("implemented")}}

	fac := newFakeFactory(nil)
	fac.byModel["opus-4-8"] = topRunner  // plan + review (top)
	fac.byModel["sonnet-5"] = lint       // lint (mid)
	fac.byModel["qwen3-coder"] = builder // build (local)

	router := newFakeRouter()
	router.available[core.RolePlan] = []registry.RunOption{opt("opus-4-8", "claude", "top")}
	router.available[core.RoleLint] = []registry.RunOption{opt("sonnet-5", "claude", "mid")}
	router.available[core.RoleReview] = []registry.RunOption{opt("opus-4-8", "claude", "top"), opt("gpt-5-5", "codex", "top")}
	router.build = buildDecisionFor("qwen3-coder", "ollama")

	p := New(Deps{
		GH:      ghc,
		WS:      ws,
		Router:  router,
		Skills:  sk,
		Runners: fac,
	}, Config{
		Repo:          testRepo(),
		RepoDir:       repoDir,
		Owner:         "reissui",
		DefaultVerify: "go test ./...",
		TopTier:       topTierIDs,
	})
	// Build/assemble verification always green in this happy-path lifecycle.
	p.SetVerifierForTest(verifyFuncForTest(func(context.Context, string, string) error { return nil }))

	// The idea is owner-authored so the issue's own verification command is
	// trusted end-to-end.
	idea := &gh.Issue{Number: 10, Title: "rate limiting", Body: "add a limiter", AuthorLogin: "reissui", State: core.StateResearching}

	// --- PLAN ---
	plan, err := p.Plan(bg(), idea, PlanInputs{}, 0)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(plan.IssueNumbers) != 1 {
		t.Fatalf("want 1 child, got %d", len(plan.IssueNumbers))
	}
	childNum := plan.IssueNumbers[0]

	// The reviewer author will be the build model (below top) → mandatory top
	// review. Fetch the created child (carries the trusted Verify command).
	child, err := ghc.GetIssue(bg(), testRepo(), childNum)
	if err != nil {
		t.Fatal(err)
	}
	// Ensure the child is owner-authored so the trusted path is exercised (the
	// fake sets no author on created issues; set it to the owner here to model a
	// clex-authored issue).
	ghc.issues[childNum].AuthorLogin = "reissui"
	child.AuthorLogin = "reissui"

	// --- BUILD ---
	bres, err := p.Build(bg(), plan.EpicNumber, child, KnowledgeExcerpts{Map: "rl area"}, 0)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if bres.PRNumber == 0 {
		t.Fatal("build opened no PR")
	}
	// Trusted author → the issue's own command ran, no substitution.
	if bres.Verification.Substituted || bres.Verification.Command != "go test ./internal/rl/..." {
		t.Errorf("verification = %+v, want trusted body command", bres.Verification)
	}

	// --- REVIEW --- author is the local build model → mandatory top reviewer.
	rres, err := p.Review(bg(), plan.EpicNumber, child, bres.PRNumber, bres.Model, "the diff", true)
	if err != nil {
		t.Fatalf("Review: %v", err)
	}
	if rres.Reviewer.Kind != ReviewerMandatoryTop {
		t.Errorf("reviewer kind = %q, want mandatory_top", rres.Reviewer.Kind)
	}
	if !rres.Merged {
		t.Fatal("issue PR should merge into integration branch on approve+green")
	}

	// Model landed → record it in LOG.md (the knowledge-file step).
	wrote, err := AppendLog(repoDir, LogEntry{Issue: childNum, Summary: "add token bucket", Where: "internal/rl"})
	if err != nil || !wrote {
		t.Fatalf("AppendLog: wrote=%v err=%v", wrote, err)
	}

	// --- ASSEMBLE --- all (one) children landed.
	ares, err := p.Assemble(bg(), plan.EpicNumber, true, AssembleInput{
		EpicTitle: "Rate limiting",
		Children:  []int{childNum},
		Summary:   "Adds a token-bucket rate limiter.",
		Verifications: []IssueVerification{
			{Issue: childNum, Command: bres.Verification.Command, Passed: true},
		},
		AutoMerge: false, // default OFF — the owner's manual gate
	}, "go build ./...", 0)
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	if ares.Merged {
		t.Error("final PR must NOT auto-merge by default")
	}
	if len(ghc.openedPRs) != 2 {
		// one issue PR + one final PR to main
		t.Errorf("opened %d PRs total, want 2 (issue + final)", len(ghc.openedPRs))
	}

	// --- LOG.md assertion: exactly one line for the landed issue ---
	logContent := readContext(t, repoDir, logFile)
	lines := nonEmptyLines(logContent)
	if len(lines) != 1 {
		t.Fatalf("LOG.md has %d lines, want exactly 1 per landed issue:\n%s", len(lines), logContent)
	}
	if !strings.Contains(lines[0], "#"+itoa(childNum)) {
		t.Errorf("LOG.md line does not reference landed issue #%d: %q", childNum, lines[0])
	}
}

// itoa is a tiny local int→string to avoid importing strconv just for the test.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
