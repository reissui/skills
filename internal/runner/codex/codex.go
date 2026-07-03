// Package codex adapts the official OpenAI Codex CLI (`codex`) to the
// core.Runner interface. It shells out to the codex binary — never the OpenAI
// API directly — and normalizes the `codex exec --json` JSONL event stream into
// core.Events (spec: Runner adapters, Compliance note).
//
// One Adapter value serves a single codex model (selected at construction, e.g.
// gpt-5-5, codex-mini); registering several models means constructing several
// Adapters that share the same binary. The binary path is injectable so tests
// drive a fake script instead of the real CLI.
package codex

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/reissui/clex/internal/core"
)

// defaultBinary is the codex executable name resolved from PATH when no explicit
// path is configured.
const defaultBinary = "codex"

// Adapter satisfies the core.Runner contract at compile time.
var _ core.Runner = (*Adapter)(nil)

// Adapter implements core.Runner by driving the codex CLI. The zero value is not
// usable; construct one with New.
type Adapter struct {
	// binary is the codex executable (path or bare name resolved via PATH).
	binary string
	// model is the codex model this adapter runs (e.g. "gpt-5-5"). One adapter
	// serves one model so the registry can offer several codex models from a
	// single binary (spec: Runner adapters — providers are pluggable).
	model string
	// env is the fully-resolved, allowlisted child environment. When nil, it is
	// computed once, lazily, from the parent process via childEnv.
	env []string
}

// Option configures an Adapter at construction.
type Option func(*Adapter)

// WithBinary overrides the codex executable path (default: "codex" on PATH).
// Tests use this to point at a fake script.
func WithBinary(path string) Option {
	return func(a *Adapter) { a.binary = path }
}

// WithEnv overrides the child environment passed to codex. When unset, the
// adapter derives a minimal allowlisted env from the parent (see childEnv).
// Tests use this for deterministic, hermetic child environments.
func WithEnv(env []string) Option {
	return func(a *Adapter) { a.env = append([]string(nil), env...) }
}

// New constructs a codex Adapter for the given model id. Pass WithBinary in
// tests to inject a fake CLI; in production the binary resolves from PATH.
func New(model string, opts ...Option) *Adapter {
	a := &Adapter{binary: defaultBinary, model: model}
	for _, opt := range opts {
		opt(a)
	}
	if a.binary == "" {
		a.binary = defaultBinary
	}
	return a
}

// Model reports the codex model id this adapter runs.
func (a *Adapter) Model() string { return a.model }

// Run executes task in dir and streams normalized events until completion. It
// spawns `codex exec --json <prompt>` (or `codex exec resume <id> <prompt>` when
// task.ResumeID is set), injects any named skills into the worktree's AGENTS.md,
// and parses the JSONL event stream. The returned channel is closed when the
// run finishes; the terminal event is an EventResult carrying the session id, or
// an EventError. Cancelling ctx kills the child process group.
func (a *Adapter) Run(ctx context.Context, task core.Task, dir string) (<-chan core.Event, error) {
	if a.binary == "" {
		return nil, errors.New("codex: no binary configured")
	}
	// Skills injection is idempotent and owned by the adapter; codex has no
	// skills dir, so named skills are rendered into AGENTS.md (spec: Skills
	// layer — Codex rendered into AGENTS.md).
	if len(task.Skills) > 0 {
		if err := InjectSkills(dir, task.Skills); err != nil {
			return nil, fmt.Errorf("codex: inject skills: %w", err)
		}
	}

	args := a.buildArgs(task, dir)

	cmd := exec.CommandContext(ctx, a.binary, args...)
	cmd.Dir = dir
	cmd.Env = a.childEnv()
	// Run the child in its own process group so cancellation kills the whole
	// tree (the CLI may spawn tool subprocesses), not just the codex parent.
	setProcessGroup(cmd)
	// Kill the entire group, not just the leader, when ctx is cancelled.
	cmd.Cancel = func() error { return killGroup(cmd) }

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("codex: stdout pipe: %w", err)
	}
	// Codex writes non-JSON diagnostics to stderr; drain it so the child never
	// blocks on a full pipe, but keep it out of the event stream.
	cmd.Stderr = io.Discard

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("codex: start %s: %w", a.binary, err)
	}

	out := make(chan core.Event)
	go func() {
		defer close(out)
		streamEvents(bufio.NewReader(stdout), out)
		// Reap the child and surface a non-zero exit that produced no result.
		waitErr := cmd.Wait()
		if waitErr != nil && ctx.Err() == nil {
			emit(out, core.Event{
				Type: core.EventError,
				Err:  fmt.Sprintf("codex exited: %v", waitErr),
			})
		}
	}()
	return out, nil
}

// buildArgs assembles the codex exec argv for task in dir. The prompt is always
// passed as a positional argument (never stdin) so cancellation and argv
// assertions are deterministic.
func (a *Adapter) buildArgs(task core.Task, dir string) []string {
	// Leading global/config args apply to both fresh and resumed runs.
	args := []string{"exec", "--json"}
	if a.model != "" {
		args = append(args, "--model", a.model)
	}
	// Confine writes to the worktree and don't require a git repo check; the
	// workspace manager already prepared dir (spec: runners confined to
	// worktrees).
	args = append(args, "--cd", dir, "--skip-git-repo-check")
	if effort := reasoningEffort(task.Effort); effort != "" {
		// Codex has no dedicated effort flag; it is a config override.
		args = append(args, "-c", "model_reasoning_effort="+effort)
	}

	if task.ResumeID != "" {
		// `codex exec resume <SESSION_ID> <PROMPT>`: the resume subcommand and
		// its session id come before the prompt (spec: Resume, don't restart).
		args = append(args, "resume", task.ResumeID, task.Prompt)
		return args
	}
	args = append(args, task.Prompt)
	return args
}

// reasoningEffort maps a core effort level to codex's model_reasoning_effort
// value. Codex accepts minimal|low|medium|high; unknown values are dropped so a
// bad config never breaks the invocation (the CLI keeps its default).
func reasoningEffort(effort string) string {
	switch strings.ToLower(strings.TrimSpace(effort)) {
	case "minimal":
		return "minimal"
	case "low":
		return "low"
	case "medium":
		return "medium"
	case "high":
		return "high"
	case "max":
		// clex's "max" thinking maps to codex's strongest reasoning level.
		return "high"
	default:
		return ""
	}
}

// Probe reports whether codex is installed and authenticated. It runs
// `codex --version` (binary presence) and an auth check; either failing yields
// Availability{Healthy: false} with a human-readable detail (spec: Probe —
// rate-limit/auth failures → unhealthy).
func (a *Adapter) Probe(ctx context.Context) (core.Availability, error) {
	if a.binary == "" {
		return core.Availability{Healthy: false, Detail: "no codex binary configured"}, nil
	}

	verCmd := exec.CommandContext(ctx, a.binary, "--version")
	verCmd.Env = a.childEnv()
	verOut, err := verCmd.Output()
	if err != nil {
		return core.Availability{
			Healthy: false,
			Detail:  fmt.Sprintf("codex --version failed: %v", err),
		}, nil
	}
	version := strings.TrimSpace(string(verOut))

	// Auth check: `codex login status` exits non-zero when logged out or the
	// token is invalid. A non-zero exit here is an expected unhealthy state, not
	// a Probe error.
	authCmd := exec.CommandContext(ctx, a.binary, "login", "status")
	authCmd.Env = a.childEnv()
	if authOut, authErr := authCmd.CombinedOutput(); authErr != nil {
		detail := strings.TrimSpace(string(authOut))
		if detail == "" {
			detail = authErr.Error()
		}
		return core.Availability{
			Healthy: false,
			Detail:  fmt.Sprintf("codex not authenticated (%s): %s", version, detail),
		}, nil
	}

	return core.Availability{Healthy: true, Detail: version}, nil
}

// childEnv returns the minimal, allowlisted environment for codex child
// processes. An explicit WithEnv override wins; otherwise it is derived once
// from the parent via filterEnv.
func (a *Adapter) childEnv() []string {
	if a.env != nil {
		return a.env
	}
	a.env = filterEnv(os.Environ())
	return a.env
}

// allowedEnvExact is the set of environment variable names passed through to
// codex verbatim. Everything else is dropped so a child never inherits the
// daemon's full environment (spec: least-privilege child processes).
var allowedEnvExact = map[string]bool{
	"PATH":    true,
	"HOME":    true,
	"USER":    true,
	"LOGNAME": true,
	"SHELL":   true,
	"LANG":    true,
	"LC_ALL":  true,
	"TERM":    true,
	"TMPDIR":  true,
	// codex/OpenAI auth + config discovery.
	"CODEX_HOME":     true,
	"OPENAI_API_KEY": true,
	// git/ssh essentials for tool subprocesses.
	"SSH_AUTH_SOCK":   true,
	"GIT_SSH":         true,
	"GIT_SSH_COMMAND": true,
	"XDG_CONFIG_HOME": true,
	"XDG_CACHE_HOME":  true,
	"XDG_DATA_HOME":   true,
}

// allowedEnvPrefix lists prefixes whose variables are passed through (locale and
// codex-namespaced settings).
var allowedEnvPrefix = []string{"LC_", "CODEX_", "OPENAI_"}

// strippedEnvExact names variables that must never reach a child even if they
// would otherwise match a prefix rule. Anthropic credentials are stripped for
// billing + compliance (harmless here but kept consistent with the claude
// adapter — spec: ANTHROPIC_API_KEY stays stripped).
var strippedEnvExact = map[string]bool{
	"ANTHROPIC_API_KEY":    true,
	"ANTHROPIC_AUTH_TOKEN": true,
}

// filterEnv returns only the allowlisted subset of env (a "KEY=value" slice),
// dropping everything not explicitly permitted and always removing stripped
// credentials.
func filterEnv(env []string) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		key, _, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		if strippedEnvExact[key] {
			continue
		}
		if allowedEnvExact[key] || hasAllowedPrefix(key) {
			out = append(out, kv)
		}
	}
	return out
}

// hasAllowedPrefix reports whether key starts with a permitted prefix.
func hasAllowedPrefix(key string) bool {
	for _, p := range allowedEnvPrefix {
		if strings.HasPrefix(key, p) {
			return true
		}
	}
	return false
}

// agentsFile is the path to a worktree's AGENTS.md.
func agentsFile(dir string) string { return filepath.Join(dir, "AGENTS.md") }
