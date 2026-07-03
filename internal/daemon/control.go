package daemon

import (
	"context"
	"fmt"
	"strings"

	"github.com/reissui/clex/internal/core"
	"github.com/reissui/clex/internal/gh"
)

// controlKind classifies a control action funneled onto the loop.
type controlKind int

const (
	ctlPause controlKind = iota
	ctlResume
	ctlStop
	ctlSteer
)

// controlAction is a pause/resume/stop/steer request routed onto the loop so it
// is serialized with dispatch decisions. reply, if non-nil, receives a
// human-readable result line (used by IPC to answer the CLI synchronously).
type controlAction struct {
	kind  controlKind
	issue int
	text  string
	reply chan string
}

// answer sends a reply line if a channel is present (non-blocking).
func (a controlAction) answer(msg string) {
	if a.reply != nil {
		select {
		case a.reply <- msg:
		default:
		}
	}
}

// submitControl posts a control action to the loop and, when reply is set,
// waits for the result. It is the single entry point shared by IPC and Telegram
// handlers so both go through the serialized loop.
func (d *Daemon) submitControl(ctx context.Context, a controlAction) string {
	d.enqueue(loopEvent{kind: evControl, control: a})
	if a.reply == nil {
		return ""
	}
	select {
	case <-ctx.Done():
		return "cancelled"
	case msg := <-a.reply:
		return msg
	}
}

// onControl executes a control action on the loop goroutine.
func (d *Daemon) onControl(ctx context.Context, a controlAction) {
	switch a.kind {
	case ctlPause:
		if d.setPaused(true) {
			d.logEvent(ctx, 0, "control", "paused (global): new dispatches held")
			d.notify(ctx, "⏸ paused: new dispatches held; running work continues")
		}
		a.answer("paused")
	case ctlResume:
		if d.setPaused(false) {
			d.logEvent(ctx, 0, "control", "resumed (global)")
			d.notify(ctx, "▶ resumed")
		}
		a.answer("resumed")
		d.reconcile(ctx)
	case ctlStop:
		a.answer(d.stopIssue(ctx, a.issue))
	case ctlSteer:
		a.answer(d.steer(ctx, a.issue, a.text))
	}
}

// stopIssue cancels the runner for exactly the targeted issue, reverts its
// label to clex:approved, and PRESERVES its worktree (spec: per-issue stop). It
// does not touch any other running issue. The build goroutine observes the
// cancelled context, and onBuildDone sees stopped=true and leaves the worktree.
func (d *Daemon) stopIssue(ctx context.Context, issue int) string {
	d.mu.Lock()
	rs, ok := d.running[issue]
	d.mu.Unlock()
	if !ok {
		return fmt.Sprintf("#%d is not running", issue)
	}
	// Revert the label first so the source of truth reflects the stop even if
	// the process dies immediately after. SetState is idempotent.
	if err := d.deps.GH.SetState(ctx, d.cfg.Repo, issue, core.StateApproved); err != nil {
		d.log.Warn("stop: revert label", "issue", issue, "err", d.red.Redact(err.Error()))
	}
	// Cancel the runner's context. The worktree is intentionally left in place.
	rs.cancel()
	d.logEvent(ctx, issue, "control", "stopped: runner cancelled, label reverted, worktree preserved")
	d.notify(ctx, fmt.Sprintf("⏹ #%d stopped; worktree preserved", issue))
	return fmt.Sprintf("stopped #%d", issue)
}

// steer delivers steering text toward an issue. Three cases (spec: Steering):
//   - active runner  → inject as the next turn of the resumed session;
//   - idle issue     → append a *Steering* note to the issue body and re-lint;
//   - epic (issue 0 or an epic issue) → update the PRD body and propagate.
func (d *Daemon) steer(ctx context.Context, issue int, text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return "empty steer ignored"
	}

	// Active runner: stash the steer so the next turn injects it. The running
	// build goroutine picks it up via the runState; if the current turn is
	// mid-flight, we mark it for the resumed session.
	d.mu.Lock()
	rs, running := d.running[issue]
	if running {
		rs.steer = text
	}
	d.mu.Unlock()
	if running {
		d.logEvent(ctx, issue, "steer", "queued for active runner as next resumed turn")
		d.notify(ctx, fmt.Sprintf("↳ steer queued for #%d (active runner)", issue))
		// Inject immediately as a resumed turn using the runner's session id.
		d.injectSteer(ctx, rs, text)
		return fmt.Sprintf("steer delivered to active runner #%d", issue)
	}

	// Epic steer: issue 0 means "the epic"; resolve to the epic issue and update
	// its PRD body, then propagate to unstarted children.
	iss, err := d.deps.GH.GetIssue(ctx, d.cfg.Repo, issue)
	if err != nil {
		if issue == 0 {
			return "no active runner and no epic specified"
		}
		return fmt.Sprintf("#%d not found: %v", issue, err)
	}
	if iss.IsEpic {
		return d.steerEpic(ctx, iss, text)
	}

	// Idle issue: append a Steering note to the body and re-lint (best-effort).
	return d.steerIdleIssue(ctx, iss, text)
}

// injectSteer runs one resumed turn against the active runner, feeding the
// steering text as the prompt and resuming the CLI session so context is
// preserved (spec: Resume, don't restart). Errors are logged, not fatal.
func (d *Daemon) injectSteer(ctx context.Context, rs *runState, text string) {
	runner, err := d.deps.RunnerFactory.RunnerFor(rs.model)
	if err != nil {
		d.log.Warn("steer: runner for model", "model", rs.model.ID, "err", err.Error())
		return
	}
	task := core.Task{
		Repo:     d.cfg.Repo.String(),
		Issue:    rs.issue,
		Prompt:   "Steering update from the owner: " + text,
		ResumeID: rs.sessionID,
	}
	dir := d.worktreeDir(rs.issue)
	go func() {
		ch, err := runner.Run(ctx, task, dir)
		if err != nil {
			d.log.Warn("steer: run", "issue", rs.issue, "err", d.red.Redact(err.Error()))
			return
		}
		// Drain events; the resumed turn's output is folded into the ongoing
		// build. We only capture the terminal session id for subsequent resumes.
		for ev := range ch {
			if ev.Type == core.EventResult && ev.SessionID != "" {
				d.mu.Lock()
				if cur := d.running[rs.issue]; cur != nil {
					cur.sessionID = ev.SessionID
				}
				d.mu.Unlock()
			}
		}
	}()
}

// steerEpic updates the epic PRD body with the steer and propagates it to
// unstarted children, flagging any children that have already landed and now
// contradict the change (spec: epic steer). Landed-contradiction detection is
// coarse: any child that has left the open set is flagged for the human.
func (d *Daemon) steerEpic(ctx context.Context, epic *gh.Issue, text string) string {
	newBody := epic.Body + "\n\n## Steering\n" + text
	if _, err := d.deps.GH.UpdateIssue(ctx, d.cfg.Repo, epic.Number, nil, &newBody); err != nil {
		return fmt.Sprintf("epic #%d update failed: %v", epic.Number, err)
	}
	d.logEvent(ctx, epic.Number, "steer", "epic PRD updated; propagating to unstarted children")

	issues, err := d.deps.GH.ListIssues(ctx, d.cfg.Repo)
	if err == nil {
		var propagated, flagged int
		for _, child := range issues {
			if child.IsEpic || !dependsOn(child, epic.Number) {
				continue
			}
			switch child.State {
			case core.StateApproved, core.StatePlanned, core.StateIdea, core.StateResearching:
				// Unstarted: append the steer as guidance.
				cb := child.Body + "\n\n## Steering (from epic)\n" + text
				if _, err := d.deps.GH.UpdateIssue(ctx, d.cfg.Repo, child.Number, nil, &cb); err == nil {
					propagated++
				}
			case core.StateBuilding, core.StateReview:
				// Already in flight/landed: flag potential contradiction.
				_ = d.deps.GH.Comment(ctx, d.cfg.Repo, child.Number, "Epic steering changed after this issue started; verify it does not contradict: "+text)
				flagged++
			}
		}
		d.logEvent(ctx, epic.Number, "steer", fmt.Sprintf("propagated to %d unstarted, flagged %d in-flight", propagated, flagged))
	}
	d.notify(ctx, fmt.Sprintf("↳ epic #%d steered; propagated to unstarted children", epic.Number))
	return fmt.Sprintf("epic #%d steered", epic.Number)
}

// steerIdleIssue appends a Steering note to an idle issue body and requests a
// re-lint by moving it through its normal gate. Here we append the note and log;
// re-lint is the pipeline's responsibility on the next dispatch.
func (d *Daemon) steerIdleIssue(ctx context.Context, iss *gh.Issue, text string) string {
	newBody := iss.Body + "\n\n## Steering\n" + text
	if _, err := d.deps.GH.UpdateIssue(ctx, d.cfg.Repo, iss.Number, nil, &newBody); err != nil {
		return fmt.Sprintf("#%d update failed: %v", iss.Number, err)
	}
	d.logEvent(ctx, iss.Number, "steer", "idle issue body appended with Steering note; will re-lint on next dispatch")
	d.notify(ctx, fmt.Sprintf("↳ #%d steered (idle); body updated", iss.Number))
	return fmt.Sprintf("steered idle #%d", iss.Number)
}

// worktreeDir returns the worktree path for an issue (best-effort; used to run a
// resumed steer turn in the right directory).
func (d *Daemon) worktreeDir(issue int) string {
	// The workspace manager owns the layout; reconstruct via its convention.
	return workspaceManager(d.cfg.Home, d.log).WorktreePath(d.cfg.Repo.String(), issue, "")
}
