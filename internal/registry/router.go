package registry

import (
	"math"
	"sort"
	"time"

	"github.com/reissui/clex/internal/core"
)

// buildStage is the stage key under which build latency/success history is kept
// in the store; it matches the RoleBuild routing role.
const buildStage = string(core.RoleBuild)

// buildWeights are the fixed success×speed×cost weights (they sum to 1). Success
// dominates because a failed build wastes far more than a slow one; cost is the
// lightest thumb on the scale because the build pool is already limited to
// free/subscription models. Documented in the package doc.
var buildWeights = struct{ Success, Speed, Cost float64 }{
	Success: 0.60,
	Speed:   0.25,
	Cost:    0.15,
}

// difficultyPrior is the cold-start predicted success probability for a model
// with no history at a given difficulty. Harder issues get a lower prior so an
// unproven model is not blindly handed a complex build.
var difficultyPrior = map[core.Difficulty]float64{
	core.DifficultyTrivial:  0.85,
	core.DifficultyStandard: 0.60,
	core.DifficultyComplex:  0.35,
}

// speedHalfLife is the latency at which the speed term halves: a build averaging
// speedHalfLife scores 0.5, one at 2×speedHalfLife scores 0.25, and instant work
// approaches 1.0. Ten minutes is a reasonable "brisk build" reference.
const speedHalfLife = 10 * time.Minute

// costRank is the cost-preference term by billing mode: cheaper is better.
// Metered scores 0 because metered models are excluded from the build pool
// unless explicitly overridden.
var costRank = map[core.BillingMode]float64{
	core.BillingFree:         1.0,
	core.BillingSubscription: 0.6,
	core.BillingMetered:      0.0,
}

// Candidate is a scored build model: the run option plus the component scores
// that produced its total, so the decision is explainable in the status line and
// logs.
type Candidate struct {
	Option  RunOption
	Score   float64
	Success float64 // predicted success probability term (0..1)
	Speed   float64 // speed term (0..1)
	Cost    float64 // cost-rank term (0..1)
}

// BuildDecision is the router's answer for one issue: the winning model plus the
// ranked fallbacks (best-first, winner at index 0 of Ranked) and any warnings
// (e.g. the pool degraded to a single provider). Winner is nil-safe via Ok.
type BuildDecision struct {
	Winner   Candidate
	Ranked   []Candidate // full ranking, winner first
	Warnings []Warning
	Ok       bool // false when no build-eligible model is available at all
}

// BuildOptions tune a single Build call.
type BuildOptions struct {
	// AllowMetered admits metered models into the build pool. Off by default:
	// build work never routes to a metered model without an explicit human
	// override (spec: "Build work routes to top-tier only on explicit human
	// override"; metered is likewise opt-in).
	AllowMetered bool
	// EffortOverride / FastOverride, when set, override the resolved run options
	// (mirrors a per-role override applied to the ad-hoc build pool).
	EffortOverride string
	FastOverride   bool
}

// Build selects the best model to execute an issue of the given difficulty,
// scoring the build-eligible pool (local + mid tiers) by predicted success,
// observed speed, and cost rank. It returns the winner plus the full ranked
// fallback list. The result is deterministic given fixed history and config; the
// formula and its constants are documented in the package doc.
//
// Metered models are excluded unless opts.AllowMetered is set, so the router
// never spends money on a build without an explicit override. If the whole pool
// is unhealthy the decision falls through to any healthy build-eligible model
// with a warning; only a total absence of build models yields Ok=false.
func (r *Registry) Build(difficulty core.Difficulty, opts BuildOptions) BuildDecision {
	pool := r.buildPool(opts.AllowMetered)

	var warns []Warning
	healthy := make([]core.Model, 0, len(pool))
	for _, m := range pool {
		if r.modelHealthy(m) {
			healthy = append(healthy, m)
		}
	}
	if len(healthy) == 0 && len(pool) > 0 {
		// Everything in the build pool is currently unhealthy. The scheduler
		// still needs a builder; surface a warning and score the raw pool so the
		// least-bad option is returned rather than nothing.
		warns = append(warns, Warning{Role: core.RoleBuild, Message: "all build-eligible models unhealthy; scoring anyway"})
		healthy = pool
	}
	if len(healthy) == 0 {
		return BuildDecision{Ok: false, Warnings: []Warning{{Role: core.RoleBuild, Message: "no build-eligible models configured"}}}
	}

	cands := make([]Candidate, 0, len(healthy))
	for _, m := range healthy {
		cands = append(cands, r.scoreBuild(m, difficulty, opts))
	}
	// Sort best-first; ties break deterministically by model ID so the ranking
	// is a total order.
	sort.SliceStable(cands, func(i, j int) bool {
		if math.Abs(cands[i].Score-cands[j].Score) > scoreEpsilon {
			return cands[i].Score > cands[j].Score
		}
		return cands[i].Option.Model.ID < cands[j].Option.Model.ID
	})

	return BuildDecision{
		Winner:   cands[0],
		Ranked:   cands,
		Warnings: warns,
		Ok:       true,
	}
}

// scoreEpsilon is the tolerance below which two scores are considered tied (and
// broken by model ID). Keeps floating-point noise from perturbing the order.
const scoreEpsilon = 1e-9

// buildPool returns the build-eligible model universe: every declared model in a
// build tier (local + mid), excluding metered unless allowMetered. Order follows
// tier rank then model ID for stable scoring input.
func (r *Registry) buildPool(allowMetered bool) []core.Model {
	var pool []core.Model
	for _, m := range r.cfg.CoreModels() {
		tier := r.tierOf(m.ID)
		if !isBuildTier(tier) {
			continue
		}
		if m.Billing == core.BillingMetered && !allowMetered {
			continue
		}
		pool = append(pool, m)
	}
	sort.SliceStable(pool, func(i, j int) bool {
		ri, rj := rankOf(r.tierOf(pool[i].ID)), rankOf(r.tierOf(pool[j].ID))
		if ri != rj {
			return ri < rj
		}
		return pool[i].ID < pool[j].ID
	})
	return pool
}

// isBuildTier reports whether a tier participates in build routing. The build
// pool is local + mid; top is reserved for plan/review and human-override
// escalations.
func isBuildTier(tier string) bool {
	return tier == tierLocal || tier == tierMid
}

// scoreBuild computes a model's build Candidate for a difficulty.
func (r *Registry) scoreBuild(m core.Model, difficulty core.Difficulty, opts BuildOptions) Candidate {
	success := r.successTerm(m.ID, difficulty)
	speed := r.speedTerm(m.ID)
	cost := costRank[m.Billing]

	score := buildWeights.Success*success + buildWeights.Speed*speed + buildWeights.Cost*cost

	opt := RunOption{Model: m, Effort: m.Effort, Fast: m.Fast, Tier: r.tierOf(m.ID)}
	if opts.EffortOverride != "" {
		opt.Effort = opts.EffortOverride
	}
	if opts.FastOverride {
		opt.Fast = true
	}
	return Candidate{Option: opt, Score: score, Success: success, Speed: speed, Cost: cost}
}

// successTerm is the predicted success probability for a model at a difficulty:
// its historical SuccessRate, or the difficulty-derived cold-start prior when
// there is no history (rate == 0). A store error also falls back to the prior so
// a transient DB hiccup cannot zero out every candidate.
func (r *Registry) successTerm(model string, difficulty core.Difficulty) float64 {
	rate, err := r.hist.SuccessRate(model, difficulty)
	if err != nil || rate <= 0 {
		if p, ok := difficultyPrior[difficulty]; ok {
			return p
		}
		return difficultyPrior[core.DifficultyStandard]
	}
	return clamp01(rate)
}

// speedTerm maps a model's average build latency to [0,1] with a half-life
// decay: faster clears the queue and scores higher. No history (avg == 0) yields
// the neutral prior 0.5 so speed neither rewards nor punishes an unproven model.
func (r *Registry) speedTerm(model string) float64 {
	avg, err := r.hist.AvgDuration(model, buildStage)
	if err != nil || avg <= 0 {
		return 0.5
	}
	return math.Exp2(-float64(avg) / float64(speedHalfLife))
}

func clamp01(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}
