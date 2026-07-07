package gh

import (
	"context"
	"fmt"

	"github.com/google/go-github/v66/github"
)

// PullRequest is a clex view of a GitHub pull request.
type PullRequest struct {
	Number int
	Title  string
	State  string // "open", "closed"
	Merged bool
	Head   string // head branch (ref)
	Base   string // base branch (ref)
	// Mergeable is GitHub's computed mergeability (may be nil while GitHub is
	// still computing it just after creation).
	Mergeable *bool
	// MergeableState is GitHub's coarse status: "clean", "dirty", "blocked",
	// "behind", "unstable", "unknown", etc.
	MergeableState string
}

// OpenPR opens a pull request from head into base. base is typically an epic
// integration branch (clex/epic-<n>), not main — integration branches are the
// common case (spec: Workspace manager & branch model). head may be
// "owner:branch" for cross-fork PRs or a plain branch for same-repo.
func (c *Client) OpenPR(ctx context.Context, repo Repo, title, head, base, body string) (*PullRequest, error) {
	pr, _, err := c.gh.PullRequests.Create(ctx, repo.Owner, repo.Name, &github.NewPullRequest{
		Title: github.String(title),
		Head:  github.String(head),
		Base:  github.String(base),
		Body:  github.String(body),
	})
	if err != nil {
		return nil, fmt.Errorf("open PR %s->%s in %s: %w", head, base, repo, err)
	}
	return prFromGitHub(pr), nil
}

// GetPR fetches a pull request and its mergeable/status state.
func (c *Client) GetPR(ctx context.Context, repo Repo, number int) (*PullRequest, error) {
	pr, _, err := c.gh.PullRequests.Get(ctx, repo.Owner, repo.Name, number)
	if err != nil {
		return nil, fmt.Errorf("get PR #%d in %s: %w", number, repo, err)
	}
	return prFromGitHub(pr), nil
}

// ReviewComment posts a single review comment on a specific line of a PR's diff.
// path is the file path; line is the position in the diff. For a general
// (non-line) review, use ReviewPR.
func (c *Client) ReviewComment(ctx context.Context, repo Repo, number int, commitID, path string, line int, body string) error {
	_, _, err := c.gh.PullRequests.CreateComment(ctx, repo.Owner, repo.Name, number, &github.PullRequestComment{
		Body:     github.String(body),
		CommitID: github.String(commitID),
		Path:     github.String(path),
		Line:     github.Int(line),
	})
	if err != nil {
		return fmt.Errorf("review comment on PR #%d in %s: %w", number, repo, err)
	}
	return nil
}

// ReviewPR submits a whole-PR review with a body and an event
// ("COMMENT", "APPROVE", or "REQUEST_CHANGES"). Model reviews use this to gate
// issue→integration merges (spec: Review policy).
func (c *Client) ReviewPR(ctx context.Context, repo Repo, number int, event, body string) error {
	_, _, err := c.gh.PullRequests.CreateReview(ctx, repo.Owner, repo.Name, number, &github.PullRequestReviewRequest{
		Body:  github.String(body),
		Event: github.String(event),
	})
	if err != nil {
		return fmt.Errorf("review PR #%d in %s: %w", number, repo, err)
	}
	return nil
}

// MergePR merges a pull request using the given method ("merge", "squash", or
// "rebase"). commitMessage may be empty to accept GitHub's default. It returns
// the merge commit SHA.
func (c *Client) MergePR(ctx context.Context, repo Repo, number int, method, commitMessage string) (string, error) {
	opts := &github.PullRequestOptions{}
	if method != "" {
		opts.MergeMethod = method
	}
	res, _, err := c.gh.PullRequests.Merge(ctx, repo.Owner, repo.Name, number, commitMessage, opts)
	if err != nil {
		return "", fmt.Errorf("merge PR #%d in %s: %w", number, repo, err)
	}
	if !res.GetMerged() {
		return "", fmt.Errorf("merge PR #%d in %s: not merged: %s", number, repo, res.GetMessage())
	}
	return res.GetSHA(), nil
}

// prFromGitHub builds the clex PullRequest view from a go-github pull request.
func prFromGitHub(pr *github.PullRequest) *PullRequest {
	out := &PullRequest{
		Number:         pr.GetNumber(),
		Title:          pr.GetTitle(),
		State:          pr.GetState(),
		Merged:         pr.GetMerged(),
		Head:           pr.GetHead().GetRef(),
		Base:           pr.GetBase().GetRef(),
		MergeableState: pr.GetMergeableState(),
	}
	if pr.Mergeable != nil {
		v := pr.GetMergeable()
		out.Mergeable = &v
	}
	return out
}
