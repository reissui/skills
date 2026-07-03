package daemon

import (
	"context"
	"fmt"
	"time"

	"github.com/reissui/clex/internal/core"
	"github.com/reissui/clex/internal/registry"
	"github.com/reissui/clex/internal/telegram"
)

// passCostGate adjudicates the cost gate for a dispatch and returns whether it
// may proceed (spec: Cost-gate wiring):
//   - GateProceed → dispatch silently (the estimate is logged).
//   - GateConfirm → ask the owner via Telegram before dispatch; proceed only on
//     an affirmative (non-skip) answer.
//   - GateBlock   → refuse and pause the epic (global pause).
//
// The estimate is derived by the registry from model billing, stage, and issue
// size; subscription/free models always proceed.
func (d *Daemon) passCostGate(ctx context.Context, issue int, model core.Model, stage string) bool {
	decision, estimate := d.deps.Registry.GateModel(model, stage, d.issueSize(issue), d.epicStart())
	switch decision {
	case registry.GateProceed:
		if estimate > 0 {
			d.logEvent(ctx, issue, "cost", fmt.Sprintf("proceed: est $%.2f for %s on %s", estimate, model.ID, stage))
		}
		return true

	case registry.GateConfirm:
		d.setPendingGate(fmt.Sprintf("#%d %s ~$%.2f", issue, model.ID, estimate))
		defer d.setPendingGate("")
		q := telegram.Question{
			Prompt:   fmt.Sprintf("#%d: %s on %s ≈ $%.2f. Proceed?", issue, model.ID, stage, estimate),
			Proposal: "proceed",
		}
		ans, err := d.deps.TG.Ask(ctx, q)
		if err != nil {
			d.log.Warn("cost confirm ask", "issue", issue, "err", d.red.Redact(err.Error()))
			// Fail closed: if we cannot ask, do not spend.
			d.logEvent(ctx, issue, "cost", "confirm unavailable; holding dispatch")
			return false
		}
		if ans.Skipped {
			d.logEvent(ctx, issue, "cost", "owner skipped; dispatch held")
			d.notify(ctx, fmt.Sprintf("#%d held: cost confirm skipped", issue))
			return false
		}
		d.logEvent(ctx, issue, "cost", fmt.Sprintf("owner confirmed est $%.2f", estimate))
		return true

	case registry.GateBlock:
		d.logEvent(ctx, issue, "cost", fmt.Sprintf("BLOCK: est $%.2f exceeds epic cap; pausing epic", estimate))
		d.notify(ctx, fmt.Sprintf("⛔ #%d blocked: epic cost cap reached; pausing", issue))
		// Block pauses the epic globally (spec: Block pauses the epic).
		d.setPaused(true)
		return false

	default:
		return true
	}
}

// issueSize maps an issue's difficulty to the registry's coarse cost-scaling
// size. Absent per-stage history, this seeds the cold-start estimate.
func (d *Daemon) issueSize(issue int) registry.IssueSize {
	iss, err := d.deps.GH.GetIssue(context.Background(), d.cfg.Repo, issue)
	if err != nil {
		return registry.IssueSizeMedium
	}
	switch iss.Meta.Difficulty {
	case core.DifficultyTrivial:
		return registry.IssueSizeSmall
	case core.DifficultyComplex:
		return registry.IssueSizeLarge
	default:
		return registry.IssueSizeMedium
	}
}

// epicStart returns the reference time for epic spend accounting. Without a
// tracked epic-start timestamp the daemon uses its own start time, which bounds
// "spend since epic began" to the current daemon lifetime — a safe lower bound
// for the gate.
func (d *Daemon) epicStart() time.Time {
	if d.startedAt.IsZero() {
		return time.Now()
	}
	return d.startedAt
}

// setPendingGate records (or clears) the human-readable pending-gate string,
// consulted by the update quiesce hook and status snapshots.
func (d *Daemon) setPendingGate(s string) {
	d.mu.Lock()
	d.pendingGate = s
	d.mu.Unlock()
}

// gatePending reports whether a cost-gate confirmation is currently outstanding.
func (d *Daemon) gatePending() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.pendingGate != ""
}
