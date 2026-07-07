// Package gh is clex's GitHub integration: everything the daemon says to and
// reads from GitHub. GitHub is the source of truth for pipeline state (issue
// labels), so this package owns the label set, the label-driven state machine
// (enforcing core.CanTransition), the issue-metadata parser, PR operations, and
// a cheap ETag-based poller that emits typed change events.
//
// All HTTP goes through a *github.Client whose base URL is injectable, so tests
// run entirely against httptest fixtures — there are no live GitHub calls in the
// test suite (spec: Testing strategy; issue #6 acceptance criteria).
//
// Security: only owner-/clex-authored changes may drive pipeline actions. The
// poller tags every change event with the acting GitHub login and a TrustedActors
// filter drops (and counts) everything else (spec: Security model).
package gh

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os/exec"
	"strings"

	"github.com/google/go-github/v66/github"
	"golang.org/x/oauth2"
)

// Client wraps a go-github client with clex-specific operations. Construct it
// with New. It is safe for concurrent use by multiple goroutines (the embedded
// http.Client is).
type Client struct {
	gh    *github.Client
	http  *http.Client
	token string
	// baseURL is the API root (with trailing slash), used by the poller's raw
	// conditional requests. Mirrors gh.BaseURL.
	baseURL *url.URL
	// self is clex's own GitHub login, treated as a trusted actor alongside the
	// owner. May be empty if not configured.
	self string
}

// Option configures a Client.
type Option func(*Client)

// WithBaseURL points the client at an alternate API root (e.g. an httptest
// server in tests, or a GitHub Enterprise instance). The URL is normalized to
// carry a trailing slash, matching go-github's requirement.
func WithBaseURL(raw string) Option {
	return func(c *Client) {
		u, err := url.Parse(raw)
		if err != nil {
			return
		}
		if !strings.HasSuffix(u.Path, "/") {
			u.Path += "/"
		}
		c.baseURL = u
	}
}

// WithHTTPClient supplies a custom *http.Client (e.g. one wired to a test
// server's transport). The OAuth token, if any, is layered on top.
func WithHTTPClient(h *http.Client) Option {
	return func(c *Client) { c.http = h }
}

// WithSelfLogin records clex's own GitHub login so the poller's trusted-actor
// filter accepts clex-authored changes in addition to the owner's.
func WithSelfLogin(login string) Option {
	return func(c *Client) { c.self = login }
}

// New constructs a Client authenticated with the given token. Pass options to
// override the base URL (for tests) or HTTP client. An empty token produces an
// unauthenticated client — useful only against fixtures.
func New(token string, opts ...Option) (*Client, error) {
	c := &Client{token: token}
	for _, opt := range opts {
		opt(c)
	}

	// Build the transport: base HTTP client (possibly a test one) with an OAuth
	// token source layered on when a token is present.
	base := c.http
	if base == nil {
		base = &http.Client{}
	}
	httpClient := base
	if token != "" {
		ts := oauth2.StaticTokenSource(&oauth2.Token{AccessToken: token})
		httpClient = &http.Client{
			Transport: &oauth2.Transport{
				Source: ts,
				Base:   base.Transport,
			},
		}
	}
	c.http = httpClient

	gc := github.NewClient(httpClient)
	if c.baseURL != nil {
		gc.BaseURL = c.baseURL
	} else {
		c.baseURL = gc.BaseURL
	}
	c.gh = gc
	return c, nil
}

// TokenFromGH returns a GitHub token by shelling out to `gh auth token`. It is
// the default credential source when no token is supplied via config or env
// (spec: "a token from `gh auth token` or env"). The command is looked up on
// PATH; callers should fall back to an env var if this returns an error.
func TokenFromGH(ctx context.Context) (string, error) {
	path, err := exec.LookPath("gh")
	if err != nil {
		return "", fmt.Errorf("gh CLI not found on PATH: %w", err)
	}
	out, err := exec.CommandContext(ctx, path, "auth", "token").Output()
	if err != nil {
		return "", fmt.Errorf("gh auth token: %w", err)
	}
	tok := strings.TrimSpace(string(out))
	if tok == "" {
		return "", fmt.Errorf("gh auth token returned empty output")
	}
	return tok, nil
}

// GitHub exposes the underlying go-github client for operations not wrapped by
// this package. Prefer the typed methods here where they exist.
func (c *Client) GitHub() *github.Client { return c.gh }

// Repo identifies a GitHub repository as owner/name.
type Repo struct {
	Owner string
	Name  string
}

// String renders the repo as "owner/name".
func (r Repo) String() string { return r.Owner + "/" + r.Name }

// ParseRepo parses an "owner/name" string into a Repo.
func ParseRepo(s string) (Repo, error) {
	parts := strings.SplitN(strings.TrimSpace(s), "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return Repo{}, fmt.Errorf("invalid repo %q: want owner/name", s)
	}
	return Repo{Owner: parts[0], Name: parts[1]}, nil
}
