package daemon

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/reissui/clex/internal/core"
	"github.com/reissui/clex/internal/gh"
	"github.com/reissui/clex/internal/pipeline"
	"github.com/reissui/clex/internal/registry"
	"github.com/reissui/clex/internal/scheduler"
	"github.com/reissui/clex/internal/store"
)

// dispatchBuild launches a build for one scheduler decision, subject to the
// cost gate. It runs on the loop goroutine; the actual build executes in its own
// goroutine and reports back via evBuildDone. Idempotency: an issue already in
// the running set is skipped (a duplicate poller event or overlapping tick must
// not double-dispatch).
func (d *Daemon) dispatchBuild(ctx context.Context, disp scheduler.Dispatch, issues []*gh.Issue) {
	d.mu.Lock()
	_, already := d.running[disp.Issue]
	d.mu.Unlock()
	if already {
		return
	}

	iss := findIssue(issues, disp.Issue)
	if iss == nil {
		d.log.Warn("dispatch: issue vanished", "issue", disp.Issue)
		return
	}

	// Route: ask the registry for the build model.
	decision := d.deps.Registry.Build(iss.Meta.Difficulty, registry.BuildOptions{})
	if !decision.Ok {
		d.notify(ctx, fmt.Sprintf("#%d: no build model available; leaving approved", disp.Issue))
		d.logEvent(ctx, disp.Issue, "route", "no build model available")
		return
	}
	model := decision.Winner.Option.Model

	// Cost gate BEFORE dispatch (spec: Confirm → Ask; Block → pause epic).
	if !d.passCostGate(ctx, disp.Issue, model, "build") {
		return
	}

	d.startBuild(ctx, iss, disp, model, "", 0)
}

// startBuild transitions the issue to clex:building, records a session, and
// spawns the build goroutine. carryDiff (non-empty on escalation) and failures
// seed the runState so an escalation re-dispatch carries prior context forward.
func (d *Daemon) startBuild(ctx context.Context, iss *gh.Issue, disp scheduler.Dispatch, model core.Model, carryDiff string, failures int) {
	epicNum := epicOf(iss)

	// Move to building (idempotent label swap). If the transition is invalid
	// (e.g. issue already moved), log and bail rather than force it.
	if err := d.deps.GH.SetState(ctx, d.cfg.Repo, iss.Number, core.StateBuilding); err != nil {
		d.log.Warn("set building", "issue", iss.Number, "err", d.red.Redact(err.Error()))
		return
	}

	sessionID, _ := d.deps.Store.CreateSession(store.Session{
		Issue:     iss.Number,
		Repo:      d.cfg.Repo.String(),
		Model:     model.ID,
		State:     store.SessionRunning,
		StartedAt: time.Now(),
	})

	buildCtx, cancel := context.WithCancel(ctx)
	rs := &runState{
		issue:    iss.Number,
		provider: model.Provider,
		model:    model,
		stage:    "build",
		cancel:   cancel,
		failures: failures,
		lastDiff: carryDiff,
	}
	d.mu.Lock()
	d.running[iss.Number] = rs
	d.mu.Unlock()

	d.logEvent(ctx, iss.Number, "dispatch", fmt.Sprintf("build #%d with %s (%s)", iss.Number, model.ID, disp.Reason))
	d.notify(ctx, fmt.Sprintf("▶ #%d building with %s", iss.Number, model.ID))

	go d.runBuild(buildCtx, sessionID, epicNum, iss, model, carryDiff)
}

// runBuild executes the pipeline Build stage in a goroutine and reports the
// outcome back to the loop. It never mutates daemon state directly (that is the
// loop's job); it only sends an evBuildDone.
func (d *Daemon) runBuild(ctx context.Context, sessionID int64, epicNum int, iss *gh.Issue, model core.Model, carryDiff string) {
	k := d.knowledgeFor(iss, carryDiff)
	res, err := d.deps.Stages.Build(ctx, epicNum, iss, k, 0)

	done := buildDone{issue: iss.Number, result: res, err: err, diff: res.SessionID}
	// A cancelled context means /stop or shutdown; flag it so the loop preserves
	// the worktree and does not treat it as a verification failure.
	if ctx.Err() != nil {
		done.stopped = true
	}
	// Record session end.
	endState := store.SessionDone
	if err != nil {
		endState = store.SessionStopped
	}
	if sessionID != 0 {
		_ = d.deps.Store.FinishSession(sessionID, endState, time.Now())
		if res.SessionID != "" {
			_ = d.deps.Store.SetSessionCLIID(sessionID, res.SessionID)
		}
	}
	d.enqueue(loopEvent{kind: evBuildDone, done: done})
}

// onBuildDone handles a completed build on the loop goroutine: success advances
// to review; a stop preserves state; a verification/runner failure reverts and,
// after the retry cap, escalates exactly once carrying the failed diff forward.
func (d *Daemon) onBuildDone(ctx context.Context, done buildDone) {
	d.mu.Lock()
	rs := d.running[done.issue]
	delete(d.running, done.issue)
	d.mu.Unlock()

	if done.stopped {
		// /stop or shutdown cancelled the runner. The stop handler already
		// reverted the label and preserved the worktree; nothing to do here but
		// log. (On shutdown the label revert happens in shutdown().)
		d.logEvent(ctx, done.issue, "build", "cancelled (stop/shutdown); worktree preserved")
		return
	}

	if done.err == nil {
		d.onBuildSuccess(ctx, done)
		return
	}

	// Failure path. Distinguish verification failure (escalatable) from other
	// runner errors. Both revert to approved via the pipeline already; we drive
	// retry/escalation here.
	failures := 1
	if rs != nil {
		failures = rs.failures + 1
	}
	verFail := errors.Is(done.err, pipeline.ErrVerificationFailed)
	d.logEvent(ctx, done.issue, "build", fmt.Sprintf("failed (attempt %d): %s", failures, done.err.Error()))

	if failures <= maxAutoRetries {
		// Automatic retry without escalation: re-dispatch same model, carrying
		// the diff forward so the runner resumes rather than restarts.
		d.notify(ctx, fmt.Sprintf("↻ #%d retry %d/%d", done.issue, failures, maxAutoRetries))
		d.redispatch(ctx, done.issue, modelOrZero(rs), done.diff, failures)
		return
	}

	// Retry budget exhausted. Escalate exactly once if this was a verification
	// failure and a stronger model exists.
	if verFail {
		if next, ok := d.deps.Stages.EscalateModel(modelOrZero(rs)); ok {
			d.logEvent(ctx, done.issue, "escalate", fmt.Sprintf("%s → %s after %d verification failures", modelIDOrDash(rs), next.ID, failures))
			d.notify(ctx, fmt.Sprintf("⤴ #%d escalating to %s (carrying prior diff)", done.issue, next.ID))
			if d.passCostGate(ctx, done.issue, next, "build") {
				d.startEscalatedBuild(ctx, done.issue, next, done.diff, failures)
			}
			return
		}
	}

	// No escalation available: leave failed for a human decision.
	d.notify(ctx, fmt.Sprintf("✗ #%d failed; needs a decision (retry/reassign/skip)", done.issue))
}

// onBuildSuccess advances a green build into the review stage, then, if the
// review approves and merges, checks whether the epic is ready to assemble.
func (d *Daemon) onBuildSuccess(ctx context.Context, done buildDone) {
	iss, err := d.deps.GH.GetIssue(ctx, d.cfg.Repo, done.issue)
	if err != nil {
		d.log.Warn("get issue post-build", "issue", done.issue, "err", d.red.Redact(err.Error()))
		return
	}
	epicNum := epicOf(iss)
	green := done.result.Verification.Command == "" || true // build succeeded ⇒ verification green
	d.logEvent(ctx, done.issue, "build", fmt.Sprintf("succeeded; PR #%d; moving to review", done.result.PRNumber))

	rev, err := d.deps.Stages.Review(ctx, epicNum, iss, done.result.PRNumber, done.result.Model, done.diff, green)
	if err != nil {
		d.notify(ctx, fmt.Sprintf("#%d review error: %s", done.issue, err.Error()))
		return
	}
	if rev.Outcome == pipeline.ReviewApproved && rev.Merged {
		d.notify(ctx, fmt.Sprintf("✓ #%d merged into epic (%s)", done.issue, shortSHA(rev.MergeSHA)))
		d.maybeAssemble(ctx, epicNum)
	} else {
		d.notify(ctx, fmt.Sprintf("#%d review: %s", done.issue, rev.Outcome))
	}
}

// redispatch re-runs a build for an issue after an automatic retry, reusing the
// same model and carrying the prior diff forward.
func (d *Daemon) redispatch(ctx context.Context, issue int, model core.Model, diff string, failures int) {
	iss, err := d.deps.GH.GetIssue(ctx, d.cfg.Repo, issue)
	if err != nil {
		d.log.Warn("get issue for retry", "issue", issue, "err", d.red.Redact(err.Error()))
		return
	}
	if model.ID == "" {
		decision := d.deps.Registry.Build(iss.Meta.Difficulty, registry.BuildOptions{})
		if !decision.Ok {
			return
		}
		model = decision.Winner.Option.Model
	}
	d.startBuild(ctx, iss, scheduler.Dispatch{Issue: issue, Provider: model.Provider, Reason: "retry"}, model, diff, failures)
}

// startEscalatedBuild re-dispatches with a stronger model, carrying the failed
// diff forward (spec: "re-dispatch carrying failed diff + notes forward").
func (d *Daemon) startEscalatedBuild(ctx context.Context, issue int, model core.Model, diff string, failures int) {
	iss, err := d.deps.GH.GetIssue(ctx, d.cfg.Repo, issue)
	if err != nil {
		d.log.Warn("get issue for escalation", "issue", issue, "err", d.red.Redact(err.Error()))
		return
	}
	d.startBuild(ctx, iss, scheduler.Dispatch{Issue: issue, Provider: model.Provider, Reason: "escalation"}, model, diff, failures)
}

// maybeAssemble runs the assemble stage when every child of the epic has landed.
func (d *Daemon) maybeAssemble(ctx context.Context, epicNum int) {
	if epicNum == 0 {
		return
	}
	issues, err := d.deps.GH.ListIssues(ctx, d.cfg.Repo)
	if err != nil {
		return
	}
	children, allLanded := d.epicChildren(issues, epicNum)
	if !allLanded {
		return
	}
	epicIss, err := d.deps.GH.GetIssue(ctx, d.cfg.Repo, epicNum)
	if err != nil {
		return
	}
	in := pipeline.AssembleInput{
		EpicTitle: epicIss.Title,
		Children:  children,
		Summary:   "clex epic assembly",
		AutoMerge: d.cfg.AutoMergeFinalPR,
	}
	res, err := d.deps.Stages.Assemble(ctx, epicNum, allLanded, in, d.epicVerify(), 0)
	if err != nil {
		if errors.Is(err, pipeline.ErrNotReady) {
			return
		}
		d.notify(ctx, fmt.Sprintf("epic #%d assemble error: %s", epicNum, err.Error()))
		return
	}
	d.notify(ctx, fmt.Sprintf("🏁 epic #%d final PR #%d opened", epicNum, res.PRNumber))
	d.logEvent(ctx, epicNum, "assemble", fmt.Sprintf("final PR #%d (merged=%v)", res.PRNumber, res.Merged))
}

func (d *Daemon) epicVerify() string {
	if d.cfg.EpicVerify != "" {
		return d.cfg.EpicVerify
	}
	return d.cfg.DefaultVerify
}

// --- small helpers ---

func findIssue(issues []*gh.Issue, number int) *gh.Issue {
	for _, iss := range issues {
		if iss.Number == number {
			return iss
		}
	}
	return nil
}

func modelOrZero(rs *runState) core.Model {
	if rs == nil {
		return core.Model{}
	}
	return rs.model
}

func modelIDOrDash(rs *runState) string {
	if rs == nil || rs.model.ID == "" {
		return "-"
	}
	return rs.model.ID
}

func shortSHA(s string) string {
	if len(s) > 7 {
		return s[:7]
	}
	return s
}
