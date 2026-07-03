package main

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/reissui/clex/internal/core"
	"github.com/reissui/clex/internal/gh"
	"github.com/reissui/clex/internal/ipc"
)

// exit codes are stable and scripted against (spec: CLI — "Exit codes: 0 ok,
// 1 error, 2 doctor-found-problems").
const (
	exitOK      = 0 // success
	exitError   = 1 // a command failed
	exitProblem = 2 // doctor found problems, or a usage error
)

// env is the injectable surface every command runs against. Tests construct one
// with fakes (scripted stdin, a fake gh factory, mocked service loader, a stub
// IPC socket) so no command ever needs a live service. Production wires the real
// implementations in newEnv.
//
// Keeping all outside-world coupling behind this struct is what makes the whole
// CLI table-testable: a command reads its inputs from env, never from package
// globals or os.* directly.
type env struct {
	stdin  io.Reader
	stdout io.Writer
	stderr io.Writer

	// home is the clex home directory (normally ~/.clex). The IPC socket, global
	// config, and worktree root all live under it.
	home string
	// now returns the current time; fakeable so uptime/age rendering is stable.
	now func() time.Time

	// dialTimeout bounds IPC connect attempts (short in tests).
	dialTimeout time.Duration

	// probe detects a dependency binary and its auth state. Injectable so tests
	// drive ✓/✗ without the real claude/codex/ollama/gh installed.
	probe depProbe
	// newGH builds a GitHub client from a token. Injectable so init/idea/doctor
	// run against a fake instead of api.github.com.
	newGH ghFactory
	// telegram verifies a bot token + performs the tap-to-bind handshake.
	// Injectable so the wizard never hits Telegram.
	telegram telegramVerifier
	// service installs/loads the launchd or systemd unit. Loading is mocked in
	// tests; rendering is exercised directly.
	service serviceManager
	// goos overrides runtime.GOOS for service-unit selection in tests.
	goos string

	// ghToken resolves the ambient GitHub token (real: `gh auth token`).
	// Injectable so idea/plan/build/init tests supply a fixed token without gh.
	ghToken tokenResolver
	// originRemote returns the current repo's git "origin" remote URL (real:
	// `git remote get-url origin`). Injectable so tests set a repo without a
	// checkout. An error means "no origin", which callers treat as "no repo".
	originRemote remoteResolver
}

// tokenResolver returns the GitHub token to authenticate CLI gh operations. The
// production resolver shells to `gh auth token`; tests inject a constant.
type tokenResolver func(ctx context.Context) (string, error)

// remoteResolver returns the git origin remote URL of the working directory.
type remoteResolver func() (string, error)

// ghFactory builds a GitHub client for a token. It returns the concrete
// *gh.Client in production; tests substitute a fake satisfying the small
// interfaces the commands actually use (ghLabeler, ghIssuer, ghInspector).
type ghFactory func(token string) (ghClient, error)

// ghClient is the subset of *gh.Client the CLI depends on, widened with the two
// inspection calls doctor needs (token scopes, branch protection) that the base
// client does not yet expose. A thin adapter (realGH) implements the extras via
// the raw go-github client.
type ghClient interface {
	EnsureLabels(ctx context.Context, repo gh.Repo, agents []string) error
	CreateIssue(ctx context.Context, repo gh.Repo, title, body string, labels []string) (*gh.Issue, error)
	SetState(ctx context.Context, repo gh.Repo, number int, to core.State) error
	// TokenScopes returns the OAuth scopes GitHub reports for the token (the
	// X-OAuth-Scopes header). A fine-grained PAT reports none; a classic token
	// with full-repo access reports "repo". Empty slice is fine.
	TokenScopes(ctx context.Context) ([]string, error)
	// BranchProtected reports whether the given branch has protection enabled.
	BranchProtected(ctx context.Context, repo gh.Repo, branch string) (bool, error)
}

// newEnv builds the production env: real ~/.clex, real dependency probes, real
// GitHub + Telegram + service manager. Commands still receive it as a value so
// nothing reaches around it.
func newEnv(stdin io.Reader, stdout, stderr io.Writer) *env {
	return &env{
		stdin:        stdin,
		stdout:       stdout,
		stderr:       stderr,
		home:         defaultHome(),
		now:          time.Now,
		dialTimeout:  ipc.DefaultDialTimeout,
		probe:        execProbe{},
		newGH:        realGHFactory,
		telegram:     realTelegram{},
		service:      realService{},
		goos:         runtimeGOOS(),
		ghToken:      gh.TokenFromGH,
		originRemote: gitOriginRemote,
	}
}

// configuredRepo infers the managed repository ("owner/name") from the current
// working directory's git "origin" remote. clex commands are run from inside the
// target repo (spec: "clex init — in a repo …"), so the remote is the natural,
// config-free source of the repo identity. ok is false when the remote is
// missing or not a recognizable GitHub URL. Injectable via env.originRemote so
// tests need no real git checkout.
func (e *env) configuredRepo() (string, bool) {
	raw, err := e.originRemote()
	if err != nil {
		return "", false
	}
	if r, ok := repoFromRemote(raw); ok {
		return r, true
	}
	return "", false
}

// socketPath is where the daemon listens; commands that need the daemon name it
// in their "is it running?" error so the user knows exactly what to start.
func (e *env) socketPath() string { return ipc.SocketPath(e.home) }

// ipcClient dials the daemon control socket with the env's timeout.
func (e *env) ipcClient() *ipc.Client {
	return ipc.NewClient(e.socketPath()).WithDialTimeout(e.dialTimeout)
}

// globalConfigPath is ~/.clex/config.toml.
func (e *env) globalConfigPath() string { return filepath.Join(e.home, "config.toml") }

// defaultHome resolves ~/.clex, falling back to ./.clex if the home dir is
// somehow unavailable (never fatal at construction time).
func defaultHome() string {
	if h, err := os.UserHomeDir(); err == nil {
		return filepath.Join(h, ".clex")
	}
	return ".clex"
}
