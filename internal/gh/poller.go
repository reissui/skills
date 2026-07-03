package gh

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"
)

// ChangeKind classifies a poller change event.
type ChangeKind string

const (
	// ChangeLabeled is emitted when a label is added to an issue/PR.
	ChangeLabeled ChangeKind = "labeled"
	// ChangeUnlabeled is emitted when a label is removed from an issue/PR.
	ChangeUnlabeled ChangeKind = "unlabeled"
	// ChangeIssueOpened is emitted when an issue is (re)opened.
	ChangeIssueOpened ChangeKind = "issue_opened"
	// ChangeIssueClosed is emitted when an issue is closed.
	ChangeIssueClosed ChangeKind = "issue_closed"
	// ChangePRMerged is emitted when a PR is merged (GitHub reports this as a
	// "merged" issue event, since PRs are issues).
	ChangePRMerged ChangeKind = "pr_merged"
)

// Change is a single observed change on a repo. It always carries the acting
// GitHub login (Actor) so the trusted-actor filter can decide whether it may
// drive pipeline actions (spec: Security model).
type Change struct {
	Repo Repo
	Kind ChangeKind
	// Issue is the issue or PR number the change applies to.
	Issue int
	// Actor is the GitHub login that caused the change. Empty only if GitHub
	// omitted it (treated as untrusted).
	Actor string
	// Label is the label name for ChangeLabeled/ChangeUnlabeled; empty
	// otherwise.
	Label string
	// EventID is GitHub's monotonic event id, used to de-duplicate across polls.
	EventID int64
	// At is when GitHub recorded the event.
	At time.Time
}

// TrustedActors decides which GitHub logins may drive pipeline actions. Only the
// configured Owner and clex's own identity (Self) are trusted; everything else
// is dropped and counted (spec: "changes by any other GitHub account are ignored
// and logged"). Comparison is case-insensitive, matching GitHub login semantics.
type TrustedActors struct {
	// Owner is the repository owner's login (the human operator).
	Owner string
	// Self is clex's own bot/user login, if it acts under a distinct account.
	// May be empty.
	Self string

	mu      sync.Mutex
	dropped int64
}

// Trusted reports whether login is an allowed actor. An empty login is never
// trusted.
func (t *TrustedActors) Trusted(login string) bool {
	if login == "" {
		return false
	}
	if equalFoldLogin(login, t.Owner) {
		return true
	}
	if t.Self != "" && equalFoldLogin(login, t.Self) {
		return true
	}
	return false
}

// filter records a drop for an untrusted actor and returns whether the change
// should be kept.
func (t *TrustedActors) filter(login string) bool {
	if t.Trusted(login) {
		return true
	}
	t.mu.Lock()
	t.dropped++
	t.mu.Unlock()
	return false
}

// Dropped returns how many changes have been dropped for untrusted actors so
// far. Safe for concurrent use.
func (t *TrustedActors) Dropped() int64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.dropped
}

// equalFoldLogin compares two GitHub logins case-insensitively.
func equalFoldLogin(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := 0; i < len(a); i++ {
		ca, cb := a[i], b[i]
		if 'A' <= ca && ca <= 'Z' {
			ca += 'a' - 'A'
		}
		if 'A' <= cb && cb <= 'Z' {
			cb += 'a' - 'A'
		}
		if ca != cb {
			return false
		}
	}
	return true
}

// rawIssueEvent is the subset of GitHub's repository issue-events payload clex
// consumes. Endpoint: GET /repos/{owner}/{repo}/issues/events.
type rawIssueEvent struct {
	ID    int64  `json:"id"`
	Event string `json:"event"` // labeled, unlabeled, closed, reopened, merged
	Actor *struct {
		Login string `json:"login"`
	} `json:"actor"`
	Label *struct {
		Name string `json:"name"`
	} `json:"label"`
	Issue *struct {
		Number int `json:"number"`
	} `json:"issue"`
	CreatedAt time.Time `json:"created_at"`
}

// PollOptions configures Poll.
type PollOptions struct {
	// Trusted, if non-nil, filters change events: untrusted actors are dropped
	// and counted. If nil, all events pass through (not recommended in
	// production).
	Trusted *TrustedActors
	// Interval overrides the poll interval. If zero, the every argument to Poll
	// is used.
	Interval time.Duration
}

// Poll starts polling the given repos for changes and returns a channel of
// typed Change events. It uses conditional requests (If-None-Match with the
// per-repo ETag) so unchanged polls cost no rate-limit quota and return quickly
// (spec: "conditional requests (ETags) to stay cheap").
//
// The returned channel is closed when ctx is cancelled. Each repo is polled on
// its own ticker; a 304 Not Modified yields no events. Events are emitted in
// GitHub order and de-duplicated by event id across polls. When opts.Trusted is
// set, changes from untrusted actors are dropped and counted, never emitted.
func (c *Client) Poll(ctx context.Context, repos []Repo, every time.Duration, opts PollOptions) <-chan Change {
	interval := every
	if opts.Interval > 0 {
		interval = opts.Interval
	}
	if interval <= 0 {
		interval = 30 * time.Second
	}

	out := make(chan Change)
	var wg sync.WaitGroup
	for _, repo := range repos {
		wg.Add(1)
		go func(repo Repo) {
			defer wg.Done()
			c.pollRepo(ctx, repo, interval, opts, out)
		}(repo)
	}
	go func() {
		wg.Wait()
		close(out)
	}()
	return out
}

// pollRepo polls a single repo until ctx is done, tracking the ETag and the
// highest seen event id.
func (c *Client) pollRepo(ctx context.Context, repo Repo, interval time.Duration, opts PollOptions, out chan<- Change) {
	var etag string
	var lastID int64
	first := true

	tick := time.NewTicker(interval)
	defer tick.Stop()

	for {
		if !first {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
			}
		}
		first = false

		events, newETag, err := c.fetchIssueEvents(ctx, repo, etag)
		if err != nil {
			// Transient errors (including ctx cancellation) are non-fatal to the
			// loop; ctx.Done in the next select stops us cleanly.
			if ctx.Err() != nil {
				return
			}
			continue
		}
		if newETag != "" {
			etag = newETag
		}
		// GitHub returns most-recent-first; process oldest-first so lastID moves
		// forward monotonically and consumers see chronological order.
		for i := len(events) - 1; i >= 0; i-- {
			ev := events[i]
			if ev.ID <= lastID {
				continue // already emitted in a prior poll
			}
			change, ok := toChange(repo, ev)
			if !ok {
				lastID = ev.ID
				continue
			}
			if opts.Trusted != nil && !opts.Trusted.filter(change.Actor) {
				lastID = ev.ID
				continue // untrusted: dropped and counted
			}
			select {
			case <-ctx.Done():
				return
			case out <- change:
			}
			lastID = ev.ID
		}
	}
}

// fetchIssueEvents issues a conditional GET for the repo's issue events. When
// etag is non-empty it is sent as If-None-Match; a 304 response returns no
// events and the same etag. The returned etag is the response's ETag header.
func (c *Client) fetchIssueEvents(ctx context.Context, repo Repo, etag string) ([]rawIssueEvent, string, error) {
	ref, err := c.baseURL.Parse(fmt.Sprintf("repos/%s/%s/issues/events?per_page=100", repo.Owner, repo.Name))
	if err != nil {
		return nil, "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ref.String(), nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()

	newETag := resp.Header.Get("ETag")
	if resp.StatusCode == http.StatusNotModified {
		// Nothing changed since the last poll: cheap path, no body.
		io.Copy(io.Discard, resp.Body)
		return nil, newETag, nil
	}
	if resp.StatusCode != http.StatusOK {
		io.Copy(io.Discard, resp.Body)
		return nil, newETag, fmt.Errorf("poll %s: unexpected status %d", repo, resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, newETag, err
	}
	var events []rawIssueEvent
	if err := json.Unmarshal(body, &events); err != nil {
		return nil, newETag, fmt.Errorf("poll %s: decode events: %w", repo, err)
	}
	return events, newETag, nil
}

// toChange maps a raw GitHub issue event to a clex Change. It returns ok=false
// for event types clex does not track.
func toChange(repo Repo, ev rawIssueEvent) (Change, bool) {
	ch := Change{
		Repo:    repo,
		Issue:   0,
		EventID: ev.ID,
		At:      ev.CreatedAt,
	}
	if ev.Actor != nil {
		ch.Actor = ev.Actor.Login
	}
	if ev.Issue != nil {
		ch.Issue = ev.Issue.Number
	}
	if ev.Label != nil {
		ch.Label = ev.Label.Name
	}
	switch ev.Event {
	case "labeled":
		ch.Kind = ChangeLabeled
	case "unlabeled":
		ch.Kind = ChangeUnlabeled
	case "closed":
		ch.Kind = ChangeIssueClosed
	case "reopened":
		ch.Kind = ChangeIssueOpened
	case "merged":
		ch.Kind = ChangePRMerged
	default:
		return Change{}, false
	}
	return ch, true
}
