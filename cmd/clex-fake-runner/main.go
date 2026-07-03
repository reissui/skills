// Command clex-fake-runner is a deterministic stand-in for the official
// claude / codex CLIs, used by clex's opt-in end-to-end test suite (build tag
// e2e, package e2e). It speaks the exact core.Event JSON protocol on stdout
// that the real runner adapters emit, but instead of calling a model it replays
// a scripted sequence of events and side effects. This lets the whole
// idea → plan → build → review → assemble pipeline run against a scratch git
// repo with zero live services (spec: Testing strategy — "a deterministic fake
// runner (scripted binary emitting the event protocol) drives the full
// idea→PR flow").
//
// # Protocol
//
// The binary writes one JSON object per line to stdout, each a core.Event:
//
//	{"type":"text","text":"..."}
//	{"type":"tool_use","text":"Edit"}
//	{"type":"usage","tokens":{"in":1200,"out":8}}
//	{"type":"result","session_id":"sess-...","tokens":{"in":1200,"out":40}}
//	{"type":"error","err":"..."}
//
// The stream is terminated by a single "result" event (or, on failure, an
// "error" event). This mirrors internal/core.Event field-for-field, so the
// runner adapter in the e2e package can reuse the real stream-parsing shape.
//
// # Script format
//
// The script is a JSON document supplied either via the -script flag (a path)
// or the CLEX_FAKE_SCRIPT env var (a path). Its shape:
//
//	{
//	  "delay_ms": 0,                       // per-step delay before each event
//	  "session_id": "sess-abc",            // session id stamped on the result
//	  "exit_code": 0,                      // process exit code (default 0)
//	  "writes": [                          // files written into $CWD (the worktree)
//	    {"path": "clex_touch.txt", "content": "built by fake\n"}
//	  ],
//	  "commit": true,                      // git add -A && git commit in $CWD after writes
//	  "commit_message": "fake build",      // commit subject (default "clex-fake-runner build")
//	  "events": [                          // events emitted on stdout, in order
//	    {"type": "text", "text": "Working on it."},
//	    {"type": "tool_use", "text": "Edit"},
//	    {"type": "usage", "in": 1000, "out": 20},
//	    {"type": "result"}                 // session_id is filled from the top-level field
//	  ]
//	}
//
// A "result" event without an explicit session_id inherits the top-level
// session_id. If no events are supplied, the binary emits a minimal
// text+result pair so a stage always sees a terminal event.
//
// # Working directory
//
// The binary runs in the directory the adapter chose (a per-issue worktree for
// build, the primary checkout for review). "writes" paths are resolved relative
// to that directory, and "commit" runs git there — this is how a fake build
// produces real branch content the pipeline can open a PR from and merge.
//
// The environment is intentionally not inspected: the runner adapter is
// responsible for handing the child a minimal allowlisted env (spec: Security
// model). This binary is a leaf test tool and never reaches the network.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// scriptEvent is one emitted event in the script. It flattens core.Event plus
// the usage counts so a script author writes {"type":"usage","in":1,"out":2}.
type scriptEvent struct {
	Type      string `json:"type"`
	Text      string `json:"text,omitempty"`
	Err       string `json:"err,omitempty"`
	SessionID string `json:"session_id,omitempty"`
	In        int    `json:"in,omitempty"`
	Out       int    `json:"out,omitempty"`
}

// fileWrite is a single file the fake runner materializes in its working dir.
type fileWrite struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

// script is the full behavior description parsed from the script file.
type script struct {
	DelayMS       int           `json:"delay_ms"`
	SessionID     string        `json:"session_id"`
	ExitCode      int           `json:"exit_code"`
	Writes        []fileWrite   `json:"writes"`
	Commit        bool          `json:"commit"`
	CommitMessage string        `json:"commit_message"`
	Events        []scriptEvent `json:"events"`
}

// event is the on-the-wire shape emitted to stdout. It is a local mirror of
// internal/core.Event so this command has no dependency on the module's
// internal packages (a leaf test binary must build standalone) while remaining
// byte-compatible with the adapter's parser.
type event struct {
	Type      string `json:"type"`
	Text      string `json:"text,omitempty"`
	Tokens    usage  `json:"tokens,omitempty"`
	SessionID string `json:"session_id,omitempty"`
	Err       string `json:"err,omitempty"`
}

// usage mirrors core.Usage.
type usage struct {
	In  int `json:"in"`
	Out int `json:"out"`
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run parses the script, performs its side effects, emits its events, and
// returns the scripted exit code. It is separated from main so behavior is
// unit-testable.
func run(args []string, stdout, stderr *os.File) int {
	path := scriptPath(args)
	if path == "" {
		fmt.Fprintln(stderr, "clex-fake-runner: no script (set -script <path> or CLEX_FAKE_SCRIPT)")
		return 2
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		fmt.Fprintf(stderr, "clex-fake-runner: read script: %v\n", err)
		return 2
	}
	var sc script
	if err := json.Unmarshal(raw, &sc); err != nil {
		fmt.Fprintf(stderr, "clex-fake-runner: parse script: %v\n", err)
		return 2
	}

	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(stderr, "clex-fake-runner: getwd: %v\n", err)
		return 2
	}

	// 1. Materialize any files into the working directory.
	for _, w := range sc.Writes {
		dst := filepath.Join(cwd, w.Path)
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			fmt.Fprintf(stderr, "clex-fake-runner: mkdir for %s: %v\n", w.Path, err)
			return 2
		}
		if err := os.WriteFile(dst, []byte(w.Content), 0o644); err != nil {
			fmt.Fprintf(stderr, "clex-fake-runner: write %s: %v\n", w.Path, err)
			return 2
		}
	}

	// 2. Commit the working tree so the issue branch carries the change (a real
	//    build's runner commits; the pipeline opens a PR from the branch).
	if sc.Commit {
		if err := gitCommit(cwd, commitMessageOr(sc.CommitMessage)); err != nil {
			fmt.Fprintf(stderr, "clex-fake-runner: commit: %v\n", err)
			return 2
		}
	}

	// 3. Emit the scripted events, honoring the per-step delay.
	enc := json.NewEncoder(stdout)
	events := sc.Events
	if len(events) == 0 {
		events = defaultEvents()
	}
	for _, se := range events {
		if sc.DelayMS > 0 {
			time.Sleep(time.Duration(sc.DelayMS) * time.Millisecond)
		}
		if err := enc.Encode(toEvent(se, sc.SessionID)); err != nil {
			fmt.Fprintf(stderr, "clex-fake-runner: emit event: %v\n", err)
			return 2
		}
	}
	return sc.ExitCode
}

// scriptPath resolves the script location from -script <path> or the
// CLEX_FAKE_SCRIPT env var (flag wins).
func scriptPath(args []string) string {
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "-script" || a == "--script":
			if i+1 < len(args) {
				return args[i+1]
			}
		case len(a) > 9 && a[:9] == "-script=":
			return a[9:]
		case len(a) > 10 && a[:10] == "--script=":
			return a[10:]
		}
	}
	return os.Getenv("CLEX_FAKE_SCRIPT")
}

// toEvent converts a scriptEvent into the wire event, inheriting the top-level
// session id for a result event that did not set its own.
func toEvent(se scriptEvent, defaultSession string) event {
	ev := event{
		Type:      se.Type,
		Text:      se.Text,
		SessionID: se.SessionID,
		Err:       se.Err,
		Tokens:    usage{In: se.In, Out: se.Out},
	}
	if se.Type == "result" && ev.SessionID == "" {
		ev.SessionID = defaultSession
	}
	return ev
}

// defaultEvents is the minimal stream when a script supplies no events: a line
// of text followed by a terminal result, so every stage sees completion.
func defaultEvents() []scriptEvent {
	return []scriptEvent{
		{Type: "text", Text: "clex-fake-runner: no scripted events."},
		{Type: "result"},
	}
}

// commitMessageOr returns msg or a stable default.
func commitMessageOr(msg string) string {
	if msg == "" {
		return "clex-fake-runner build"
	}
	return msg
}

// gitCommit stages everything in dir and commits it. It configures a local
// identity so the commit succeeds even in a repo without a global git user, and
// tolerates an empty commit (nothing to do) as success.
func gitCommit(dir, message string) error {
	if _, err := git(dir, "add", "-A"); err != nil {
		return err
	}
	// A no-op commit (no staged changes) is fine — the branch already carries
	// the content; treat "nothing to commit" as success.
	if out, err := git(dir, "-c", "user.email=fake@clex.test", "-c", "user.name=clex-fake-runner",
		"commit", "-m", message); err != nil {
		if isNothingToCommit(out) {
			return nil
		}
		return fmt.Errorf("%v: %s", err, out)
	}
	return nil
}

// git runs a git subcommand in dir and returns combined output.
func git(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// isNothingToCommit reports whether git output indicates an empty commit.
func isNothingToCommit(out string) bool {
	for _, s := range []string{"nothing to commit", "no changes added", "working tree clean"} {
		if contains(out, s) {
			return true
		}
	}
	return false
}

// contains is a tiny substring test (avoids importing strings for one call).
func contains(hay, needle string) bool {
	if len(needle) == 0 {
		return true
	}
	for i := 0; i+len(needle) <= len(hay); i++ {
		if hay[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
