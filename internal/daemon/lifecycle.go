package daemon

import (
	"context"
	"time"

	"github.com/reissui/clex/internal/core"
	"github.com/reissui/clex/internal/store"
	"github.com/reissui/clex/internal/version"
)

// versionString returns the build version, defaulting to "dev".
func versionString() string {
	if version.Version == "" {
		return "dev"
	}
	return version.Version
}

// Quiesced reports whether the daemon is safe to swap the binary under: zero
// runners are active AND no cost gate is pending (spec: Update quiesce hook —
// #19). Self-update stages a new binary and calls ApplyWhenQuiesced; the actual
// swap must only happen while this is true, so an in-flight build is never
// killed mid-run by an update.
func (d *Daemon) Quiesced() bool {
	d.mu.Lock()
	running := len(d.running)
	gate := d.pendingGate
	d.mu.Unlock()
	return running == 0 && gate == ""
}

// ApplyWhenQuiesced is the hook #19 (self-update) calls to apply a staged
// update. It invokes apply exactly once, the first moment the daemon is
// quiesced (no active runners, no pending gate), polling at a modest interval.
// It blocks until either the update is applied or ctx is cancelled; it returns
// the error from apply (or ctx.Err()). Exposing it as a hook keeps the update
// policy in #19 while the daemon owns the safety condition.
//
// The check-then-apply is performed while holding no long-lived lock; because a
// new build can only be started by the loop goroutine, and callers of this hook
// typically also pause the daemon first, the quiesced window is stable enough to
// apply within. Deferring on a single active runner is exactly the tested
// behavior: with one runner active, apply is not called until it finishes.
func (d *Daemon) ApplyWhenQuiesced(ctx context.Context, apply func() error) error {
	const poll = 200 * time.Millisecond
	if d.Quiesced() {
		return apply()
	}
	t := time.NewTicker(poll)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
			if d.Quiesced() {
				return apply()
			}
		}
	}
}

// shutdown performs a clean stop: it cancels every in-flight runner (their
// build goroutines observe the cancelled context and exit), reverts their labels
// to clex:approved so a restart re-dispatches from a clean state, and flushes
// the store. It is called when Run's context is cancelled (SIGTERM). Worktrees
// are preserved so the resumed build reuses them.
func (d *Daemon) shutdown() {
	d.log.Info("clexd shutting down")

	// Snapshot and clear the running set under lock.
	d.mu.Lock()
	running := make([]*runState, 0, len(d.running))
	for _, rs := range d.running {
		running = append(running, rs)
	}
	d.running = make(map[int]*runState)
	d.mu.Unlock()

	// Cancel each runner and revert its label. Use a fresh, bounded context:
	// Run's context is already cancelled, so label reverts need their own.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for _, rs := range running {
		rs.cancel()
		if err := d.deps.GH.SetState(ctx, d.cfg.Repo, rs.issue, core.StateApproved); err != nil {
			d.log.Warn("shutdown: revert label", "issue", rs.issue, "err", d.red.Redact(err.Error()))
		}
		d.logEvent(ctx, rs.issue, "shutdown", "runner cancelled on SIGTERM; label reverted, worktree preserved")
	}

	// Close any still-open session rows and flush the store.
	if sessions, err := d.deps.Store.RunningSessions(); err == nil {
		for _, s := range sessions {
			_ = d.deps.Store.FinishSession(s.ID, store.SessionStopped, time.Now())
		}
	}
	if err := d.deps.Store.Close(); err != nil {
		d.log.Warn("shutdown: close store", "err", err.Error())
	}
	d.log.Info("clexd stopped", "reverted_runners", len(running))
}
