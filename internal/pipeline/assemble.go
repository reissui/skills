package pipeline

import (
	"context"
	"fmt"
	"strings"

	"github.com/reissui/clex/internal/core"
	"github.com/reissui/clex/internal/workspace"
)

// IssueVerification is the recorded per-issue verification result carried into
// the assemble summary (spec: Review policy — the final PR summary lists
// "per-issue verification results").
type IssueVerification struct {
	Issue   int
	Command string
	// Passed reports whether the issue's verification command passed during its
	// build.
	Passed bool
	// Substituted reports whether the repo default was used instead of the body
	// command (untrusted author).
	Substituted bool
	// ReviewerFlags are any reviewer notes worth surfacing to the human on the
	// final PR.
	ReviewerFlags string
}

// AssembleInput carries the material the assemble stage summarizes.
type AssembleInput struct {
	// EpicTitle is the epic issue title, used for the final PR title.
	EpicTitle string
	// Children are the landed child issue numbers.
	Children []int
	// Verifications are the per-issue verification results to summarize.
	Verifications []IssueVerification
	// Summary is a short "what changed" epic-level description.
	Summary string
	// AutoMerge enables auto-merging the final PR. It is OFF by default and must
	// be explicitly set per epic (spec: Workspace manager & branch model —
	// "Auto-merge of this final PR only if explicitly enabled per epic or in
	// config (default off)").
	AutoMerge bool
}

// AssembleResult is the typed outcome of the assemble stage.
type AssembleResult struct {
	// PRNumber is THE single PR opened from the integration branch to main.
	PRNumber int
	// Merged reports whether the final PR was auto-merged (only when AutoMerge
	// was explicitly enabled).
	Merged bool
	// MergeSHA is set when Merged is true.
	MergeSHA string
}

// Assemble runs the epic assembly once all children have landed: it rebases the
// integration branch onto main, runs epic-level verification, and opens exactly
// ONE PR from clex/epic-<n> to main with a top-tier summary comment (what
// changed, per-issue verification results, reviewer flags). It auto-merges that
// PR only when in.AutoMerge is explicitly true; otherwise the PR is left as the
// owner's manual gate.
//
// allLanded reports whether every child issue has merged into the integration
// branch; when false Assemble returns ErrNotReady without side effects.
//
// epicVerify is the epic-level verification command (repo default or an epic
// override supplied by the caller); it runs against the primary checkout's
// integration branch state.
//
// Idempotency: if a PR from the integration branch to main already exists
// (existingPRNumber != 0), Assemble does not open a second one — it reuses it,
// which is what guarantees "exactly one PR to main" across crash/re-run.
func (p *Pipeline) Assemble(ctx context.Context, epicNum int, allLanded bool, in AssembleInput, epicVerify string, existingPRNumber int) (AssembleResult, error) {
	if !allLanded {
		return AssembleResult{}, fmt.Errorf("%w: epic #%d has unlanded children", ErrNotReady, epicNum)
	}

	// 1. Rebase the integration branch onto main so the final PR is a clean
	//    fast-forwardable delta (spec: "the integration branch rebases onto
	//    main"). The workspace manager aborts a conflicting rebase and returns
	//    ErrConflict, leaving the branch clean.
	if err := p.deps.WS.RebaseEpicOntoMain(ctx, p.cfg.RepoDir, epicNum); err != nil {
		return AssembleResult{}, fmt.Errorf("assemble: rebase epic onto main: %w", err)
	}

	// 2. Run epic-level verification against the integration branch.
	if v := strings.TrimSpace(epicVerify); v != "" {
		if err := wrapVerifyErr(p.verifier().run(ctx, p.cfg.RepoDir, v)); err != nil {
			return AssembleResult{}, fmt.Errorf("assemble: epic verification: %w", err)
		}
	}

	// 3. Open exactly ONE PR to main (or reuse the existing one).
	res := AssembleResult{PRNumber: existingPRNumber}
	if res.PRNumber == 0 {
		title := in.EpicTitle
		if title == "" {
			title = fmt.Sprintf("Epic #%d", epicNum)
		}
		body := fmt.Sprintf("Final integration PR for epic #%d.", epicNum)
		pr, err := p.deps.GH.OpenPR(ctx, p.cfg.Repo,
			title,
			workspace.EpicBranch(epicNum),
			workspace.MainBranch,
			body)
		if err != nil {
			return res, fmt.Errorf("assemble: open final PR: %w", err)
		}
		res.PRNumber = pr.Number
	}

	// 4. Post the top-tier summary comment on the final PR.
	if err := p.deps.GH.Comment(ctx, p.cfg.Repo, res.PRNumber, assembleSummary(epicNum, in)); err != nil {
		return res, fmt.Errorf("assemble: post summary: %w", err)
	}

	// 5. Auto-merge ONLY if explicitly enabled (default OFF).
	if in.AutoMerge {
		sha, err := p.deps.GH.MergePR(ctx, p.cfg.Repo, res.PRNumber, "merge", "")
		if err != nil {
			return res, fmt.Errorf("assemble: auto-merge final PR: %w", err)
		}
		res.Merged = true
		res.MergeSHA = sha
	}
	return res, nil
}

// assembleSummary renders the top-tier summary comment for the final PR: what
// changed, per-issue verification results, and any reviewer flags.
func assembleSummary(epicNum int, in AssembleInput) string {
	var b strings.Builder
	fmt.Fprintf(&b, "## clex epic #%d — integration summary\n\n", epicNum)
	if s := strings.TrimSpace(in.Summary); s != "" {
		b.WriteString(s)
		b.WriteString("\n\n")
	}
	b.WriteString("### Per-issue verification\n\n")
	if len(in.Verifications) == 0 {
		b.WriteString("_no per-issue verification recorded_\n")
	}
	for _, v := range in.Verifications {
		status := "passed"
		if !v.Passed {
			status = "FAILED"
		}
		fmt.Fprintf(&b, "- #%d: %s — `%s`", v.Issue, status, v.Command)
		if v.Substituted {
			b.WriteString(" (repo-default command; issue author untrusted)")
		}
		b.WriteString("\n")
		if f := strings.TrimSpace(v.ReviewerFlags); f != "" {
			fmt.Fprintf(&b, "  - reviewer: %s\n", oneLine(f))
		}
	}
	return b.String()
}

// landedCount is a small helper the daemon can use to decide readiness; it
// reports how many of the given issues are closed/merged. It reads issue state
// via the GitHub client. Kept here so the "all children landed" gate has a
// tested primitive even though the daemon owns the scheduling decision.
func (p *Pipeline) landedCount(ctx context.Context, issues []int) (int, error) {
	landed := 0
	for _, num := range issues {
		iss, err := p.deps.GH.GetIssue(ctx, p.cfg.Repo, num)
		if err != nil {
			return landed, fmt.Errorf("assemble: get #%d: %w", num, err)
		}
		// An issue that has left the pipeline states (no clex:* state and not an
		// epic) is treated as landed; the daemon closes issues on merge.
		if !core.IsPipelineState(iss.State) {
			landed++
		}
	}
	return landed, nil
}
