package daemon

import (
	"context"
	"fmt"
	"time"

	"github.com/reissui/clex/internal/gh"
	"github.com/reissui/clex/internal/pipeline"
	"github.com/reissui/clex/internal/scheduler"
	"github.com/reissui/clex/internal/store"
)

// loopKind classifies an internal loop event.
type loopKind int

const (
	// evChange is a GitHub poller change (label swap, issue/PR transition).
	evChange loopKind = iota
	// evTick is a periodic reconcile tick: recompute scheduler state and
	// dispatch any newly eligible work even absent a poller event.
	evTick
	// evBuildDone is a runner-completion event from a build goroutine.
	evBuildDone
	// evControl is an IPC/Telegram control action (pause/resume/stop/steer).
	evControl
)

// loopEvent is the single serialized input to the loop. Only the fields
// relevant to Kind are set.
type loopEvent struct {
	kind    loopKind
	change  gh.Change
	done    buildDone
	control controlAction
}

// buildDone reports the outcome of a build goroutine back to the loop.
type buildDone struct {
	issue  int
	result pipeline.BuildResult
	err    error
	// sessionID is the runner CLI session id from the build. It is the
	// carry-forward handle for retry/escalation: the re-dispatched runner RESUMES
	// this session (spec: "Resume, don't restart"), so it re-enters with all the
	// prior work — including the diff it already produced — rather than a literal
	// diff blob (BuildResult exposes no diff). Review fetches its own diff.
	sessionID string
	stopped   bool // true when the build was cancelled by /stop or shutdown
}

// Run drives the daemon until ctx is cancelled. It performs crash recovery,
// starts the poller and a reconcile ticker, registers control handlers, and
// processes events serially. It returns nil on clean shutdown (ctx cancelled),
// after cancelling any in-flight runners and flushing the store.
func (d *Daemon) Run(ctx context.Context) error {
	d.startedAt = time.Now()
	d.log.Info("clexd starting", "repo", d.cfg.Repo.String(), "home", d.cfg.Home)

	// Register Telegram command handlers (also reachable via IPC).
	d.registerCommands(ctx)

	// Crash recovery: reconstruct in-flight state purely from GitHub labels
	// BEFORE accepting any new work. Orphaned clex:building issues with no live
	// session revert to clex:approved with a comment.
	if err := d.recover(ctx); err != nil {
		// Recovery failure is logged but non-fatal: the poller/tick will retry
		// reconciliation. A hard failure here should not prevent the daemon from
		// coming up and being controllable.
		d.log.Error("crash recovery failed", "err", d.red.Redact(err.Error()))
	}

	// Start the GitHub poller. Its channel feeds evChange events.
	trusted := &gh.TrustedActors{Owner: d.cfg.Owner, Self: d.cfg.SelfLogin}
	changes := d.deps.GH.Poll(ctx, []gh.Repo{d.cfg.Repo}, d.pollInterval(), gh.PollOptions{Trusted: trusted})
	go d.pumpChanges(ctx, changes)

	// Reconcile ticker: even without poller events, periodically recompute and
	// dispatch (covers newly-eligible work whose dependencies just closed).
	ticker := time.NewTicker(d.pollInterval())
	defer ticker.Stop()
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				d.enqueue(loopEvent{kind: evTick})
			}
		}
	}()

	// Initial reconcile so eligible work dispatches immediately at startup.
	d.enqueue(loopEvent{kind: evTick})

	for {
		select {
		case <-ctx.Done():
			d.shutdown()
			return nil
		case ev := <-d.events:
			d.handleEvent(ctx, ev)
		}
	}
}

// enqueue posts an event to the loop. The buffered channel makes this
// non-blocking in the common case; under a burst that fills the buffer, the send
// is completed by a short-lived goroutine that also watches the stopped channel,
// so a parked event is abandoned on shutdown instead of leaking a goroutine
// forever (the loop stops reading d.events once it returns).
func (d *Daemon) enqueue(ev loopEvent) {
	select {
	case d.events <- ev:
	case <-d.stopped:
	default:
		go func() {
			select {
			case d.events <- ev:
			case <-d.stopped:
			}
		}()
	}
}

// pumpChanges forwards poller changes onto the loop channel until the poller
// closes (ctx cancelled).
func (d *Daemon) pumpChanges(ctx context.Context, changes <-chan gh.Change) {
	for {
		select {
		case <-ctx.Done():
			return
		case ch, ok := <-changes:
			if !ok {
				return
			}
			d.enqueue(loopEvent{kind: evChange, change: ch})
		}
	}
}

// handleEvent processes one loop event on the single loop goroutine.
func (d *Daemon) handleEvent(ctx context.Context, ev loopEvent) {
	switch ev.kind {
	case evChange:
		d.onChange(ctx, ev.change)
		d.reconcile(ctx)
	case evTick:
		d.reconcile(ctx)
	case evBuildDone:
		d.onBuildDone(ctx, ev.done)
		d.reconcile(ctx)
	case evControl:
		d.onControl(ctx, ev.control)
	}
}

// onChange reacts to a single poller change. Most changes only matter insofar
// as they alter scheduler state (handled by the following reconcile), but a
// newly-opened idea issue and a merged PR warrant a direct log line.
func (d *Daemon) onChange(ctx context.Context, ch gh.Change) {
	d.logEvent(ctx, ch.Issue, "github", fmt.Sprintf("%s by %s", ch.Kind, ch.Actor))
}

// reconcile recomputes scheduler state from GitHub labels and dispatches any
// newly-eligible issues, subject to the global pause flag and caps. It is the
// single place new builds are launched.
func (d *Daemon) reconcile(ctx context.Context) {
	if d.isPaused() {
		return
	}
	issues, err := d.deps.GH.ListIssues(ctx, d.cfg.Repo)
	if err != nil {
		d.log.Warn("list issues for reconcile", "err", d.red.Redact(err.Error()))
		return
	}
	state := d.schedulerState(issues)
	if err := scheduler.Validate(state.Issues); err != nil {
		// A dependency cycle: surface once, do not dispatch (Next returns nil).
		d.log.Warn("dependency cycle; holding dispatch", "err", err.Error())
		return
	}
	for _, disp := range scheduler.Next(state) {
		d.dispatchBuild(ctx, disp, issues)
	}
}

// schedulerState builds scheduler input from GitHub issues plus the daemon's
// live running set. Provider is resolved lazily at dispatch time, so issues
// carry their known provider (from a prior clex:agent tag or empty).
func (d *Daemon) schedulerState(issues []*gh.Issue) scheduler.SchedulerState {
	var sIssues []scheduler.Issue
	for _, iss := range issues {
		if iss.IsEpic {
			continue // epics are not dispatchable units
		}
		sIssues = append(sIssues, scheduler.Issue{
			Number:     iss.Number,
			State:      iss.State,
			DependsOn:  iss.Meta.DependsOn,
			Touches:    iss.Meta.Touches,
			Closed:     false,
			Difficulty: iss.Meta.Difficulty,
		})
	}
	// A dependency that is not in the open list has merged/closed and is
	// therefore satisfied. The scheduler decides eligibility by looking up each
	// DependsOn number's own Issue entry and checking Closed, so synthesize a
	// Closed placeholder for every referenced dependency that is absent from the
	// open set. Without this, a real merged dependency would look "not closed"
	// and wrongly block its dependents.
	open := make(map[int]bool, len(issues))
	for _, iss := range issues {
		open[iss.Number] = true
	}
	seen := make(map[int]bool, len(sIssues))
	for _, si := range sIssues {
		seen[si.Number] = true
	}
	for _, si := range sIssues {
		for _, dep := range si.DependsOn {
			if !seen[dep] && !open[dep] {
				sIssues = append(sIssues, scheduler.Issue{Number: dep, Closed: true})
				seen[dep] = true
			}
		}
	}

	d.mu.Lock()
	var running []scheduler.Running
	for _, rs := range d.running {
		running = append(running, scheduler.Running{
			Issue:    rs.issue,
			Provider: rs.provider,
			Touches:  touchesFor(issues, rs.issue),
		})
	}
	d.mu.Unlock()

	return scheduler.SchedulerState{
		Issues:  sIssues,
		Running: running,
		Caps:    d.caps(),
	}
}

// touchesFor returns the Touches globs for an issue number from the issue list.
func touchesFor(issues []*gh.Issue, number int) []string {
	for _, iss := range issues {
		if iss.Number == number {
			return iss.Meta.Touches
		}
	}
	return nil
}

// logEvent appends a redacted line to the runtime event log and mirrors it to
// structured logging. Every detail is passed through the redactor so no secret
// reaches the append-only log (spec: event log redacts known secret patterns).
func (d *Daemon) logEvent(_ context.Context, issue int, kind, detail string) {
	safe := d.red.Redact(detail)
	if _, err := d.deps.Store.AppendEvent(store.LogEntry{
		TS:     time.Now(),
		Issue:  issue,
		Kind:   kind,
		Detail: safe,
	}); err != nil {
		d.log.Warn("append event", "err", err.Error())
	}
	d.log.Info("event", "issue", issue, "kind", kind, "detail", safe)
}

// notify sends a one-line status message to Telegram, redacting first. Failures
// are logged, not fatal — Telegram being down must not stall the pipeline.
func (d *Daemon) notify(ctx context.Context, text string) {
	if _, err := d.deps.TG.SendLine(ctx, d.red.Redact(text)); err != nil {
		d.log.Warn("telegram send", "err", d.red.Redact(err.Error()))
	}
}
