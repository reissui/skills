package main

import (
	"context"
	"net/http"
	"os/exec"
	"strings"

	"github.com/reissui/clex/internal/core"
	"github.com/reissui/clex/internal/gh"
)

// depResult is the outcome of probing one external dependency binary.
type depResult struct {
	// Name is the dependency's display/identifier name ("claude", "gh", …).
	Name string
	// Found reports whether the binary is on PATH.
	Found bool
	// Authed reports whether the tool is authenticated/usable (best-effort; for
	// tools without an auth concept, e.g. ollama, this mirrors Found).
	Authed bool
	// Version is the reported version string, empty if unknown or absent.
	Version string
	// Detail is a short human-readable note (e.g. "not logged in").
	Detail string
}

// depProbe detects a dependency's presence, version, and auth state. The real
// implementation shells out to the tool; tests inject a scripted fake so the
// wizard and doctor exercise every ✓/✗ branch without the tools installed.
type depProbe interface {
	Probe(ctx context.Context, name string) depResult
}

// execProbe is the production depProbe. It runs the tool's version/auth
// subcommand with a bounded context. It never returns an error: a missing or
// broken tool is a depResult with Found/Authed false, which is exactly what the
// wizard and doctor render.
type execProbe struct{}

func (execProbe) Probe(ctx context.Context, name string) depResult {
	r := depResult{Name: name}
	path, err := exec.LookPath(name)
	if err != nil {
		return r // Found=false
	}
	r.Found = true
	switch name {
	case "gh":
		// `gh auth status` exits non-zero when not logged in.
		r.Authed = runOK(ctx, path, "auth", "status")
		if v, ok := runOut(ctx, path, "--version"); ok {
			r.Version = firstLine(v)
		}
		if !r.Authed {
			r.Detail = "not logged in"
		}
	case "claude", "codex":
		if v, ok := runOut(ctx, path, "--version"); ok {
			r.Version = firstLine(v)
		}
		// The provider CLIs authenticate out-of-band (subscription login); we treat
		// presence as usable and let a real run surface an auth error. Probing auth
		// without spending a token is unreliable, so presence is the signal.
		r.Authed = true
	case "ollama":
		if v, ok := runOut(ctx, path, "--version"); ok {
			r.Version = firstLine(v)
		}
		r.Authed = true // local; no auth
	default:
		r.Authed = true
	}
	return r
}

func runOK(ctx context.Context, name string, args ...string) bool {
	return exec.CommandContext(ctx, name, args...).Run() == nil
}

func runOut(ctx context.Context, name string, args ...string) (string, bool) {
	out, err := exec.CommandContext(ctx, name, args...).Output()
	if err != nil {
		return "", false
	}
	return string(out), true
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return strings.TrimSpace(s[:i])
	}
	return s
}

// telegramResult carries the outcome of the Telegram token check and, when the
// tap-to-bind handshake completes, the chat id the user bound.
type telegramResult struct {
	// Valid reports whether the bot token authenticated (getMe succeeded).
	Valid bool
	// BotUsername is the bot's @username, echoed back in the wizard summary.
	BotUsername string
	// ChatID is the owner chat id captured when the user tapped the test message.
	// Zero until Bind completes.
	ChatID int64
	// Detail is a short human-readable note on failure.
	Detail string
}

// telegramVerifier validates a bot token and runs the tap-to-bind flow (send a
// test message, wait for the tap, return the chat id). The real implementation
// talks to Telegram; the wizard's tests inject a fake that returns a scripted
// result so no message is ever sent.
type telegramVerifier interface {
	// Verify checks that the token authenticates and returns the bot username.
	Verify(ctx context.Context, token string) telegramResult
	// Bind sends a test message and blocks until the owner taps it, returning the
	// captured chat id. Only called after Verify succeeds.
	Bind(ctx context.Context, token string) telegramResult
}

// realTelegram is a thin adapter around the Telegram bot API. It is deliberately
// minimal here — the daemon (#16) owns the long-running bot; the CLI only needs
// a one-shot token check and bind during init. Implemented in telegram_real.go
// so tests never link live behavior into unit runs.
type realTelegram struct{}

// realGHFactory builds the production GitHub client adapter.
func realGHFactory(token string) (ghClient, error) {
	c, err := gh.New(token)
	if err != nil {
		return nil, err
	}
	return &realGH{c: c, token: token}, nil
}

// realGH adapts *gh.Client to ghClient, adding the two inspection calls doctor
// needs. TokenScopes and BranchProtected go through the raw go-github client
// (c.GitHub()) since the base clex client does not expose them.
type realGH struct {
	c     *gh.Client
	token string
}

func (r *realGH) EnsureLabels(ctx context.Context, repo gh.Repo, agents []string) error {
	return r.c.EnsureLabels(ctx, repo, agents)
}

func (r *realGH) CreateIssue(ctx context.Context, repo gh.Repo, title, body string, labels []string) (*gh.Issue, error) {
	return r.c.CreateIssue(ctx, repo, title, body, labels)
}

func (r *realGH) SetState(ctx context.Context, repo gh.Repo, number int, to core.State) error {
	return r.c.SetState(ctx, repo, number, to)
}

// TokenScopes issues a bare request and reads the X-OAuth-Scopes response header.
// A fine-grained PAT returns an empty scope list; a classic PAT lists its scopes
// (doctor warns when "repo" — full control — is present).
func (r *realGH) TokenScopes(ctx context.Context) ([]string, error) {
	req, err := r.c.GitHub().NewRequest(http.MethodGet, "user", nil)
	if err != nil {
		return nil, err
	}
	resp, err := r.c.GitHub().Do(ctx, req, nil)
	if err != nil {
		return nil, err
	}
	raw := resp.Header.Get("X-OAuth-Scopes")
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	parts := strings.Split(raw, ",")
	scopes := make([]string, 0, len(parts))
	for _, p := range parts {
		if s := strings.TrimSpace(p); s != "" {
			scopes = append(scopes, s)
		}
	}
	return scopes, nil
}

// BranchProtected reports GitHub's branch-protection state for branch. A 404
// (protection not configured) is reported as false, not an error.
func (r *realGH) BranchProtected(ctx context.Context, repo gh.Repo, branch string) (bool, error) {
	_, resp, err := r.c.GitHub().Repositories.GetBranchProtection(ctx, repo.Owner, repo.Name, branch)
	if err != nil {
		if resp != nil && resp.StatusCode == http.StatusNotFound {
			return false, nil
		}
		return false, err
	}
	return true, nil
}
