package pipeline

import (
	"context"
	"strings"
	"testing"

	"github.com/reissui/clex/internal/core"
	"github.com/reissui/clex/internal/gh"
	"github.com/reissui/clex/internal/registry"
)

// samplePlan is a well-formed planner output with two child issues.
const samplePlan = `preamble chatter the parser should ignore
===CLEX-PLAN v1===
===EPIC===
TITLE: Widget subsystem
Build the widget subsystem.
It has two parts.
===ISSUE===
TITLE: Add widget store
DIFFICULTY: standard
TOUCHES: internal/widget/**
VERIFY: go test ./internal/widget/...
BODY:
Implement the widget store.
Acceptance: tests pass.
===ISSUE===
TITLE: Add widget API
DIFFICULTY: standard
DEPENDS-ON: 1
TOUCHES: internal/api/**
VERIFY: go test ./internal/api/...
BODY:
Implement the widget API on top of the store.
===QUESTION===
TEXT: Should the store be persistent?
PROPOSED: Yes, back it with SQLite.
===END===`

// TestParsePlanOutput checks the parser extracts epic, ordered issues with
// metadata, dependency ordinals, and batched questions.
func TestParsePlanOutput(t *testing.T) {
	out, err := parsePlanOutput(samplePlan)
	if err != nil {
		t.Fatalf("parsePlanOutput: %v", err)
	}
	if out.EpicTitle != "Widget subsystem" {
		t.Errorf("EpicTitle = %q", out.EpicTitle)
	}
	if !strings.Contains(out.EpicBody, "two parts") {
		t.Errorf("EpicBody missing content: %q", out.EpicBody)
	}
	if len(out.Issues) != 2 {
		t.Fatalf("issues = %d, want 2", len(out.Issues))
	}
	if out.Issues[0].Title != "Add widget store" || out.Issues[0].Difficulty != core.DifficultyStandard {
		t.Errorf("issue[0] = %+v", out.Issues[0])
	}
	if out.Issues[0].Verify != "go test ./internal/widget/..." {
		t.Errorf("issue[0].Verify = %q", out.Issues[0].Verify)
	}
	if len(out.Issues[1].DependsOnOrdinals) != 1 || out.Issues[1].DependsOnOrdinals[0] != 1 {
		t.Errorf("issue[1] deps = %v, want [1]", out.Issues[1].DependsOnOrdinals)
	}
	if !strings.Contains(out.Issues[1].Body, "on top of the store") {
		t.Errorf("issue[1] body = %q", out.Issues[1].Body)
	}
	if len(out.Questions) != 1 || !strings.Contains(out.Questions[0].Proposed, "SQLite") {
		t.Errorf("questions = %+v", out.Questions)
	}
}

// TestComposeIssueBodyRoundTrip: the rendered metadata block round-trips through
// gh.ParseMetadata, and re-composing is idempotent (no stacked blocks).
func TestComposeIssueBodyRoundTrip(t *testing.T) {
	ci := ChildIssue{
		Title:      "x",
		Body:       "do the thing",
		Difficulty: core.DifficultyComplex,
		Touches:    []string{"internal/x/**", "go.mod"},
		Verify:     "go test ./internal/x/...",
	}
	body := composeIssueBody(ci.Body, []int{3, 4}, ci)
	meta := gh.ParseMetadata(body)
	if meta.Difficulty != core.DifficultyComplex {
		t.Errorf("Difficulty = %q", meta.Difficulty)
	}
	if len(meta.DependsOn) != 2 || meta.DependsOn[0] != 3 || meta.DependsOn[1] != 4 {
		t.Errorf("DependsOn = %v, want [3 4]", meta.DependsOn)
	}
	if meta.Verify != "go test ./internal/x/..." {
		t.Errorf("Verify = %q", meta.Verify)
	}
	if len(meta.Touches) != 2 {
		t.Errorf("Touches = %v", meta.Touches)
	}
	// Idempotent re-compose: feeding the composed body back must not stack a
	// second fenced block.
	body2 := composeIssueBody(body, []int{3, 4}, ci)
	if strings.Count(body2, "```clex") != 1 {
		t.Errorf("re-compose stacked metadata blocks: %q", body2)
	}
}

// planFixture wires a Plan-ready pipeline. plannerScripts drives the planner
// model across its calls; lintResults maps a child TITLE to the lint output
// string it should produce.
func planFixture(t *testing.T, plannerScripts [][]core.Event, lintFn func(title string) string) (*Pipeline, *fakeGH, *scriptedRunner, *scriptedLint) {
	t.Helper()
	ghc := newFakeGH()
	planner := &scriptedRunner{scripts: plannerScripts}
	lint := &scriptedLint{fn: lintFn}
	router := newFakeRouter()
	router.available[core.RolePlan] = []registry.RunOption{opt("opus-4-8", "claude", "top")}
	router.available[core.RoleLint] = []registry.RunOption{opt("sonnet-5", "claude", "mid")}
	fac := newFakeFactory(nil)
	fac.byModel["opus-4-8"] = planner
	fac.byModel["sonnet-5"] = lint
	p := New(Deps{
		GH:      ghc,
		Router:  router,
		Runners: fac,
	}, Config{Repo: testRepo(), RepoDir: t.TempDir(), TopTier: topTierIDs})
	return p, ghc, planner, lint
}

// scriptedLint is a runner whose output depends on the issue number in the
// prompt, looked up via fn against the seeded issue titles. It records how many
// times each issue was linted.
type scriptedLint struct {
	fn        func(title string) string
	byNumber  map[int]string // issue number -> title, populated by the test
	callCount map[int]int
}

func (s *scriptedLint) Run(ctx context.Context, task core.Task, dir string) (<-chan core.Event, error) {
	if s.callCount == nil {
		s.callCount = map[int]int{}
	}
	s.callCount[task.Issue]++
	title := ""
	if s.byNumber != nil {
		title = s.byNumber[task.Issue]
	}
	out := s.fn(title)
	ch := make(chan core.Event, 2)
	ch <- core.Event{Type: core.EventText, Text: out}
	ch <- core.Event{Type: core.EventResult}
	close(ch)
	return ch, nil
}

func (s *scriptedLint) Probe(ctx context.Context) (core.Availability, error) {
	return core.Availability{Healthy: true}, nil
}

// TestPlanCreatesEpicAndChildren: a clean plan creates the epic and both
// children with dependency wiring, and emits the batched questions.
func TestPlanCreatesEpicAndChildren(t *testing.T) {
	p, ghc, _, lint := planFixture(t,
		[][]core.Event{textThenResult(samplePlan)},
		func(string) string { return "all good\nLINT: PASS" })
	_ = lint

	idea := &gh.Issue{Number: 10, Title: "widgets", Body: "I want widgets", State: core.StateResearching}
	res, err := p.Plan(bg(), idea, PlanInputs{}, 0)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if res.EpicNumber == 0 {
		t.Error("no epic created")
	}
	if len(res.IssueNumbers) != 2 {
		t.Fatalf("children = %d, want 2", len(res.IssueNumbers))
	}
	if res.Bounced {
		t.Error("clean plan should not bounce")
	}
	if len(res.LintFailures) != 0 {
		t.Errorf("unexpected lint failures: %v", res.LintFailures)
	}
	if len(res.Questions) != 1 {
		t.Errorf("questions = %d, want 1", len(res.Questions))
	}
	// Epic carries the clex:epic marker and the idea provenance marker (crash
	// recovery scans for it to resume instead of duplicating).
	epic := ghc.issues[res.EpicNumber]
	if !epic.IsEpic {
		t.Error("epic missing clex:epic marker")
	}
	if !strings.Contains(epic.Body, PlannedFromMarker(idea.Number)) {
		t.Errorf("epic body missing %q", PlannedFromMarker(idea.Number))
	}
	// Children are gated at clex:planned — nothing builds before /build.
	for _, n := range res.IssueNumbers {
		if st := ghc.issues[n].State; st != core.StatePlanned {
			t.Errorf("child #%d state = %s, want %s", n, st, core.StatePlanned)
		}
	}
	// Every child links its epic; the second child also depends on the first
	// (real numbers wired in).
	child1 := ghc.issues[res.IssueNumbers[0]]
	if len(child1.Meta.DependsOn) != 1 || child1.Meta.DependsOn[0] != res.EpicNumber {
		t.Errorf("child1 DependsOn = %v, want [%d]", child1.Meta.DependsOn, res.EpicNumber)
	}
	child2 := ghc.issues[res.IssueNumbers[1]]
	want := []int{res.EpicNumber, res.IssueNumbers[0]}
	if len(child2.Meta.DependsOn) != 2 || child2.Meta.DependsOn[0] != want[0] || child2.Meta.DependsOn[1] != want[1] {
		t.Errorf("child2 DependsOn = %v, want %v", child2.Meta.DependsOn, want)
	}
}

// TestPlanBouncesLintFailureExactlyOnce is the core acceptance criterion: a
// lint-failing child bounces the plan back to the planner EXACTLY ONCE, then the
// residual failure is surfaced (not retried again).
func TestPlanBouncesLintFailureExactlyOnce(t *testing.T) {
	// The planner emits the same plan both times; lint always fails the API
	// issue, so after the single bounce it must still be surfaced.
	p, ghc, planner, lint := planFixture(t,
		[][]core.Event{textThenResult(samplePlan), textThenResult(samplePlan)},
		func(title string) string {
			if strings.Contains(title, "API") {
				return "missing acceptance criteria detail"
			}
			return "LINT: PASS"
		})

	// Wire the lint's number→title map after creation by intercepting via a
	// pre-seed: we know CreateIssue assigns sequential numbers starting at 101.
	lint.byNumber = map[int]string{101: "Add widget store", 102: "Add widget API"}

	idea := &gh.Issue{Number: 10, Title: "widgets", Body: "I want widgets", State: core.StateResearching}
	res, err := p.Plan(bg(), idea, PlanInputs{}, 0)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if !res.Bounced {
		t.Error("expected the plan to bounce once")
	}
	// The planner must have been called EXACTLY twice: initial + one bounce.
	if planner.calls != 2 {
		t.Errorf("planner calls = %d, want exactly 2 (initial + one bounce)", planner.calls)
	}
	// The failing API issue is surfaced as a residual lint failure.
	if len(res.LintFailures) != 1 {
		t.Fatalf("residual LintFailures = %d, want 1", len(res.LintFailures))
	}
	if res.LintFailures[0].Issue != 102 {
		t.Errorf("surfaced failure issue = %d, want 102", res.LintFailures[0].Issue)
	}
	// The API issue was linted exactly twice (initial + re-lint after bounce),
	// proving no second bounce happened.
	if got := lint.callCount[102]; got != 2 {
		t.Errorf("API issue linted %d times, want 2 (initial + one re-lint, no 2nd bounce)", got)
	}
	_ = ghc
}

// TestPlanIdempotentResume: re-running Plan with an existing epic number does
// not create a second epic or children (crash recovery).
func TestPlanIdempotentResume(t *testing.T) {
	p, ghc, planner, _ := planFixture(t,
		[][]core.Event{textThenResult(samplePlan)},
		func(string) string { return "LINT: PASS" })
	// Seed an existing epic.
	ghc.seedIssue(&gh.Issue{Number: 200, Title: "Epic", IsEpic: true, State: core.StateEpic})

	res, err := p.Plan(bg(), &gh.Issue{Number: 10, Title: "widgets", Body: "x"}, PlanInputs{}, 200)
	if err != nil {
		t.Fatalf("Plan resume: %v", err)
	}
	if res.EpicNumber != 200 {
		t.Errorf("EpicNumber = %d, want reused 200", res.EpicNumber)
	}
	if planner.calls != 0 {
		t.Errorf("planner invoked %d times on resume; want 0", planner.calls)
	}
	if len(ghc.created) != 0 {
		t.Errorf("resume created %d issues; want 0", len(ghc.created))
	}
}
