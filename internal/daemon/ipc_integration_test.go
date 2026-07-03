package daemon

import (
	"context"
	"testing"
	"time"

	"github.com/reissui/clex/internal/gh"
	"github.com/reissui/clex/internal/ipc"
)

// TestIPCControlEndToEnd wires the real ipc.Server to the daemon's Handler and
// drives it with the real ipc.Client — the exact coupling #17 relies on. It
// proves the protocol round-trips pause/status/stop/steer through the daemon's
// serialized loop over a 0600 socket, with no CLI code (that is #17's job).
func TestIPCControlEndToEnd(t *testing.T) {
	stages := newFakeStages()
	gate := stages.holdBuild(200)
	h := newHarness(t, stages)
	h.approvedIssue(200, nil, []string{"ipc/**"})
	h.runDaemon(t)

	// Start the IPC server bound to the daemon.
	sockPath := ipc.SocketPath(h.d.cfg.Home)
	srv, err := ipc.Listen(sockPath, h.d, nil)
	if err != nil {
		t.Fatalf("ipc.Listen: %v", err)
	}
	sctx, scancel := context.WithCancel(context.Background())
	go func() { _ = srv.Serve(sctx) }()
	t.Cleanup(func() { scancel(); _ = srv.Close() })

	cli := ipc.NewClient(sockPath).WithDialTimeout(time.Second)
	ctx := context.Background()

	// Wait for the build to be active, then query status via IPC.
	if !waitFor(time.Second, func() bool {
		h.d.mu.Lock()
		defer h.d.mu.Unlock()
		return len(h.d.running) == 1
	}) {
		t.Fatal("build never became active")
	}
	resp, err := cli.Status(ctx)
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if resp.Status == nil || len(resp.Status.Running) != 1 || resp.Status.Running[0].Issue != 200 {
		t.Fatalf("status did not report the running build: %+v", resp.Status)
	}

	// Pause via IPC.
	if r, err := cli.Pause(ctx); err != nil || !r.OK {
		t.Fatalf("pause: %v resp=%+v", err, r)
	}
	if !h.d.isPaused() {
		t.Fatal("daemon not paused after IPC pause")
	}

	// Stop the running issue via IPC. Release the build gate so the cancelled
	// build goroutine can finish and drain out of the running set.
	r, err := cli.Stop(ctx, 200)
	if err != nil || !r.OK {
		t.Fatalf("stop: %v resp=%+v", err, r)
	}
	close(gate)
	// Wait until #200 has fully left the running set so the subsequent steer
	// takes the idle-issue path (body edit) rather than the active-runner path.
	if !waitFor(time.Second, func() bool {
		h.d.mu.Lock()
		defer h.d.mu.Unlock()
		_, has := h.d.running[200]
		return !has
	}) {
		t.Fatal("#200 did not leave the running set after stop")
	}

	// Steer via IPC (idle issue after stop; the daemon appends to the body).
	if _, err := cli.Steer(ctx, 200, "use fewer allocations"); err != nil {
		t.Fatalf("steer: %v", err)
	}
	if !waitFor(time.Second, func() bool {
		iss, gerr := h.gh.GetIssue(ctx, testRepo, 200)
		return gerr == nil && contains(iss.Body, "use fewer allocations")
	}) {
		t.Fatal("steer via IPC did not reach the issue body")
	}

	// Models + costs commands answer without error.
	if r, err := cli.Models(ctx); err != nil || !r.OK {
		t.Fatalf("models: %v resp=%+v", err, r)
	}
	if r, err := cli.Costs(ctx); err != nil || !r.OK {
		t.Fatalf("costs: %v resp=%+v", err, r)
	}

	_ = gh.Repo{} // keep gh import if assertions above change
}
