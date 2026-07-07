// Package claude is the Claude Code adapter: a core.Runner that shells out to
// the official `claude` binary and normalizes its stream-json output into
// core.Events. It NEVER calls any Anthropic API — Anthropic permits Max/Pro
// subscription usage only through the official CLI, so routing subscription
// credentials through a direct API call is prohibited (spec: Compliance note).
//
// The adapter also enforces two security invariants from the spec's Security
// model: child processes get a minimal allowlisted environment with
// ANTHROPIC_API_KEY / ANTHROPIC_AUTH_TOKEN stripped (so subscription auth can
// never silently fall back to metered API billing), and the child runs in its
// own process group so cancellation kills the whole tree with no orphans.
package claude

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/reissui/clex/internal/core"
)

// DefaultBinary is the command looked up on PATH when no binary is configured.
const DefaultBinary = "claude"

// Event is an alias for core.Event so the adapter's channel type matches the
// core.Runner interface exactly while keeping call sites terse.
type Event = core.Event

// Adapter implements core.Runner by shelling out to the Claude Code CLI. The
// zero value is not usable; construct one with New.
type Adapter struct {
	// binPath is the claude executable. Injectable so tests can point it at a
	// fake script and CI never touches the real CLI.
	binPath string
	// skillsRoot is the clex skills directory whose named subdirectories are
	// symlinked into a run's .claude/skills before spawn. Empty disables
	// injection.
	skillsRoot string
	// envFunc builds the child environment. It defaults to the strict
	// allowlist over the parent environment; tests override it to append the
	// control vars their fake binary needs (which the allowlist would strip).
	envFunc func() []string
}

// Option configures an Adapter.
type Option func(*Adapter)

// WithBinary overrides the claude executable path (used in tests and for
// non-standard install locations).
func WithBinary(path string) Option {
	return func(a *Adapter) { a.binPath = path }
}

// WithSkillsRoot sets the directory that holds installed skills. Named skills
// from a task are symlinked out of here into the run's worktree.
func WithSkillsRoot(root string) Option {
	return func(a *Adapter) { a.skillsRoot = root }
}

// New returns a Claude Code adapter. With no options it resolves the `claude`
// binary from PATH lazily at Run/Probe time.
func New(opts ...Option) *Adapter {
	a := &Adapter{binPath: DefaultBinary}
	for _, opt := range opts {
		opt(a)
	}
	if a.envFunc == nil {
		a.envFunc = func() []string { return childEnv(os.Environ()) }
	}
	return a
}

// Run spawns `claude -p <prompt> --output-format stream-json --verbose` in dir
// and streams normalized events until the CLI exits. The returned channel is
// closed when the run finishes; its terminal event is an EventResult on success
// or an EventError on failure. Cancelling ctx kills the child's process group.
func (a *Adapter) Run(ctx context.Context, task core.Task, dir string) (<-chan Event, error) {
	if task.Skills != nil {
		if err := a.injectSkills(dir, task.Skills); err != nil {
			return nil, fmt.Errorf("claude: inject skills: %w", err)
		}
	}

	args := buildArgs(task)
	cmd := exec.Command(a.binPath, args...)
	cmd.Dir = dir
	cmd.Env = a.envFunc()
	// Own process group so a cancel can signal the whole tree, not just the
	// direct child (the CLI spawns tool subprocesses).
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("claude: stdout pipe: %w", err)
	}
	// The CLI writes stdin-wait warnings and auth errors to stderr; keep it off
	// the parent's fds without discarding structured stdout.
	cmd.Stderr = nil

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("claude: start %s: %w", a.binPath, err)
	}

	out := make(chan Event)
	go a.pump(ctx, cmd, stdout, out)
	return out, nil
}

// pump reads the child's stdout line by line, forwards normalized events, and
// guarantees the process group is reaped when ctx is cancelled or stdout ends.
func (a *Adapter) pump(ctx context.Context, cmd *exec.Cmd, stdout io.Reader, out chan<- Event) {
	defer close(out)

	// A goroutine tied to ctx kills the whole process group on cancellation so
	// no child is orphaned; killing the negative pid targets the group.
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			killGroup(cmd)
		case <-done:
		}
	}()

	var sessionID string
	scanner := bufio.NewScanner(stdout)
	// CLI lines (esp. init with the full tool/mcp inventory) can be large; give
	// the scanner room well past the default 64KiB.
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(strings.TrimSpace(string(line))) == 0 {
			continue
		}
		events, sid := parseLine(line)
		if sid != "" {
			sessionID = sid
		}
		for _, ev := range events {
			// Backfill the latched session id on the terminal event when the
			// result line itself omitted it.
			if ev.Type == core.EventResult && ev.SessionID == "" {
				ev.SessionID = sessionID
			}
			select {
			case out <- ev:
			case <-ctx.Done():
				killGroup(cmd)
				_ = cmd.Wait()
				return
			}
		}
	}

	waitErr := cmd.Wait()
	if scanErr := scanner.Err(); scanErr != nil {
		emit(ctx, out, Event{Type: core.EventError, Err: "claude: read stream: " + scanErr.Error()})
		return
	}
	// A non-zero exit with no structured result line still needs to surface as
	// an error so the pipeline does not mistake a crash for a clean run.
	if waitErr != nil && ctx.Err() == nil {
		emit(ctx, out, Event{Type: core.EventError, Err: "claude: " + waitErr.Error(), SessionID: sessionID})
	}
}

// emit sends ev unless ctx is already cancelled.
func emit(ctx context.Context, out chan<- Event, ev Event) {
	select {
	case out <- ev:
	case <-ctx.Done():
	}
}

// buildArgs assembles the claude argv for a task. -p/--print with
// stream-json+verbose is the machine-readable batch mode; --resume continues an
// existing session; --effort maps the task's reasoning level.
func buildArgs(task core.Task) []string {
	args := []string{
		"-p", task.Prompt,
		"--output-format", "stream-json",
		"--verbose",
	}
	if task.ResumeID != "" {
		args = append(args, "--resume", task.ResumeID)
	}
	if e := effortFlag(task.Effort); e != "" {
		args = append(args, "--effort", e)
	}
	return args
}

// effortFlag maps a core effort level onto the claude CLI's --effort vocabulary
// (low, medium, high, xhigh, max). Unknown/empty values omit the flag so the
// CLI uses its own default. Note: Claude Code has no fast-output flag, so
// task.Fast is intentionally not translated here (documented deviation — the
// spec says translate fast "where the provider supports it").
func effortFlag(effort string) string {
	switch strings.ToLower(strings.TrimSpace(effort)) {
	case "low", "medium", "high", "xhigh", "max":
		return strings.ToLower(strings.TrimSpace(effort))
	default:
		return ""
	}
}

// injectSkills symlinks each named skill directory from the adapter's skills
// root into dir/.claude/skills so the CLI discovers them for this run. Missing
// skills root or a missing named skill are skipped rather than fatal — the
// clex skills installer owns provisioning; this adapter only wires up links.
func (a *Adapter) injectSkills(dir string, skills []string) error {
	if a.skillsRoot == "" || len(skills) == 0 {
		return nil
	}
	dst := filepath.Join(dir, ".claude", "skills")
	if err := os.MkdirAll(dst, 0o700); err != nil {
		return err
	}
	for _, name := range skills {
		if name == "" || strings.ContainsRune(name, os.PathSeparator) {
			continue // guard against path traversal in skill names
		}
		src := filepath.Join(a.skillsRoot, name)
		if _, err := os.Stat(src); err != nil {
			continue // not installed here; discovery falls through to others
		}
		link := filepath.Join(dst, name)
		if _, err := os.Lstat(link); err == nil {
			continue // already linked
		}
		if err := os.Symlink(src, link); err != nil {
			return err
		}
	}
	return nil
}
