package pipeline

import (
	"context"
	"fmt"
	"strings"

	"github.com/reissui/clex/internal/core"
	"github.com/reissui/clex/internal/gh"
	"github.com/reissui/clex/internal/registry"
)

// reviewSkills are injected for the reviewer run (kept minimal — reviewers get
// the diff + acceptance criteria, not the repo).
var reviewSkills []string

// ReviewOutcome enumerates the reviewer's verdict.
type ReviewOutcome string

const (
	// ReviewApproved means the reviewer approved and, if verification is green,
	// the issue PR was merged into the integration branch.
	ReviewApproved ReviewOutcome = "approved"
	// ReviewChangesRequested means the reviewer requested changes; the PR is not
	// merged and the authoring runner should be re-invoked once to address them.
	ReviewChangesRequested ReviewOutcome = "changes_requested"
)

// ReviewerKind classifies why a particular reviewer was chosen, so the decision
// is testable in all three policy cases.
type ReviewerKind string

const (
	// ReviewerMandatoryTop: author was below top tier → a mandatory top-tier
	// reviewer (spec: Review policy — "Mandatory top-tier review").
	ReviewerMandatoryTop ReviewerKind = "mandatory_top"
	// ReviewerCrossProvider: top-tier author → a DIFFERENT top provider (spec:
	// "cross-review by a different top provider").
	ReviewerCrossProvider ReviewerKind = "cross_provider"
	// ReviewerSameProviderFresh: only one top provider exists → fresh-context,
	// review-only session on the same provider (spec: single-provider fallback).
	ReviewerSameProviderFresh ReviewerKind = "same_provider_fresh"
)

// ReviewerChoice is the resolved reviewer for a PR.
type ReviewerChoice struct {
	Model core.Model
	Kind  ReviewerKind
	// FreshContext is true for the single-provider fallback, signalling the
	// adapter to start a brand-new review-only session rather than resume.
	FreshContext bool
}

// ReviewResult is the typed outcome of the review stage.
type ReviewResult struct {
	Reviewer ReviewerChoice
	Outcome  ReviewOutcome
	// Findings is the reviewer's textual output posted as a PR comment.
	Findings string
	// Merged reports whether the issue PR was merged into the integration
	// branch (only on approve + green verification).
	Merged bool
	// MergeSHA is the merge commit SHA when Merged is true.
	MergeSHA string
}

// selectReviewer applies the spec's reviewer-selection policy given the author
// model and the top-tier review options currently available from the registry.
//
//   - Author below top tier → a mandatory top-tier reviewer (the first available
//     top option).
//   - Top-tier author, ≥2 top providers → a different top provider than the
//     author's.
//   - Top-tier author, exactly 1 top provider → same-provider fresh-context
//     review-only session.
//
// It returns ErrNoModel if no top-tier reviewer can be resolved at all.
func (p *Pipeline) selectReviewer(authorModel core.Model, opts []registry.RunOption) (ReviewerChoice, error) {
	tops := topOptions(opts, p.cfg.TopTier)
	if len(tops) == 0 {
		return ReviewerChoice{}, fmt.Errorf("%w: no top-tier reviewer", ErrNoModel)
	}

	if !p.isTopTier(authorModel.ID) {
		// Case 1: below-top author → mandatory top-tier reviewer.
		return ReviewerChoice{Model: tops[0].Model, Kind: ReviewerMandatoryTop}, nil
	}

	// Author is top-tier. Prefer a different provider.
	for _, o := range tops {
		if o.Model.Provider != authorModel.Provider {
			// Case 2: cross-provider review.
			return ReviewerChoice{Model: o.Model, Kind: ReviewerCrossProvider}, nil
		}
	}

	// Case 3: only the author's provider has a top model → fresh-context,
	// review-only session on the same provider. Pick a top option from that
	// provider, preferring one that is not the exact authoring model id.
	choice := tops[0]
	for _, o := range tops {
		if o.Model.ID != authorModel.ID {
			choice = o
			break
		}
	}
	return ReviewerChoice{Model: choice.Model, Kind: ReviewerSameProviderFresh, FreshContext: true}, nil
}

// Review runs the review stage for an issue PR. It selects the reviewer per
// policy, runs the reviewer on the diff + acceptance criteria (never the repo),
// posts findings as a PR comment, submits an approve/request-changes review, and
// — on approve with green verification — merges the issue PR into the epic
// integration branch.
//
// verificationGreen is the build stage's verification result for this issue
// (true when its verification command passed). Merge happens only on approve AND
// verificationGreen (spec: Review policy — merges "happen automatically once
// verification passes and the review approves").
//
// authorModel is the model that authored the PR (from the build stage). diff is
// the unified diff (fetched by the caller/adapter — the pipeline stays diff-
// scoped and does not read the repo).
//
// Idempotency: if the PR is already merged, Review returns the prior outcome
// without re-reviewing or re-merging.
func (p *Pipeline) Review(ctx context.Context, epicNum int, issue *gh.Issue, prNumber int, authorModel core.Model, diff string, verificationGreen bool) (ReviewResult, error) {
	// Idempotent short-circuit: PR already merged.
	pr, err := p.deps.GH.GetPR(ctx, p.cfg.Repo, prNumber)
	if err != nil {
		return ReviewResult{}, fmt.Errorf("review: get PR #%d: %w", prNumber, err)
	}
	if pr.Merged {
		return ReviewResult{Outcome: ReviewApproved, Merged: true}, nil
	}

	// Resolve reviewer options (review role → top tier) and select per policy.
	opts, _ := p.deps.Router.Available(core.RoleReview)
	choice, err := p.selectReviewer(authorModel, opts)
	if err != nil {
		return ReviewResult{}, err
	}
	reviewer, err := p.deps.Runners.RunnerFor(choice.Model)
	if err != nil {
		return ReviewResult{}, fmt.Errorf("review: runner for %s: %w", choice.Model.ID, err)
	}

	// Run the reviewer on the diff + acceptance criteria.
	prompt := buildReviewPrompt(ReviewContext{Issue: issue, Diff: diff, PRNumber: prNumber})
	rr, err := runToCompletion(ctx, reviewer, core.Task{
		Repo:   p.cfg.Repo.String(),
		Prompt: prompt,
		Issue:  issue.Number,
		Skills: reviewSkills,
	}, p.cfg.RepoDir)
	if err != nil {
		return ReviewResult{Reviewer: choice}, fmt.Errorf("review: reviewer run: %w", err)
	}

	res := ReviewResult{Reviewer: choice, Findings: strings.TrimSpace(rr.Text)}

	// Post findings as a PR comment (issue-level comment on the PR).
	if err := p.deps.GH.Comment(ctx, p.cfg.Repo, prNumber, reviewCommentBody(choice, rr.Text)); err != nil {
		return res, fmt.Errorf("review: post findings: %w", err)
	}

	// Decide the verdict from the reviewer's output.
	approved := reviewApproves(rr.Text)
	if !approved {
		res.Outcome = ReviewChangesRequested
		if err := p.deps.GH.ReviewPR(ctx, p.cfg.Repo, prNumber, "REQUEST_CHANGES", res.Findings); err != nil {
			return res, fmt.Errorf("review: request changes: %w", err)
		}
		return res, nil
	}

	res.Outcome = ReviewApproved
	if err := p.deps.GH.ReviewPR(ctx, p.cfg.Repo, prNumber, "APPROVE", res.Findings); err != nil {
		return res, fmt.Errorf("review: approve: %w", err)
	}

	// Merge only on approve AND green verification.
	if !verificationGreen {
		return res, nil
	}

	// Rebase the issue branch onto the integration branch before merging (spec:
	// Workspace manager & branch model — "Issue branches rebase onto the
	// integration branch before merging"). A conflict aborts cleanly and blocks
	// the merge, surfacing as an error rather than a dirty branch. The workspace
	// manager is optional in isolated tests; when present, rebase first.
	if p.deps.WS != nil {
		wt := p.deps.WS.WorktreePath(p.cfg.RepoDir, issue.Number, slugify(issue.Title))
		if err := p.deps.WS.RebaseOntoEpic(ctx, wt, epicNum); err != nil {
			return res, fmt.Errorf("review: rebase #%d onto epic before merge: %w", issue.Number, err)
		}
	}

	sha, err := p.deps.GH.MergePR(ctx, p.cfg.Repo, prNumber, "squash", "")
	if err != nil {
		return res, fmt.Errorf("review: merge PR #%d: %w", prNumber, err)
	}
	res.Merged = true
	res.MergeSHA = sha
	return res, nil
}

// topOptions filters opts to those whose model id is in topTier, preserving the
// topTier order (deterministic reviewer selection). Only healthy/available
// options are passed in by the caller, so a top model absent from opts is one
// that is currently unavailable and is correctly skipped.
func topOptions(opts []registry.RunOption, topTier []string) []registry.RunOption {
	byID := make(map[string]registry.RunOption, len(opts))
	for _, o := range opts {
		byID[o.Model.ID] = o
	}
	var out []registry.RunOption
	for _, id := range topTier {
		if o, ok := byID[id]; ok {
			out = append(out, o)
		}
	}
	return out
}

// reviewApproves reports whether the reviewer output signals approval. The
// contract: an approving review contains "REVIEW: APPROVE" (case-insensitive);
// anything else (including an explicit "REQUEST_CHANGES") is treated as changes
// requested, biasing toward human-visible findings over silent approval.
func reviewApproves(text string) bool {
	up := strings.ToUpper(text)
	if strings.Contains(up, "REQUEST_CHANGES") || strings.Contains(up, "REQUEST CHANGES") {
		return false
	}
	return strings.Contains(up, "REVIEW: APPROVE") || strings.Contains(up, "REVIEW:APPROVE")
}

// reviewCommentBody frames the reviewer's findings with a one-line header noting
// the reviewer model and selection kind.
func reviewCommentBody(choice ReviewerChoice, findings string) string {
	header := fmt.Sprintf("clex review by %s (%s)", choice.Model.ID, choice.Kind)
	return header + "\n\n" + strings.TrimSpace(findings)
}
