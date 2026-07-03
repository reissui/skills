package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/reissui/clex/internal/ipc"
)

// stubHandler is an in-test ipc.Handler that records the last request and returns
// a scripted response, so the CLI's daemon commands are exercised against a real
// ipc.Listen socket with no daemon.
type stubHandler struct {
	last ipc.Request
	resp ipc.Response
}

func (h *stubHandler) Handle(_ context.Context, req ipc.Request) (ipc.Response, error) {
	h.last = req
	return h.resp, nil
}

// startStubDaemon spins an ipc.Listen server at the env's socket path with the
// given handler, and registers cleanup. It returns the handler for assertions.
func startStubDaemon(t *testing.T, e *env, resp ipc.Response) *stubHandler {
	t.Helper()
	h := &stubHandler{resp: resp}
	srv, err := ipc.Listen(e.socketPath(), h, nil)
	if err != nil {
		t.Fatalf("ipc.Listen: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go srv.Serve(ctx)
	t.Cleanup(func() {
		cancel()
		_ = srv.Close()
	})
	return h
}

// TestIPCRoundTrip is the acceptance IPC round-trip: a stub ipc server, a real
// ipc.NewClient dial, and an asserted Response.
func TestIPCRoundTrip(t *testing.T) {
	e := newTestEnv(t)
	want := ipc.Response{OK: true, Message: "pong", Status: &ipc.Status{Version: "test", Repo: "acme/widgets"}}
	startStubDaemon(t, e, want)

	resp, err := e.ipcClient().Do(context.Background(), ipc.Request{Command: ipc.CmdStatus})
	if err != nil {
		t.Fatalf("round-trip Do: %v", err)
	}
	if !resp.OK || resp.Status == nil || resp.Status.Repo != "acme/widgets" {
		t.Fatalf("unexpected response: %+v", resp)
	}
}

// TestDaemonRequiredNoDaemon: a daemon-backed command with no daemon running
// must fail with an error naming the socket path and exit 1.
func TestDaemonRequiredNoDaemon(t *testing.T) {
	for _, cmd := range []string{"status", "models", "costs", "pause", "resume"} {
		e := newTestEnv(t) // no daemon started
		code := run(e, []string{cmd})
		if code != exitError {
			t.Errorf("%s with no daemon: exit = %d, want 1", cmd, code)
		}
		if !strings.Contains(errBuf(e).String(), e.socketPath()) {
			t.Errorf("%s error should name the socket path %q; got: %s", cmd, e.socketPath(), errBuf(e))
		}
	}
}

func TestStatusRendersSnapshot(t *testing.T) {
	e := newTestEnv(t)
	startStubDaemon(t, e, ipc.Response{OK: true, Status: &ipc.Status{
		Version:  "1.0.0",
		Repo:     "acme/widgets",
		Paused:   true,
		Uptime:   "3m0s",
		Running:  []ipc.RunningIssue{{Issue: 14, Provider: "claude", Model: "opus", Stage: "build"}},
		Pipeline: map[string]int{"clex:building": 1, "clex:approved": 2},
		Explain:  []string{"#15 blocked by #14"},
	}})
	code := run(e, []string{"status"})
	if code != exitOK {
		t.Fatalf("status exit = %d, want 0", code)
	}
	out := outBuf(e).String()
	for _, want := range []string{"acme/widgets", "[paused]", "#14", "opus", "clex:building=1", "#15 blocked"} {
		if !strings.Contains(out, want) {
			t.Errorf("status output missing %q; got:\n%s", want, out)
		}
	}
}

func TestStatusJSON(t *testing.T) {
	e := newTestEnv(t)
	startStubDaemon(t, e, ipc.Response{OK: true, Status: &ipc.Status{Version: "1.0.0", Repo: "acme/widgets"}})
	code := run(e, []string{"status", "--json"})
	if code != exitOK {
		t.Fatalf("status --json exit = %d, want 0", code)
	}
	var st ipc.Status
	if err := json.Unmarshal(outBuf(e).Bytes(), &st); err != nil {
		t.Fatalf("status --json invalid: %v\n%s", err, outBuf(e))
	}
	if st.Repo != "acme/widgets" {
		t.Fatalf("unexpected status json: %+v", st)
	}
}

func TestSteerSendsRequest(t *testing.T) {
	e := newTestEnv(t)
	h := startStubDaemon(t, e, ipc.Response{OK: true, Message: "steered #14"})
	code := run(e, []string{"steer", "14", "focus", "on", "the", "flaky", "test"})
	if code != exitOK {
		t.Fatalf("steer exit = %d, want 0 (stderr: %s)", code, errBuf(e))
	}
	if h.last.Command != ipc.CmdSteer || h.last.Issue != 14 {
		t.Fatalf("unexpected steer request: %+v", h.last)
	}
	if h.last.Text != "focus on the flaky test" {
		t.Fatalf("steer text = %q, want joined args", h.last.Text)
	}
}

func TestSteerEpicTarget(t *testing.T) {
	e := newTestEnv(t)
	h := startStubDaemon(t, e, ipc.Response{OK: true, Message: "steered epic"})
	code := run(e, []string{"steer", "epic", "raise the bar"})
	if code != exitOK {
		t.Fatalf("steer epic exit = %d, want 0", code)
	}
	if h.last.Issue != 0 {
		t.Fatalf("epic steer should target issue 0; got %d", h.last.Issue)
	}
}

func TestSteerUsageError(t *testing.T) {
	e := newTestEnv(t)
	code := run(e, []string{"steer", "14"}) // missing text
	if code != exitError {
		t.Fatalf("steer without text: exit = %d, want 1", code)
	}
}

func TestModelsTable(t *testing.T) {
	e := newTestEnv(t)
	startStubDaemon(t, e, ipc.Response{OK: true, Models: []ipc.ModelHealth{
		{Model: "opus", Provider: "claude", Healthy: true, Detail: "top"},
		{Model: "local-l3", Provider: "ollama", Healthy: false, Detail: "offline"},
	}})
	code := run(e, []string{"models"})
	if code != exitOK {
		t.Fatalf("models exit = %d, want 0", code)
	}
	out := outBuf(e).String()
	if !strings.Contains(out, "✓") || !strings.Contains(out, "opus") || !strings.Contains(out, "✗") {
		t.Fatalf("models table missing marks/models; got:\n%s", out)
	}
}

func TestCostsDrift(t *testing.T) {
	e := newTestEnv(t)
	startStubDaemon(t, e, ipc.Response{OK: true, Costs: &ipc.Costs{
		SpentThisEpicUSD:     3.50,
		EstimatedThisEpicUSD: 2.00,
		SpentTodayUSD:        1.25,
		Lines:                []string{"opus: $3.50"},
	}})
	code := run(e, []string{"costs"})
	if code != exitOK {
		t.Fatalf("costs exit = %d, want 0", code)
	}
	out := outBuf(e).String()
	if !strings.Contains(out, "3.50") || !strings.Contains(out, "drift") {
		t.Fatalf("costs output missing spend/drift; got:\n%s", out)
	}
}

func TestPauseResumeMessages(t *testing.T) {
	for _, tc := range []struct{ cmd, msg string }{
		{"pause", "paused"},
		{"resume", "resumed"},
	} {
		e := newTestEnv(t)
		startStubDaemon(t, e, ipc.Response{OK: true, Message: tc.msg})
		code := run(e, []string{tc.cmd})
		if code != exitOK {
			t.Fatalf("%s exit = %d, want 0", tc.cmd, code)
		}
		if !strings.Contains(outBuf(e).String(), tc.msg) {
			t.Fatalf("%s output missing %q; got: %s", tc.cmd, tc.msg, outBuf(e))
		}
	}
}

// TestDaemonRejectionIsError: a Response with OK=false comes back as exit 1 with
// the daemon's error message.
func TestDaemonRejectionIsError(t *testing.T) {
	e := newTestEnv(t)
	startStubDaemon(t, e, ipc.Response{OK: false, Error: "stop requires an issue number"})
	code := run(e, []string{"status"})
	if code != exitError {
		t.Fatalf("rejected command exit = %d, want 1", code)
	}
	if !strings.Contains(errBuf(e).String(), "stop requires an issue number") {
		t.Fatalf("expected daemon error surfaced; got: %s", errBuf(e))
	}
}
