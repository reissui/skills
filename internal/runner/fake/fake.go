// Package fake adapts the clex-fake-runner test binary to core.Runner.
//
// The adapter is intentionally production-wired, not test-only: a config may
// declare a provider with kind = "fake" and route any role to models backed by
// that provider. The runner still performs no network calls and only executes
// the configured clex-fake-runner binary against a JSON script.
package fake

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"github.com/reissui/clex/internal/core"
)

// DefaultBinary is resolved on PATH when no provider binary is configured.
const DefaultBinary = "clex-fake-runner"

// Adapter shells out to clex-fake-runner and parses its core.Event JSONL stream.
type Adapter struct {
	binary string
	script string
	env    []string
}

var _ core.Runner = (*Adapter)(nil)

// Option configures an Adapter.
type Option func(*Adapter)

// WithBinary overrides the clex-fake-runner executable path.
func WithBinary(path string) Option {
	return func(a *Adapter) { a.binary = path }
}

// WithScript sets the script passed as -script to clex-fake-runner.
func WithScript(path string) Option {
	return func(a *Adapter) { a.script = path }
}

// WithEnv overrides the child environment. Tests use this to keep execution
// hermetic.
func WithEnv(env []string) Option {
	return func(a *Adapter) { a.env = append([]string(nil), env...) }
}

// New constructs a fake runner adapter.
func New(opts ...Option) *Adapter {
	a := &Adapter{binary: DefaultBinary}
	for _, opt := range opts {
		opt(a)
	}
	if a.binary == "" {
		a.binary = DefaultBinary
	}
	return a
}

// Run executes the configured fake-runner script in dir and streams core.Event
// values decoded from stdout. The task is not interpreted by the adapter; script
// selection belongs to the caller/config, keeping this provider deterministic.
func (a *Adapter) Run(ctx context.Context, task core.Task, dir string) (<-chan core.Event, error) {
	if a.binary == "" {
		return nil, fmt.Errorf("fake: no binary configured")
	}
	args := []string{}
	if strings.TrimSpace(a.script) != "" {
		args = append(args, "-script", a.script)
	}
	cmd := exec.CommandContext(ctx, a.binary, args...)
	cmd.Dir = dir
	cmd.Env = a.childEnv()
	cmd.Stderr = io.Discard

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("fake: stdout pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("fake: start %s: %w", a.binary, err)
	}

	out := make(chan core.Event, 16)
	go func() {
		defer close(out)
		scanner := bufio.NewScanner(stdout)
		scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line == "" {
				continue
			}
			var ev core.Event
			if err := json.Unmarshal([]byte(line), &ev); err != nil {
				emit(ctx, out, core.Event{Type: core.EventError, Err: "fake: decode event: " + err.Error()})
				continue
			}
			emit(ctx, out, ev)
		}
		if err := scanner.Err(); err != nil {
			emit(ctx, out, core.Event{Type: core.EventError, Err: "fake: read stream: " + err.Error()})
		}
		if err := cmd.Wait(); err != nil && ctx.Err() == nil {
			emit(ctx, out, core.Event{Type: core.EventError, Err: "fake: " + err.Error()})
		}
	}()
	_ = task // task is intentionally script data, not adapter control.
	return out, nil
}

// Probe reports whether the fake provider is runnable from the current config.
func (a *Adapter) Probe(context.Context) (core.Availability, error) {
	if a.binary == "" {
		return core.Availability{Healthy: false, Detail: "no fake runner binary configured"}, nil
	}
	if _, err := exec.LookPath(a.binary); err != nil {
		if _, statErr := os.Stat(a.binary); statErr != nil {
			return core.Availability{Healthy: false, Detail: "fake runner binary not found: " + err.Error()}, nil
		}
	}
	script := strings.TrimSpace(a.script)
	if script == "" {
		script = os.Getenv("CLEX_FAKE_SCRIPT")
	}
	if script == "" {
		return core.Availability{Healthy: false, Detail: "no fake runner script configured"}, nil
	}
	if _, err := os.Stat(script); err != nil {
		return core.Availability{Healthy: false, Detail: "fake runner script not readable: " + err.Error()}, nil
	}
	return core.Availability{Healthy: true, Detail: "fake runner"}, nil
}

func (a *Adapter) childEnv() []string {
	if a.env != nil {
		return append([]string(nil), a.env...)
	}
	env := []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + os.Getenv("HOME"),
	}
	if tmp := os.Getenv("TMPDIR"); tmp != "" {
		env = append(env, "TMPDIR="+tmp)
	}
	if script := os.Getenv("CLEX_FAKE_SCRIPT"); script != "" {
		env = append(env, "CLEX_FAKE_SCRIPT="+script)
	}
	return env
}

func emit(ctx context.Context, out chan<- core.Event, ev core.Event) {
	select {
	case out <- ev:
	case <-ctx.Done():
	}
}
