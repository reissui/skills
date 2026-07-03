package registry

import (
	"sort"

	"github.com/reissui/clex/internal/core"
)

// Escalate returns the model one tier above current on the escalation ladder
// (local → mid → top), for handing a twice-failed build to a stronger model
// (spec: Escalation ladder). It prefers a model from a *different* provider than
// current so a provider-specific failure (a bad rate window, a model blind spot)
// is more likely to be escaped; if the next tier has only same-provider models,
// it takes the best same-provider one rather than failing.
//
// The second return is false when current is already at the top tier (nowhere to
// climb) or when no model exists in any tier above it — the caller then stops
// and surfaces [skip] (spec: "a builder that fails twice is stopped; false at
// top").
//
// Selection within the target tier is deterministic: healthy models first, then
// different-provider before same-provider, then by model ID.
func (r *Registry) Escalate(current core.Model) (core.Model, bool) {
	curTier := r.tierOf(current.ID)
	curRank := rankOf(curTier)
	if curRank >= rankOf(tierTop) {
		return core.Model{}, false // already top (or untiered/unknown): nowhere up
	}

	// Find the next-higher tier that actually has models, climbing one rank at a
	// time so we never skip a populated tier.
	for target := curRank + 1; target <= rankOf(tierTop); target++ {
		cands := r.modelsAtRank(target)
		if len(cands) == 0 {
			continue
		}
		best, ok := r.pickEscalation(cands, current.Provider)
		if ok {
			return best, true
		}
	}
	return core.Model{}, false
}

// modelsAtRank returns every declared model whose tier has the given rank.
func (r *Registry) modelsAtRank(rank int) []core.Model {
	var out []core.Model
	for _, m := range r.cfg.CoreModels() {
		if rankOf(r.tierOf(m.ID)) == rank {
			out = append(out, m)
		}
	}
	return out
}

// pickEscalation chooses the best model from a tier for escalation, preserving
// provider diversity: healthy > unhealthy, different-provider > same-provider,
// then model ID. It returns false only if the slice is empty.
func (r *Registry) pickEscalation(cands []core.Model, avoidProvider string) (core.Model, bool) {
	if len(cands) == 0 {
		return core.Model{}, false
	}
	sorted := make([]core.Model, len(cands))
	copy(sorted, cands)
	sort.SliceStable(sorted, func(i, j int) bool {
		hi, hj := r.modelHealthy(sorted[i]), r.modelHealthy(sorted[j])
		if hi != hj {
			return hi // healthy first
		}
		di := sorted[i].Provider != avoidProvider
		dj := sorted[j].Provider != avoidProvider
		if di != dj {
			return di // different provider first (provider diversity)
		}
		return sorted[i].ID < sorted[j].ID
	})
	return sorted[0], true
}
