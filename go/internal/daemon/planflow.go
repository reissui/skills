package daemon

import (
	"context"
	"fmt"
	"strings"

	"github.com/reissui/clex/internal/core"
	"github.com/reissui/clex/internal/gh"
	"github.com/reissui/clex/internal/pipeline"
)

// planDone reports the outcome of a plan goroutine back to the loop.
type planDone struct {
	idea    int
	result  pipeline.PlanResult
	err     error
	stopped bool // true when the plan was cancelled by /stop or shutdown
}

// dispatchPlan launches planning for one clex:idea issue. It runs on the loop
// goroutine; the planner executes in its own goroutine and reports back via
// evPlanDone. Idempotency: an issue already in the running set is skipped, and
// an epic a prior (crashed) run already created for this idea is detected via
// the PlannedFromMarker so Plan resumes it instead of duplicating.
func (d *Daemon) dispatchPlan(ctx context.Context, iss *gh.Issue, issues []*gh.Issue) {
	d.mu.Lock()
	_, already := d.running[iss.Number]
	failed := d.planFailed[iss.Number]
	d.mu.Unlock()
	if already || failed {
		return
	}

	if err := d.deps.GH.SetState(ctx, d.cfg.Repo, iss.Number, core.StateResearching); err != nil {
		d.log.Warn("set researching", "issue", iss.Number, "err", d.red.Redact(err.Error()))
		return
	}

	existingEpic := findPlannedEpic(issues, iss.Number)

	planCtx, cancel := context.WithCancel(ctx)
	rs := &runState{issue: iss.Number, stage: "plan", cancel: cancel}
	d.mu.Lock()
	d.running[iss.Number] = rs
	d.mu.Unlock()

	d.logEvent(ctx, iss.Number, "dispatch", fmt.Sprintf("plan #%d (existing epic %d)", iss.Number, existingEpic))
	d.notify(ctx, fmt.Sprintf("🧠 #%d planning: %s", iss.Number, iss.Title))

	go d.runPlan(planCtx, iss, existingEpic)
}

// runPlan executes the pipeline Plan stage in a goroutine and reports the
// outcome back to the loop. Like runBuild it never mutates daemon state
// directly; it only sends an evPlanDone.
func (d *Daemon) runPlan(ctx context.Context, iss *gh.Issue, existingEpic int) {
	res, err := d.deps.Stages.Plan(ctx, iss, pipeline.PlanInputs{Knowledge: d.knowledgeFor(iss, "")}, existingEpic)
	d.enqueue(loopEvent{kind: evPlanDone, plan: planDone{
		idea:    iss.Number,
		result:  res,
		err:     err,
		stopped: ctx.Err() != nil,
	}})
}

// onPlanDone runs on the loop goroutine when a plan goroutine finishes. Success
// moves the idea to clex:planned, links it to the epic, and posts the plan gate
// to Telegram (build starts only on /build). Failure reverts the idea to
// clex:idea with a comment so the owner can retry or edit; a stop leaves the
// revert to stopIssue's own path (label already handled below because plan has
// no worktree to preserve).
func (d *Daemon) onPlanDone(ctx context.Context, done planDone) {
	d.mu.Lock()
	delete(d.running, done.idea)
	d.mu.Unlock()

	switch {
	case done.stopped:
		if err := d.deps.GH.SetState(ctx, d.cfg.Repo, done.idea, core.StateIdea); err != nil {
			d.log.Warn("plan stop: revert label", "issue", done.idea, "err", d.red.Redact(err.Error()))
		}
		d.logEvent(ctx, done.idea, "plan", "stopped; reverted to clex:idea")
		return
	case done.err != nil:
		// Revert the label AND remember the failure in-memory: without the
		// guard, reconcile would immediately re-plan the reverted idea and a
		// permanent failure would hot-loop. /steer on the idea clears the guard
		// for a deliberate retry; so does a daemon restart.
		d.mu.Lock()
		if d.planFailed == nil {
			d.planFailed = make(map[int]bool)
		}
		d.planFailed[done.idea] = true
		d.mu.Unlock()
		if err := d.deps.GH.SetState(ctx, d.cfg.Repo, done.idea, core.StateIdea); err != nil {
			d.log.Warn("plan fail: revert label", "issue", done.idea, "err", d.red.Redact(err.Error()))
		}
		msg := d.red.Redact(done.err.Error())
		_ = d.deps.GH.Comment(ctx, d.cfg.Repo, done.idea, "clex planning failed: "+oneLineOf(msg))
		d.logEvent(ctx, done.idea, "plan", "failed: "+oneLineOf(msg))
		d.notify(ctx, fmt.Sprintf("✗ #%d planning failed: %s — /steer %d <text> to adjust and retry", done.idea, oneLineOf(msg), done.idea))
		return
	}

	if err := d.deps.GH.SetState(ctx, d.cfg.Repo, done.idea, core.StatePlanned); err != nil {
		d.log.Warn("plan done: set planned", "issue", done.idea, "err", d.red.Redact(err.Error()))
	}
	_ = d.deps.GH.Comment(ctx, d.cfg.Repo, done.idea,
		fmt.Sprintf("clex planned this idea as epic #%d.", done.result.EpicNumber))
	d.logEvent(ctx, done.idea, "plan",
		fmt.Sprintf("epic #%d with %d issues", done.result.EpicNumber, len(done.result.IssueNumbers)))
	d.notify(ctx, d.planGateSummary(ctx, done.result))
}

// planGateSummary renders the plan gate message: the epic, its children (with
// titles when they resolve), residual lint failures, the planner's open
// questions, and how to proceed. It is a multi-line message rather than a
// keyboard so the owner reviews the actual PRD on GitHub and confirms with an
// explicit /build.
func (d *Daemon) planGateSummary(ctx context.Context, res pipeline.PlanResult) string {
	var b strings.Builder
	fmt.Fprintf(&b, "📋 plan ready: epic #%d", res.EpicNumber)
	if epic, err := d.deps.GH.GetIssue(ctx, d.cfg.Repo, res.EpicNumber); err == nil && epic.Title != "" {
		fmt.Fprintf(&b, " — %s", epic.Title)
	}
	if n := len(res.IssueNumbers); n > 0 {
		fmt.Fprintf(&b, "\n%d issues:", n)
		for _, num := range res.IssueNumbers {
			if iss, err := d.deps.GH.GetIssue(ctx, d.cfg.Repo, num); err == nil {
				fmt.Fprintf(&b, "\n  #%d %s", num, iss.Title)
			} else {
				fmt.Fprintf(&b, "\n  #%d", num)
			}
		}
	}
	for _, lf := range res.LintFailures {
		fmt.Fprintf(&b, "\n⚠ #%d failed issue-lint: %s", lf.Issue, oneLineOf(lf.Detail))
	}
	for _, q := range res.Questions {
		fmt.Fprintf(&b, "\n❓ %s", strings.TrimSpace(q.Text))
		if p := strings.TrimSpace(q.Proposed); p != "" {
			fmt.Fprintf(&b, " (proposed: %s)", p)
		}
	}
	fmt.Fprintf(&b, "\nreview on GitHub, /steer %d <text> to adjust, /build %d to start", res.EpicNumber, res.EpicNumber)
	return b.String()
}

// findPlannedEpic scans open issues for an epic whose body carries the
// PlannedFromMarker for the given idea. Non-zero means a prior plan run already
// created the epic (crash between epic creation and the planned label), so Plan
// must resume it rather than create a duplicate.
func findPlannedEpic(issues []*gh.Issue, idea int) int {
	marker := pipeline.PlannedFromMarker(idea)
	for _, iss := range issues {
		if iss.IsEpic && strings.Contains(iss.Body, marker) {
			return iss.Number
		}
	}
	return 0
}

// oneLineOf collapses whitespace to a single compact line for Telegram/log use,
// capped on a rune boundary.
func oneLineOf(s string) string {
	fields := strings.Fields(s)
	out := strings.Join(fields, " ")
	const max = 200
	if len(out) > max {
		out = cutAtRune(out, max) + "…"
	}
	return out
}
