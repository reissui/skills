// Package local adapts local models — Ollama models driven through
// `codex exec --oss` — to the core.Runner interface. Execution reuses the codex
// adapter verbatim (it shells out to the codex binary and normalizes the JSONL
// stream); this package only adds the `--oss` flag and Ollama autodetection on
// top. It never duplicates the codex exec/parse machinery and never calls a
// provider API directly (spec: Runner adapters — "local: codex --oss against
// Ollama … same adapter shape"; "Ollama is auto-detected").
//
// One Adapter value serves a single local model (selected at construction, e.g.
// qwen3-coder); registering several local models means constructing several
// Adapters that share the same codex + ollama binaries. Both binaries and the
// command runner are injectable so tests drive fake scripts instead of a live
// stack.
package local

import (
	"context"
	"fmt"
	"os/exec"
	"strings"

	"github.com/reissui/clex/internal/core"
	"github.com/reissui/clex/internal/runner/codex"
)

// defaultOllama is the ollama executable name resolved from PATH when no
// explicit path is configured.
const defaultOllama = "ollama"

// ossFlag is the codex global flag that routes `codex exec` at a local Ollama
// model instead of the cloud provider.
const ossFlag = "--oss"

// Adapter satisfies the core.Runner contract at compile time.
var _ core.Runner = (*Adapter)(nil)

// commandRunner runs a command and returns its combined stdout, so tests can
// substitute a fake `ollama list` without spawning a process. The default
// implementation shells out via os/exec.
type commandRunner func(ctx context.Context, name string, args ...string) ([]byte, error)

// Adapter implements core.Runner for local models. Run delegates to a wrapped
// codex adapter configured with --oss; Detect and Probe query Ollama. The zero
// value is not usable; construct one with New.
type Adapter struct {
	// exec is the wrapped codex adapter that actually runs `codex exec --oss …`.
	// All event parsing lives in the codex package — never re-implemented here.
	exec *codex.Adapter
	// model is the local model id this adapter runs (e.g. "qwen3-coder"). It is
	// the id reported by `ollama list` and passed to codex as --model.
	model string
	// ollamaBin is the ollama executable (path or bare name resolved via PATH),
	// used only for autodetection and health probing.
	ollamaBin string
	// run executes external commands (ollama list). Injectable for hermetic
	// tests; defaults to execCommand.
	run commandRunner

	// codexBinary and env are set by options and consumed by New when building
	// the wrapped codex adapter; they are not read after construction.
	codexBinary string
	env         []string
}

// Option configures an Adapter at construction.
type Option func(*Adapter)

// WithOllamaBinary overrides the ollama executable path (default: "ollama" on
// PATH). Tests use this to point at a fake script.
func WithOllamaBinary(path string) Option {
	return func(a *Adapter) { a.ollamaBin = path }
}

// WithCodexBinary overrides the codex executable path used for `codex exec
// --oss` (default: "codex" on PATH). Tests use this to point at a fake script.
func WithCodexBinary(path string) Option {
	return func(a *Adapter) { a.codexBinary = path }
}

// WithEnv overrides the child environment passed to the codex child process.
// When unset, the codex adapter derives a minimal allowlisted env from the
// parent. Tests use this for deterministic, hermetic child environments.
func WithEnv(env []string) Option {
	return func(a *Adapter) { a.env = append([]string(nil), env...) }
}

// WithCommandRunner overrides how `ollama list` (and any ollama probe command)
// is executed. Tests inject a fake that returns canned output without spawning a
// process.
func WithCommandRunner(r commandRunner) Option {
	return func(a *Adapter) { a.run = r }
}

// New constructs a local Adapter for the given model id. The wrapped codex
// adapter is created with --oss (and the model as --model) so every Run targets
// the local stack. Pass WithOllamaBinary / WithCodexBinary / WithCommandRunner
// in tests to inject fakes; in production the binaries resolve from PATH.
func New(model string, opts ...Option) *Adapter {
	a := &Adapter{
		model:     model,
		ollamaBin: defaultOllama,
		run:       execCommand,
	}
	for _, opt := range opts {
		opt(a)
	}
	if a.ollamaBin == "" {
		a.ollamaBin = defaultOllama
	}
	if a.run == nil {
		a.run = execCommand
	}

	// Build the wrapped codex adapter with --oss (and any injected binary/env),
	// reusing its argv assembly, env allowlist, and JSONL parsing wholesale.
	codexOpts := []codex.Option{codex.WithExtraArgs(ossFlag)}
	if a.codexBinary != "" {
		codexOpts = append(codexOpts, codex.WithBinary(a.codexBinary))
	}
	if a.env != nil {
		codexOpts = append(codexOpts, codex.WithEnv(a.env))
	}
	a.exec = codex.New(model, codexOpts...)
	return a
}

// Model reports the local model id this adapter runs.
func (a *Adapter) Model() string { return a.model }

// Run executes task in dir via the wrapped codex adapter with --oss, streaming
// the same normalized events codex produces. Because the local stack has no
// distinct thinking/fast controls, task.Fast is a best-effort no-op here and
// task.Effort is passed through to codex only for models that honor it (spec:
// Fast/Effort are best-effort where the stack has no equivalent). All exec and
// parsing is codex's; this method adds nothing but the --oss routing baked into
// the wrapped adapter at construction.
func (a *Adapter) Run(ctx context.Context, task core.Task, dir string) (<-chan core.Event, error) {
	if a.exec == nil {
		return nil, fmt.Errorf("local: adapter not constructed with New")
	}
	// Fast has no local equivalent; make the no-op explicit so a caller-set flag
	// never leaks into codex argv as an unsupported option.
	task.Fast = false
	return a.exec.Run(ctx, task, dir)
}

// Detect discovers the local models Ollama currently has installed by running
// `ollama list` and parsing the model ids. The absence of Ollama (binary
// missing, daemon down, command error) is a clean "not available" —
// Availability{Healthy: false} with an empty model list and a nil error, never
// a failure return (spec: Ollama is auto-detected; absence is not an error).
//
// Detect is the discovery primitive the model registry calls; Probe layers a
// health verdict on top of the same query.
func (a *Adapter) Detect(ctx context.Context) core.Availability {
	if a.ollamaBin == "" {
		return core.Availability{Healthy: false, Detail: "no ollama binary configured"}
	}
	out, err := a.run(ctx, a.ollamaBin, "list")
	if err != nil {
		// Ollama not installed / daemon not responding: a clean not-available,
		// not an error. The registry simply offers no local models.
		return core.Availability{
			Healthy: false,
			Detail:  fmt.Sprintf("ollama not available: %v", err),
		}
	}
	models := parseOllamaList(out)
	return core.Availability{
		Healthy: len(models) > 0,
		Detail:  detectDetail(len(models)),
		Models:  models,
	}
}

// Probe reports whether the local stack is usable: healthy iff Ollama responds
// AND at least one model is installed. Detail carries the model count. Like
// Detect, a missing/unreachable Ollama is an unhealthy availability, never an
// error (spec: Probe — local healthy iff Ollama up and ≥1 model). The full
// model list rides along so a single Probe both health-checks and enumerates.
func (a *Adapter) Probe(ctx context.Context) (core.Availability, error) {
	return a.Detect(ctx), nil
}

// detectDetail renders the human-readable headroom detail for a model count.
func detectDetail(n int) string {
	if n == 0 {
		return "ollama running; no models installed"
	}
	if n == 1 {
		return "ollama: 1 model installed"
	}
	return fmt.Sprintf("ollama: %d models installed", n)
}

// parseOllamaList extracts model ids (the NAME column) from `ollama list`
// output. The command prints a header row followed by one row per model:
//
//	NAME               ID              SIZE      MODIFIED
//	qwen3-coder:latest abc123def456    4.7 GB    2 days ago
//	llama3.2:3b        0a8c26691023    2.0 GB    3 weeks ago
//
// The first whitespace-delimited field of each non-header, non-blank line is the
// model id. The header is identified by its "NAME" first field so a locale- or
// version-shifted header never leaks in as a model.
func parseOllamaList(out []byte) []string {
	var models []string
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		name := strings.Fields(line)[0]
		if name == "" || strings.EqualFold(name, "NAME") {
			continue
		}
		models = append(models, name)
	}
	return models
}

// execCommand is the default commandRunner: it runs name+args and returns
// combined output so a non-zero exit (e.g. daemon down) surfaces as an error the
// caller treats as "not available".
func execCommand(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, err
	}
	return out, nil
}
