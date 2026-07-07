package daemon

import (
	"context"
	"fmt"
	"time"

	"github.com/reissui/clex/internal/core"
	"github.com/reissui/clex/internal/store"
)

// orphanComment is posted when an in-flight build is reverted on restart.
const orphanComment = "clex daemon restarted; this issue was mid-build with no live runner and has been reverted to `clex:approved` for re-dispatch. Its worktree (if any) is preserved."

// recover reconstructs in-flight state purely from GitHub labels at startup
// (spec: Source of truth: GitHub; Error handling & safety — "daemon restart
// re-derives work from GitHub"). Because no runner process survives a daemon
// restart, every issue found in clex:building is by definition orphaned: it has
// no live session. Each such issue is reverted to clex:approved with an
// explanatory comment so the normal reconcile/dispatch path picks it up again.
// The pipeline's idempotent Build then reuses the preserved worktree and any
// open PR rather than restarting from zero.
//
// Any stale RunningSessions rows in SQLite (which likewise cannot outlive the
// process) are closed as stopped so bookkeeping matches reality; losing the DB
// entirely is still safe because labels — not the DB — drive recovery.
func (d *Daemon) recover(ctx context.Context) error {
	// Close stale session rows first (best-effort; DB is bookkeeping only).
	d.closeStaleSessions()

	issues, err := d.deps.GH.ListIssues(ctx, d.cfg.Repo)
	if err != nil {
		return fmt.Errorf("recover: list issues: %w", err)
	}
	var reverted int
	for _, iss := range issues {
		// A clex:researching issue was mid-plan; no planner survives a restart
		// either. Revert to clex:idea so reconcile re-plans it (Plan resumes an
		// already-created epic via the PlannedFromMarker scan, so no duplicates).
		if iss.State == core.StateResearching {
			if err := d.deps.GH.SetState(ctx, d.cfg.Repo, iss.Number, core.StateIdea); err != nil {
				d.log.Warn("recover: revert plan label", "issue", iss.Number, "err", d.red.Redact(err.Error()))
				continue
			}
			d.logEvent(ctx, iss.Number, "recover", "orphaned clex:researching reverted to clex:idea")
			reverted++
			continue
		}
		if iss.State != core.StateBuilding {
			continue
		}
		// Orphan: revert to approved, then comment. Order matters only for
		// idempotency — SetState is a label swap that tolerates re-runs.
		if err := d.deps.GH.SetState(ctx, d.cfg.Repo, iss.Number, core.StateApproved); err != nil {
			d.log.Warn("recover: revert label", "issue", iss.Number, "err", d.red.Redact(err.Error()))
			continue
		}
		if err := d.deps.GH.Comment(ctx, d.cfg.Repo, iss.Number, orphanComment); err != nil {
			d.log.Warn("recover: orphan comment", "issue", iss.Number, "err", d.red.Redact(err.Error()))
		}
		d.logEvent(ctx, iss.Number, "recover", "orphaned clex:building reverted to clex:approved")
		reverted++
	}
	if reverted > 0 {
		d.log.Info("crash recovery complete", "reverted", reverted)
		d.notify(ctx, fmt.Sprintf("↺ recovered from restart: %d orphaned build(s) reverted to approved", reverted))
	} else {
		d.log.Info("crash recovery: no orphaned builds")
	}
	return nil
}

// closeStaleSessions marks any RunningSessions rows as stopped. These belong to
// a prior process; leaving them "running" would corrupt status and the running
// count the quiesce hook consults.
func (d *Daemon) closeStaleSessions() {
	sessions, err := d.deps.Store.RunningSessions()
	if err != nil {
		d.log.Warn("recover: list running sessions", "err", err.Error())
		return
	}
	for _, s := range sessions {
		if err := d.deps.Store.FinishSession(s.ID, store.SessionStopped, time.Now()); err != nil {
			d.log.Warn("recover: close session", "id", s.ID, "err", err.Error())
		}
	}
}
