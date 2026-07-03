//go:build e2e

package e2e

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/reissui/clex/internal/core"
)

// scriptRunner is a core.Runner that executes the compiled clex-fake-runner
// binary, handing it a script chosen per-run, and parses the binary's
// core.Event JSON stream back into core.Events. It reuses the same
// stream-to-channel shape the real claude/codex adapters use (read a line,
// decode a core.Event, forward it), so the pipeline's runToCompletion choke
// point sees an ordinary runner (spec: brief-20 — "reuse the real
// claude/codex adapter's stream-parsing approach; the fake-runner adapter lives
// in e2e/").
//
// The env handed to the child is minimal and allowlisted, and never carries
// ANTHROPIC_* secrets — matching the security contract the real adapters honor
// (spec: Security model — child env is allowlisted).
type scriptRunner struct {
	bin string // path to the compiled clex-fake-runner
	// scriptFor returns the script JSON for a given task. It is how the harness
	// makes one binary behave as a planner, a builder, or a reviewer depending on
	// what the pipeline is running.
	scriptFor func(task core.Task) fakeScript
	// spool is a writable dir for per-run script files.
	spool string
}

// fakeScript mirrors cmd/clex-fake-runner's script JSON so the harness can build
// scripts in-process and marshal them to the file the binary reads.
type fakeScript struct {
	DelayMS       int              `json:"delay_ms,omitempty"`
	SessionID     string           `json:"session_id,omitempty"`
	ExitCode      int              `json:"exit_code,omitempty"`
	Writes        []fakeScriptFile `json:"writes,omitempty"`
	Commit        bool             `json:"commit,omitempty"`
	CommitMessage string           `json:"commit_message,omitempty"`
	Events        []fakeScriptEvt  `json:"events,omitempty"`
}

type fakeScriptFile struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

type fakeScriptEvt struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
	Err  string `json:"err,omitempty"`
	In   int    `json:"in,omitempty"`
	Out  int    `json:"out,omitempty"`
}

// Run writes the chosen script to a temp file and execs the fake-runner in dir,
// streaming its stdout as core.Events on the returned channel. It honors ctx
// cancellation by killing the child (runner contract: cancelling ctx stops the
// child).
func (s *scriptRunner) Run(ctx context.Context, task core.Task, dir string) (<-chan core.Event, error) {
	sc := s.scriptFor(task)
	scriptPath, err := s.writeScript(sc)
	if err != nil {
		return nil, err
	}

	cmd := exec.CommandContext(ctx, s.bin, "-script", scriptPath)
	cmd.Dir = dir
	// Minimal allowlisted env: PATH (git lookup) + the script location. No
	// ANTHROPIC_* or inherited secrets (security contract).
	cmd.Env = []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + os.Getenv("HOME"),
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}

	out := make(chan core.Event, 16)
	go func() {
		defer close(out)
		scan := bufio.NewScanner(stdout)
		scan.Buffer(make([]byte, 0, 64*1024), 1<<20)
		for scan.Scan() {
			line := scan.Bytes()
			if len(line) == 0 {
				continue
			}
			var ev core.Event
			if err := json.Unmarshal(line, &ev); err != nil {
				continue // ignore any non-JSON chatter
			}
			select {
			case out <- ev:
			case <-ctx.Done():
				_ = cmd.Wait()
				return
			}
		}
		// Wait reaps the child; a non-zero exit with no error event still closes
		// the stream cleanly (the pipeline treats absence of a result as failure
		// only if an error event was emitted).
		_ = cmd.Wait()
	}()
	return out, nil
}

// Probe reports the fake runner as healthy. The daemon seeds optimistic health
// and never probes in the e2e flow, but a real Runner must implement it.
func (s *scriptRunner) Probe(ctx context.Context) (core.Availability, error) {
	return core.Availability{Healthy: true, Detail: "fake runner", Models: []string{"fake"}}, nil
}

// writeScript marshals sc to a unique file under the runner's spool and returns
// its path.
func (s *scriptRunner) writeScript(sc fakeScript) (string, error) {
	if err := os.MkdirAll(s.spool, 0o700); err != nil {
		return "", err
	}
	f, err := os.CreateTemp(s.spool, "script-*.json")
	if err != nil {
		return "", err
	}
	defer f.Close()
	if err := json.NewEncoder(f).Encode(sc); err != nil {
		return "", err
	}
	return f.Name(), nil
}

// buildFakeRunner compiles cmd/clex-fake-runner once and returns the binary
// path. The build has a generous timeout and fails the test loudly on error.
func buildFakeRunner(t interface {
	Helper()
	Fatalf(string, ...any)
	TempDir() string
}) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "clex-fake-runner")
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "go", "build", "-o", bin, "./cmd/clex-fake-runner")
	// Build from the module root (one level up from e2e/).
	cmd.Dir = moduleRoot()
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build clex-fake-runner: %v\n%s", err, out)
	}
	return bin
}

// moduleRoot returns the repository root (the parent of the e2e directory),
// resolved from the test's working directory which go test sets to the package
// dir.
func moduleRoot() string {
	wd, err := os.Getwd()
	if err != nil {
		return ".."
	}
	return filepath.Dir(wd)
}
