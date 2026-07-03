package daemon

import (
	"context"
	"strings"
	"time"

	gogithub "github.com/google/go-github/v66/github"
	"github.com/reissui/clex/internal/core"
	"github.com/reissui/clex/internal/gh"
)

// ghAdapter adapts *gh.Client to GitHubPort. Most methods forward directly; only
// ListIssues adds behavior, using the raw go-github client to enumerate open
// issues (gh.Client has no list method, and internal/gh is outside this issue's
// scope to modify). The label→state derivation mirrors internal/gh's own so the
// resulting *gh.Issue values match what GetIssue would return.
type ghAdapter struct {
	c *gh.Client
}

// NewGitHubPort wraps a *gh.Client as a GitHubPort for the daemon.
func NewGitHubPort(c *gh.Client) GitHubPort { return &ghAdapter{c: c} }

func (a *ghAdapter) Poll(ctx context.Context, repos []gh.Repo, every time.Duration, opts gh.PollOptions) <-chan gh.Change {
	return a.c.Poll(ctx, repos, every, opts)
}

func (a *ghAdapter) GetIssue(ctx context.Context, repo gh.Repo, number int) (*gh.Issue, error) {
	return a.c.GetIssue(ctx, repo, number)
}

func (a *ghAdapter) SetState(ctx context.Context, repo gh.Repo, number int, to core.State) error {
	return a.c.SetState(ctx, repo, number, to)
}

func (a *ghAdapter) Comment(ctx context.Context, repo gh.Repo, number int, body string) error {
	return a.c.Comment(ctx, repo, number, body)
}

func (a *ghAdapter) UpdateIssue(ctx context.Context, repo gh.Repo, number int, title, body *string) (*gh.Issue, error) {
	return a.c.UpdateIssue(ctx, repo, number, title, body)
}

// ListIssues returns every open issue carrying a clex:* label, paginating fully.
func (a *ghAdapter) ListIssues(ctx context.Context, repo gh.Repo) ([]*gh.Issue, error) {
	raw := a.c.GitHub()
	opts := &gogithub.IssueListByRepoOptions{
		State:       "open",
		ListOptions: gogithub.ListOptions{PerPage: 100},
	}
	var out []*gh.Issue
	for {
		issues, resp, err := raw.Issues.ListByRepo(ctx, repo.Owner, repo.Name, opts)
		if err != nil {
			return nil, err
		}
		for _, iss := range issues {
			// Skip pull requests: the issues endpoint returns them too.
			if iss.PullRequestLinks != nil {
				continue
			}
			conv := convertIssue(iss)
			if conv.State == "" && !conv.IsEpic {
				continue // not a clex-managed issue
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

// convertIssue mirrors internal/gh's issueFromGitHub label/state derivation.
// It is duplicated (not imported) because that helper is unexported and
// internal/gh is out of scope to modify for this issue.
func convertIssue(iss *gogithub.Issue) *gh.Issue {
	out := &gh.Issue{
		Number:      iss.GetNumber(),
		Title:       iss.GetTitle(),
		Body:        iss.GetBody(),
		AuthorLogin: iss.GetUser().GetLogin(),
		Meta:        gh.ParseMetadata(iss.GetBody()),
	}
	for _, l := range iss.Labels {
		name := strings.TrimSpace(l.GetName())
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
