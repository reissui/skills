package registry

import (
	"time"

	"github.com/reissui/clex/internal/config"
	"github.com/reissui/clex/internal/core"
)

// GateDecision is a cost gate's verdict for a single dispatch to a model.
type GateDecision string

const (
	// GateProceed dispatches silently (logging the estimate): the model is
	// subscription/free, or a metered estimate is below the confirm threshold.
	GateProceed GateDecision = "proceed"
	// GateConfirm holds the stage and asks the owner once via Telegram before
	// spending: a metered estimate above confirm_over_usd (spec: Cost gates —
	// "#42 plan on fable-5 · est. $6.20 — [proceed] [swap] [hold]").
	GateConfirm GateDecision = "confirm"
	// GateBlock refuses the dispatch: the epic's max_usd_per_epic cap is already
	// reached, so no further metered spend is allowed until the human intervenes.
	GateBlock GateDecision = "block"
)

// IssueSize is the planner's coarse sizing of an issue, used to scale the
// cold-start cost estimate before real per-stage history exists.
type IssueSize string

const (
	IssueSizeSmall  IssueSize = "small"
	IssueSizeMedium IssueSize = "medium"
	IssueSizeLarge  IssueSize = "large"
)

// coldStartStageUSD is the assumed cost of one metered stage, per issue size,
// when the store has no per-stage history for the model yet. These are the
// documented cold-start defaults; they are deliberately conservative (biased
// high) so the very first metered dispatch is more likely to hit the confirm
// gate than to silently overspend. Real history supersedes them as soon as it
// exists (spec: "Estimates are heuristic and improve as SQLite accumulates").
var coldStartStageUSD = map[IssueSize]float64{
	IssueSizeSmall:  0.50,
	IssueSizeMedium: 2.00,
	IssueSizeLarge:  6.00,
}

// defaultColdStartUSD is used when the issue size is unknown/empty.
const defaultColdStartUSD = 2.00

// EstimateCost predicts the USD cost of running one stage on a model for an issue
// of the given size. Subscription and free models always cost $0 (they consume
// windows, not money). For a metered model it returns the documented cold-start
// default scaled by issue size.
//
// stage is the routing role / stage key (e.g. "build", "plan"); issueSize is the
// planner's coarse sizing. The estimate is heuristic by design and is always
// recorded against the actual so drift is visible in /costs. It refines as the
// store accumulates real per-stage history; the History interface currently
// exposes only trailing total spend (SpendSince), not a per-stage average, so a
// true history-weighted estimate awaits that store method — until then the
// conservative, size-scaled default is used so the first metered dispatch errs
// toward the confirm gate rather than silent overspend.
func (r *Registry) EstimateCost(model core.Model, stage string, issueSize IssueSize) float64 {
	if model.Billing != core.BillingMetered {
		return 0 // subscription / free: no marginal money
	}
	if v, ok := coldStartStageUSD[issueSize]; ok {
		return v
	}
	return defaultColdStartUSD
}

// Gate decides whether a dispatch may proceed given its estimated cost, the
// model's billing mode, epic-to-date spend, and the configured budget. It
// implements the spec's cost-gate policy:
//
//   - subscription / free  → always GateProceed (they bypass cost gates).
//   - metered, epic cap reached (spentThisEpic+estimate ≥ max_usd_per_epic > 0)
//     → GateBlock (the epic pauses).
//   - metered, estimate > confirm_over_usd (> 0) → GateConfirm.
//   - metered, otherwise → GateProceed.
//
// A zero threshold disables that gate (spec: "Zero values disable the respective
// gate"). spentThisEpic is the epic's metered spend so far, normally
// hist.SpendSince(epicStart, "").
func Gate(billing core.BillingMode, estimate, spentThisEpic float64, budget config.Budget) GateDecision {
	if billing != core.BillingMetered {
		return GateProceed // subscription / free bypass cost gates entirely
	}
	// Hard cap first: reaching the epic cap blocks further metered spend.
	if budget.MaxUSDPerEpic > 0 && spentThisEpic+estimate >= budget.MaxUSDPerEpic {
		return GateBlock
	}
	// Confirmation threshold.
	if budget.ConfirmOverUSD > 0 && estimate > budget.ConfirmOverUSD {
		return GateConfirm
	}
	return GateProceed
}

// GateModel is a convenience wrapper that estimates a metered dispatch's cost and
// gates it in one call, reading epic-to-date spend from history. It returns the
// decision and the estimate used, so the caller can show the number in a confirm
// prompt.
func (r *Registry) GateModel(model core.Model, stage string, issueSize IssueSize, epicStart time.Time) (GateDecision, float64) {
	est := r.EstimateCost(model, stage, issueSize)
	if model.Billing != core.BillingMetered {
		return GateProceed, est
	}
	spent, err := r.hist.SpendSince(epicStart, "")
	if err != nil {
		spent = 0 // treat an unreadable history as no spend; the estimate still gates
	}
	return Gate(model.Billing, est, spent, r.cfg.Budget), est
}
