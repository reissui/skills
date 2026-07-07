package pipeline

import (
	"context"

	"github.com/reissui/clex/internal/core"
	"github.com/reissui/clex/internal/gh"
	"github.com/reissui/clex/internal/registry"
)

// SetVerifierForTest overrides the verification-command executor with a test
// double. Test-only (compiled only under _test.go), so production code cannot
// bypass the real shell verifier.
func (p *Pipeline) SetVerifierForTest(v verifyRunner) { p.testVerify = v }

// verifyFuncForTest adapts a func to the unexported verifyRunner interface so
// tests in this package can script verification outcomes.
type verifyFuncForTest func(ctx context.Context, dir, command string) error

func (f verifyFuncForTest) run(ctx context.Context, dir, command string) error {
	return f(ctx, dir, command)
}

// ResolveVerificationForTest exposes the trust decision for direct assertion.
func (p *Pipeline) ResolveVerificationForTest(iss *gh.Issue) VerificationPlan {
	return p.resolveVerification(iss)
}

// SelectReviewerForTest exposes reviewer selection for direct assertion.
func (p *Pipeline) SelectReviewerForTest(author core.Model, opts []registry.RunOption) (ReviewerChoice, error) {
	return p.selectReviewer(author, opts)
}

// ParsePlanOutputForTest exposes the plan parser.
func ParsePlanOutputForTest(text string) (PlanOutput, error) { return parsePlanOutput(text) }

// ComposeIssueBodyForTest exposes body composition for round-trip assertions.
func ComposeIssueBodyForTest(body string, deps []int, ci ChildIssue) string {
	return composeIssueBody(body, deps, ci)
}

// LandedCountForTest exposes the landed-issue counter used by the readiness gate.
func (p *Pipeline) LandedCountForTest(ctx context.Context, issues []int) (int, error) {
	return p.landedCount(ctx, issues)
}
