package gh

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/go-github/v66/github"
	"github.com/reissui/clex/internal/core"
)

// Issue is a clex view of a GitHub issue: the raw fields clex cares about plus
// the current pipeline state (derived from labels) and the parsed metadata
// block. Read one with GetIssue.
type Issue struct {
	Number int
	Title  string
	Body   string
	// AuthorLogin is the GitHub login that opened the issue. Used by security
	// checks to decide whether the issue's verification command is trusted
	// (spec: Security model).
	AuthorLogin string
	// State is the current pipeline state parsed from the issue's clex:* labels,
	// or the empty State if none is present (hand-created / not yet in the
	// pipeline).
	State core.State
	// IsEpic reports whether the issue carries the clex:epic marker.
	IsEpic bool
	// Labels is every label name on the issue (including non-clex labels).
	Labels []string
	// Meta is the parsed clex metadata block from the body.
	Meta Metadata
}

// TransitionError is returned when a requested state change violates the
// pipeline state machine (core.CanTransition). It is a typed error so callers
// can distinguish an illegal transition from an API failure.
type TransitionError struct {
	Issue int
	From  core.State
	To    core.State
}

func (e *TransitionError) Error() string {
	return fmt.Sprintf("illegal state transition for issue #%d: %s -> %s",
		e.Issue, e.From, e.To)
}

// IsTransitionError reports whether err is (or wraps) a *TransitionError.
func IsTransitionError(err error) bool {
	var te *TransitionError
	return errors.As(err, &te)
}

// GetIssue fetches an issue and returns the clex view of it, parsing labels into
// a pipeline state and the body into metadata.
func (c *Client) GetIssue(ctx context.Context, repo Repo, number int) (*Issue, error) {
	iss, _, err := c.gh.Issues.Get(ctx, repo.Owner, repo.Name, number)
	if err != nil {
		return nil, fmt.Errorf("get issue #%d in %s: %w", number, repo, err)
	}
	return issueFromGitHub(iss), nil
}

// issueFromGitHub builds the clex Issue view from a go-github issue.
func issueFromGitHub(iss *github.Issue) *Issue {
	out := &Issue{
		Number:      iss.GetNumber(),
		Title:       iss.GetTitle(),
		Body:        iss.GetBody(),
		AuthorLogin: iss.GetUser().GetLogin(),
		Meta:        ParseMetadata(iss.GetBody()),
	}
	for _, l := range iss.Labels {
		name := l.GetName()
		out.Labels = append(out.Labels, name)
		s := core.State(name)
		if core.IsPipelineState(s) {
			out.State = s
		}
		if s == core.StateEpic {
			out.IsEpic = true
		}
	}
	return out
}

// ListOpenIssues returns every open clex-managed issue (one carrying a
// pipeline-state label or the epic marker), paginating fully. It is the same
// source-of-truth read the daemon uses to build scheduler state; the CLI uses
// it to enumerate an epic's children for `clex build <epic#>`.
func (c *Client) ListOpenIssues(ctx context.Context, repo Repo) ([]*Issue, error) {
	opts := &github.IssueListByRepoOptions{
		State:       "open",
		ListOptions: github.ListOptions{PerPage: 100},
	}
	var out []*Issue
	for {
		issues, resp, err := c.gh.Issues.ListByRepo(ctx, repo.Owner, repo.Name, opts)
		if err != nil {
			return nil, fmt.Errorf("list issues in %s: %w", repo, err)
		}
		for _, iss := range issues {
			// The issues endpoint returns pull requests too; skip them.
			if iss.PullRequestLinks != nil {
				continue
			}
			conv := issueFromGitHub(iss)
			if conv.State == "" && !conv.IsEpic {
				continue // not clex-managed
			}
			out = append(out, conv)
		}
		if resp == nil || resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
	}
	return out, nil
}

// CreateIssue opens a new issue with the given title, body, and labels.
func (c *Client) CreateIssue(ctx context.Context, repo Repo, title, body string, labels []string) (*Issue, error) {
	req := &github.IssueRequest{
		Title: github.String(title),
		Body:  github.String(body),
	}
	if labels != nil {
		req.Labels = &labels
	}
	iss, _, err := c.gh.Issues.Create(ctx, repo.Owner, repo.Name, req)
	if err != nil {
		return nil, fmt.Errorf("create issue in %s: %w", repo, err)
	}
	return issueFromGitHub(iss), nil
}

// UpdateIssue edits an issue's title and/or body. Pass nil for a field to leave
// it unchanged.
func (c *Client) UpdateIssue(ctx context.Context, repo Repo, number int, title, body *string) (*Issue, error) {
	req := &github.IssueRequest{}
	if title != nil {
		req.Title = title
	}
	if body != nil {
		req.Body = body
	}
	iss, _, err := c.gh.Issues.Edit(ctx, repo.Owner, repo.Name, number, req)
	if err != nil {
		return nil, fmt.Errorf("update issue #%d in %s: %w", number, repo, err)
	}
	return issueFromGitHub(iss), nil
}

// Comment posts a comment on an issue (or PR — GitHub treats PRs as issues for
// comments).
func (c *Client) Comment(ctx context.Context, repo Repo, number int, body string) error {
	_, _, err := c.gh.Issues.CreateComment(ctx, repo.Owner, repo.Name, number,
		&github.IssueComment{Body: github.String(body)})
	if err != nil {
		return fmt.Errorf("comment on #%d in %s: %w", number, repo, err)
	}
	return nil
}

// SetState transitions an issue to a new pipeline state by swapping its clex:*
// state label, enforcing core.CanTransition. The current state is read from the
// issue's labels first (GitHub is the source of truth), so a hand-edited label
// is respected rather than assumed. An illegal transition returns a
// *TransitionError and makes no writes.
//
// Only the single pipeline-state label is swapped; the epic marker and
// clex:agent/* tags are preserved. Non-clex labels are untouched.
func (c *Client) SetState(ctx context.Context, repo Repo, number int, to core.State) error {
	if !core.IsPipelineState(to) {
		return fmt.Errorf("set state for #%d: %q is not a pipeline state", number, to)
	}
	iss, _, err := c.gh.Issues.Get(ctx, repo.Owner, repo.Name, number)
	if err != nil {
		return fmt.Errorf("set state for #%d: read current: %w", number, err)
	}
	cur := issueFromGitHub(iss)
	from := cur.State
	if from == to {
		// Already there: idempotent no-op.
		return nil
	}
	if !core.CanTransition(from, to) {
		return &TransitionError{Issue: number, From: from, To: to}
	}

	// Build the new label set: keep everything except the old pipeline-state
	// label, then add the new one.
	next := make([]string, 0, len(cur.Labels)+1)
	for _, name := range cur.Labels {
		if core.IsPipelineState(core.State(name)) {
			continue // drop any/all existing pipeline-state labels
		}
		next = append(next, name)
	}
	next = append(next, string(to))

	if _, _, err := c.gh.Issues.ReplaceLabelsForIssue(ctx, repo.Owner, repo.Name, number, next); err != nil {
		return fmt.Errorf("set state for #%d: replace labels: %w", number, err)
	}
	return nil
}

// AddAgentLabel tags an issue with clex:agent/<name>, recording which runner
// owns it. Idempotent: adding an existing label is a no-op on GitHub's side.
func (c *Client) AddAgentLabel(ctx context.Context, repo Repo, number int, agent string) error {
	agent = strings.TrimSpace(agent)
	if agent == "" {
		return fmt.Errorf("add agent label to #%d: empty agent name", number)
	}
	_, _, err := c.gh.Issues.AddLabelsToIssue(ctx, repo.Owner, repo.Name, number,
		[]string{AgentLabel(agent)})
	if err != nil {
		return fmt.Errorf("add agent label to #%d: %w", number, err)
	}
	return nil
}
