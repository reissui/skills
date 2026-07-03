// Package registry is clex's model registry and router: it turns the configured
// providers, models and tiers into concrete routing decisions, weighing success,
// speed and cost, and gates spend on metered models.
//
// # Responsibilities
//
//   - Registry holds the configured models/tiers, aggregates each provider's
//     Probe() result, and tracks rate-limit headroom heuristically. Available
//     returns the healthy models backing a routing role, honoring the role's
//     config (explicit model, tier, or policy) with effort/fast resolved per
//     model and returned as run options.
//   - The build router scores build-eligible models (local + mid tiers) for an
//     issue difficulty and returns a winner plus ranked fallbacks.
//   - The escalation ladder climbs one tier at a time (local → mid → top).
//   - Cost gates estimate a metered dispatch's cost and decide proceed / confirm
//     / block against the configured budget.
//
// Providers are consumed abstractly as core.Runner values; the registry never
// makes a direct provider API call and never logs a secret (spec: Security
// model, Runner adapters).
//
// # Build scoring formula (deterministic)
//
// For a candidate build model m and issue difficulty d, the router computes
//
//	score(m) = wSuccess*success(m,d) + wSpeed*speed(m) + wCost*cost(m)
//
// with fixed weights wSuccess=0.60, wSpeed=0.25, wCost=0.15 (they sum to 1) and
// each term normalized to [0,1]:
//
//   - success(m,d) = predicted success probability. It starts from the model's
//     historical SuccessRate at difficulty d; with no history it falls back to a
//     difficulty-derived prior (trivial 0.85, standard 0.60, complex 0.35) so a
//     never-run model is neither blindly trusted nor excluded.
//   - speed(m) = a fast-clearing model scores higher. From AvgDuration(m,"build")
//     it maps 0s→1.0 decaying with a 10-minute half-life: speed = 2^(-avg/10min).
//     No history yields the neutral prior 0.5.
//   - cost(m) = cost-rank preference, free=1.0 > subscription=0.6 > metered=0.0.
//     Metered build candidates are excluded entirely unless AllowMetered is set,
//     so their cost term only matters under an explicit override.
//
// Ties (equal score to 1e-9) break by model ID ascending, so the ranking is a
// total order and fully reproducible from the same inputs. Because every input
// (SuccessRate, AvgDuration, config) is fixed per call, Build is deterministic.
//
// The weights and priors are the documented cold-start policy; they are package
// constants (buildWeights, difficultyPrior, speedHalfLife) so the behavior is
// auditable and stable across runs.
package registry

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/reissui/clex/internal/config"
	"github.com/reissui/clex/internal/core"
)

// Canonical tier names and their escalation rank. Tier names are user
// configurable, but the spec's ladder is local → mid → top; unknown tier names
// sort after these (rank tierRankUnknown) and are treated as non-build tiers.
const (
	tierLocal = "local"
	tierMid   = "mid"
	tierTop   = "top"
)

// tierRank maps a canonical tier name to its position on the escalation ladder
// (lower climbs first). Unknown tiers get tierRankUnknown.
var tierRank = map[string]int{
	tierLocal: 0,
	tierMid:   1,
	tierTop:   2,
}

const tierRankUnknown = 99

// rankOf returns the escalation rank of a tier name.
func rankOf(tier string) int {
	if r, ok := tierRank[tier]; ok {
		return r
	}
	return tierRankUnknown
}

// RunOption is a resolved, ready-to-dispatch model: the model plus the effort
// and fast attributes chosen for the requesting role (role overrides win over
// the model's own defaults). It is what Available hands back so the scheduler
// can build a core.Task without re-consulting config.
type RunOption struct {
	Model  core.Model
	Effort string // resolved reasoning/thinking level
	Fast   bool   // resolved fast-output mode
	Tier   string // the tier this model was drawn from ("" for a pinned model)
}

// Registry holds the configured model universe and the live health of each
// provider. It is safe for concurrent use.
type Registry struct {
	cfg     *config.Config
	hist    History
	runners map[string]core.Runner // provider name → runner

	mu     sync.RWMutex
	health map[string]providerHealth // provider name → last known health

	// now is injected for deterministic tests; defaults to time.Now.
	now func() time.Time
}

// providerHealth is the registry's current belief about one provider, folded
// from the last Probe() plus any rate-limit signals reported since.
type providerHealth struct {
	healthy   bool
	detail    string
	models    []string  // dynamically discovered model ids (local providers)
	rateLimit bool      // a rate-limit signal has fired and not yet cleared
	updated   time.Time // when this belief was last refreshed
}

// Option configures a Registry at construction.
type Option func(*Registry)

// WithHistory injects the history source (normally *store.Store). Without it the
// registry uses a no-op history and every model runs on cold-start priors.
func WithHistory(h History) Option {
	return func(r *Registry) {
		if h != nil {
			r.hist = h
		}
	}
}

// WithClock injects a clock, for deterministic tests.
func WithClock(now func() time.Time) Option {
	return func(r *Registry) {
		if now != nil {
			r.now = now
		}
	}
}

// New builds a Registry from a validated config and the set of provider runners
// (provider name → core.Runner). Runners are consumed abstractly; tests pass
// fakes. Providers start out optimistically healthy until the first Probe.
func New(cfg *config.Config, runners map[string]core.Runner, opts ...Option) *Registry {
	r := &Registry{
		cfg:     cfg,
		hist:    staticHistory{},
		runners: map[string]core.Runner{},
		health:  map[string]providerHealth{},
		now:     time.Now,
	}
	for name, run := range runners {
		r.runners[name] = run
	}
	// Seed optimistic health for every configured provider so a role resolves
	// before the first probe round.
	if cfg != nil {
		for name := range cfg.Providers {
			r.health[name] = providerHealth{healthy: true, updated: r.now()}
		}
	}
	for _, o := range opts {
		o(r)
	}
	return r
}

// Probe refreshes health for every provider that has a runner, calling each
// runner's Probe concurrently and folding the result. Rate-limit beliefs set via
// NoteRateLimit are preserved across a probe only if the probe itself reports
// unhealthy; a healthy probe clears a stale rate-limit flag. Probe never returns
// an error for an individual provider — a failed probe marks that provider
// unhealthy with the error detail and moves on, so one broken provider cannot
// stop the others (spec: single-provider degradation, availability-aware).
func (r *Registry) Probe(ctx context.Context) {
	type result struct {
		name string
		av   core.Availability
		err  error
	}
	results := make(chan result, len(r.runners))
	var wg sync.WaitGroup
	for name, run := range r.runners {
		wg.Add(1)
		go func(name string, run core.Runner) {
			defer wg.Done()
			av, err := run.Probe(ctx)
			results <- result{name: name, av: av, err: err}
		}(name, run)
	}
	go func() { wg.Wait(); close(results) }()

	for res := range results {
		r.mu.Lock()
		h := r.health[res.name]
		h.updated = r.now()
		if res.err != nil {
			h.healthy = false
			h.detail = redactError(res.err)
		} else {
			h.healthy = res.av.Healthy
			h.detail = res.av.Detail
			h.models = res.av.Models
			if res.av.Healthy {
				h.rateLimit = false // a clean probe clears a stale rate-limit belief
			}
		}
		r.health[res.name] = h
		r.mu.Unlock()
	}
}

// NoteRateLimit records that a provider emitted a rate-limit / near-cap signal
// (e.g. a runner event or a 429 seen by the scheduler). It flips the provider to
// unhealthy immediately so routing skips it until the next clean Probe. This is
// the heuristic headroom tracking the spec calls for; it takes only the provider
// name, never a token or response body (spec: no secrets logged).
func (r *Registry) NoteRateLimit(provider, detail string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	h := r.health[provider]
	h.rateLimit = true
	h.healthy = false
	if detail != "" {
		h.detail = detail
	} else if h.detail == "" {
		h.detail = "rate limited"
	}
	h.updated = r.now()
	r.health[provider] = h
}

// providerHealthy reports whether a provider is currently believed healthy.
// Unknown providers (no runner, no probe) are treated as healthy so a
// config-only registry still resolves roles.
func (r *Registry) providerHealthy(name string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	h, ok := r.health[name]
	if !ok {
		return true
	}
	return h.healthy && !h.rateLimit
}

// modelHealthy reports whether a model can currently run: its provider must be
// declared and healthy.
func (r *Registry) modelHealthy(m core.Model) bool {
	return r.providerHealthy(m.Provider)
}

// Warning is a non-fatal routing note (e.g. a role that fell through to another
// tier because its own was unhealthy). Callers surface these the way config
// warnings are surfaced by clex doctor.
type Warning struct {
	Role    core.Role
	Message string
}

func (w Warning) String() string {
	if w.Role != "" {
		return fmt.Sprintf("routing[%s]: %s", w.Role, w.Message)
	}
	return "routing: " + w.Message
}

// Available returns the healthy, ready-to-dispatch models for a role, honoring
// the role's config (explicit model → tier → policy) with effort/fast resolved
// per model. The second return is a slice of warnings: a role never returns an
// empty option set while any healthy model exists anywhere — if the role's own
// pool is entirely unhealthy (or the role is unconfigured), the registry falls
// through to the next-best tier and records a warning rather than erroring
// (spec: single-provider degradation, "every role still resolves").
func (r *Registry) Available(role core.Role) ([]RunOption, []Warning) {
	if r.cfg == nil {
		return nil, []Warning{{Role: role, Message: "no configuration loaded"}}
	}
	rule := r.cfg.Routing[string(role)]
	effortOverride, fastOverride := rule.Effort, rule.Fast

	// Resolve the role's declared pool via config (already tier-ordered and
	// pruned by config.Validate).
	pool := r.cfg.ModelsForRole(role)

	// Handle a pinned Model that is a runtime shorthand (e.g. "codex:best"):
	// config returns an empty slice because it is not in [models]. Resolve it
	// against the live model set here.
	var warns []Warning
	if len(pool) == 0 && rule.Model != "" {
		if resolved, ok := r.resolveShorthand(rule.Model); ok {
			pool = []core.Model{resolved}
		} else {
			warns = append(warns, Warning{Role: role, Message: fmt.Sprintf("pinned model %q not resolvable", rule.Model)})
		}
	}

	opts := r.toOptions(pool, effortOverride, fastOverride, healthyOnly)
	if len(opts) > 0 {
		return opts, warns
	}

	// Degradation path: the role's own pool is empty or entirely unhealthy.
	// Fall through to any healthy model, cheapest tier first, so the role still
	// resolves (spec: never return empty for a role with any healthy model).
	fallback := r.healthyFallback(effortOverride, fastOverride)
	if len(fallback) > 0 {
		reason := "role pool empty"
		if len(pool) > 0 {
			reason = "role pool unhealthy"
		}
		warns = append(warns, Warning{Role: role, Message: reason + "; fell through to " + fallback[0].Model.ID})
		return fallback, warns
	}

	warns = append(warns, Warning{Role: role, Message: "no healthy model available for role"})
	return nil, warns
}

// selectMode controls whether toOptions filters out unhealthy models.
type selectMode int

const (
	healthyOnly selectMode = iota
	includeAll
)

// toOptions promotes core.Models to RunOptions, applying role effort/fast
// overrides (a set override wins; otherwise the model's own default is kept) and
// stamping the model's tier. In healthyOnly mode unhealthy models are dropped.
func (r *Registry) toOptions(models []core.Model, effortOverride string, fastOverride bool, mode selectMode) []RunOption {
	out := make([]RunOption, 0, len(models))
	for _, m := range models {
		if mode == healthyOnly && !r.modelHealthy(m) {
			continue
		}
		opt := RunOption{
			Model:  m,
			Effort: m.Effort,
			Fast:   m.Fast,
			Tier:   r.tierOf(m.ID),
		}
		if effortOverride != "" {
			opt.Effort = effortOverride
		}
		if fastOverride {
			opt.Fast = true
		}
		out = append(out, opt)
	}
	return out
}

// healthyFallback returns every healthy declared model as RunOptions, ordered
// cheapest tier first (local, mid, top) then by model ID, so degradation lands
// on the least-expensive still-working model.
func (r *Registry) healthyFallback(effortOverride string, fastOverride bool) []RunOption {
	models := r.cfg.CoreModels()
	opts := r.toOptions(models, effortOverride, fastOverride, healthyOnly)
	sort.SliceStable(opts, func(i, j int) bool {
		ri, rj := rankOf(opts[i].Tier), rankOf(opts[j].Tier)
		if ri != rj {
			return ri < rj
		}
		return opts[i].Model.ID < opts[j].Model.ID
	})
	return opts
}

// tierOf returns the (first) tier that lists a model id, preferring the
// lowest-rank tier if a model appears in several. Returns "" if untiered.
func (r *Registry) tierOf(id string) string {
	best := ""
	bestRank := tierRankUnknown + 1
	for tier, ids := range r.cfg.Tiers {
		for _, mid := range ids {
			if mid == id {
				if rk := rankOf(tier); rk < bestRank {
					bestRank, best = rk, tier
				}
			}
		}
	}
	return best
}

// resolveShorthand resolves a "provider:best" pin to the best healthy model that
// provider offers. "best" means the highest-tier (top → mid → local) healthy
// model; ties break by model ID. Returns false if nothing resolves.
func (r *Registry) resolveShorthand(pin string) (core.Model, bool) {
	provider, sel, ok := strings.Cut(pin, ":")
	if !ok || sel != "best" {
		return core.Model{}, false
	}
	var cands []core.Model
	for _, m := range r.cfg.CoreModels() {
		if m.Provider == provider && r.modelHealthy(m) {
			cands = append(cands, m)
		}
	}
	if len(cands) == 0 {
		return core.Model{}, false
	}
	sort.SliceStable(cands, func(i, j int) bool {
		ri, rj := rankOf(r.tierOf(cands[i].ID)), rankOf(r.tierOf(cands[j].ID))
		if ri != rj {
			return ri > rj // higher tier first ("best")
		}
		return cands[i].ID < cands[j].ID
	})
	return cands[0], true
}
