// Package core is the shared vocabulary imported by every other clex package.
//
// It is an interface freeze: the types here are the contract that the config,
// store, GitHub, workspace, runner, registry, scheduler, pipeline, and daemon
// packages all depend on. Keep it dependency-free (standard library only) and
// provider-agnostic — no provider name (claude, codex, ollama, …) may appear in
// this package. Extend types as needed for later work, but do not rename the
// fields declared here.
//
// Relevant spec sections: Architecture overview, Runner adapters,
// Routing: model tiers and the escalation ladder.
package core

// BillingMode describes how using a model is paid for. It drives cost gates and
// restriction policy (spec: Runner adapters, Cost gates).
type BillingMode string

const (
	// BillingSubscription consumes a provider's rate window but costs no
	// marginal money (Claude Max / ChatGPT subscription usage).
	BillingSubscription BillingMode = "subscription"
	// BillingMetered is pay-per-token (e.g. Fable 5). Subject to cost gates.
	BillingMetered BillingMode = "metered"
	// BillingFree is a local model (Ollama); no money and no shared window.
	BillingFree BillingMode = "free"
)

// Valid reports whether b is a recognized billing mode.
func (b BillingMode) Valid() bool {
	switch b {
	case BillingSubscription, BillingMetered, BillingFree:
		return true
	default:
		return false
	}
}

// Difficulty is the planner's estimate of how hard an issue is to build. The
// router weighs it against a model's track record when selecting a builder
// (spec: Routing — build routing weighs success, speed, and cost).
type Difficulty string

const (
	DifficultyTrivial  Difficulty = "trivial"
	DifficultyStandard Difficulty = "standard"
	DifficultyComplex  Difficulty = "complex"
)

// Valid reports whether d is a recognized difficulty.
func (d Difficulty) Valid() bool {
	switch d {
	case DifficultyTrivial, DifficultyStandard, DifficultyComplex:
		return true
	default:
		return false
	}
}

// Role is a routing role. Each role resolves to one or more models via config
// tiers (spec: Routing: model tiers and the escalation ladder).
type Role string

const (
	RolePlan   Role = "plan"   // research / PRD / issue-writing (top tier)
	RoleBuild  Role = "build"  // executing an issue (success×speed×cost pool)
	RoleReview Role = "review" // reviewing a PR diff (top tier)
	RoleLint   Role = "lint"   // scoring child issues against the checklist (mid)
	RoleBot    Role = "bot"    // the Telegram bot core (codex:best, fast)
)

// Valid reports whether r is a recognized routing role.
func (r Role) Valid() bool {
	switch r {
	case RolePlan, RoleBuild, RoleReview, RoleLint, RoleBot:
		return true
	default:
		return false
	}
}

// Model is a single model an adapter can run, as declared in config and tracked
// by the registry (spec: Routing — [models] table).
type Model struct {
	ID       string      // model id, e.g. "opus-4-8"
	Provider string      // name of the provider block that runs it
	Billing  BillingMode // subscription | metered | free
	Effort   string      // default reasoning/thinking level; adapter translates
	Fast     bool        // default fast-output mode where supported
}

// TierMap maps a tier name (e.g. "top", "mid", "local") to an ordered list of
// model ids. Order encodes preference within the tier and the escalation ladder
// climbs across tiers (spec: Routing — [tiers], Escalation ladder).
type TierMap map[string][]string
