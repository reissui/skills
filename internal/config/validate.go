package config

import (
	"fmt"
	"sort"

	"github.com/reissui/clex/internal/core"
)

// requiredRoles is the set of routing roles clex needs to run the full pipeline.
// Each must resolve to at least one model or Validate emits a WarnEmptyRole so
// clex doctor can report the gap (spec: clex doctor validates that each role
// resolves to at least one healthy model and warns on gaps).
var requiredRoles = []core.Role{
	core.RolePlan, core.RoleBuild, core.RoleReview, core.RoleLint, core.RoleBot,
}

// Validate checks the merged config's referential integrity and returns a list
// of warnings. It is deliberately non-fatal: deleting a provider, an orphaned
// model, a dangling tier entry, an empty role, and unknown routing all produce
// warnings rather than errors, so the config still loads in a degraded-but-known
// state (spec: Providers are pluggable and disposable). The returned warnings
// are sorted by kind then message for deterministic test assertions.
//
// Validate is pure with respect to references but does mutate the receiver: it
// drops orphaned models and dangling tier entries in place so downstream code
// (the registry, scheduler) sees only usable references. Call it once, after any
// per-repo merge.
func (c *Config) Validate() []Warning {
	var warns []Warning

	warns = append(warns, c.pruneOrphanModels()...)
	warns = append(warns, c.pruneDanglingTierEntries()...)
	warns = append(warns, c.checkBillingModes()...)
	warns = append(warns, c.checkRoles()...)

	sort.Slice(warns, func(i, j int) bool {
		if warns[i].Kind != warns[j].Kind {
			return warns[i].Kind < warns[j].Kind
		}
		return warns[i].Message < warns[j].Message
	})
	return warns
}

// pruneOrphanModels drops any model whose provider is not a declared provider
// block (the documented result of deleting a subscription) and warns for each.
func (c *Config) pruneOrphanModels() []Warning {
	var warns []Warning
	for _, id := range sortedKeys(c.Models) {
		m := c.Models[id]
		if _, ok := c.Providers[m.Provider]; !ok {
			warns = append(warns, Warning{
				Kind: WarnOrphanModel,
				Message: fmt.Sprintf(
					"model %q references undeclared provider %q; dropping model",
					id, m.Provider),
			})
			delete(c.Models, id)
		}
	}
	return warns
}

// pruneDanglingTierEntries removes tier entries that name a model id which is not
// declared (or was just dropped as an orphan) and warns for each removed entry.
// A tier left empty by this pruning is retained as an empty slice — the empty
// role check downstream is what surfaces the user-facing gap.
func (c *Config) pruneDanglingTierEntries() []Warning {
	var warns []Warning
	for _, tier := range sortedKeys(c.Tiers) {
		ids := c.Tiers[tier]
		kept := ids[:0:0] // fresh backing array so we never alias the input
		for _, id := range ids {
			if _, ok := c.Models[id]; ok {
				kept = append(kept, id)
				continue
			}
			warns = append(warns, Warning{
				Kind: WarnDanglingTierEntry,
				Message: fmt.Sprintf(
					"tier %q references unknown model %q; dropping entry",
					tier, id),
			})
		}
		c.Tiers[tier] = kept
	}
	return warns
}

// checkBillingModes warns about (but keeps) any surviving model whose billing
// mode is not a recognized core.BillingMode, so a typo shows up in doctor
// without silently changing cost-gate behavior.
func (c *Config) checkBillingModes() []Warning {
	var warns []Warning
	for _, id := range sortedKeys(c.Models) {
		m := c.Models[id]
		if m.Billing != "" && !m.Billing.Valid() {
			warns = append(warns, Warning{
				Kind: WarnBadValue,
				Message: fmt.Sprintf(
					"model %q has unrecognized billing mode %q", id, m.Billing),
			})
		}
	}
	return warns
}

// checkRoles verifies that every required routing role resolves to at least one
// model. Unknown role names and rules with no selector warn as WarnBadRouting; a
// role that resolves to zero models warns as WarnEmptyRole. None are fatal.
func (c *Config) checkRoles() []Warning {
	var warns []Warning

	// Flag any configured role name clex does not recognize.
	for _, name := range sortedKeys(c.Routing) {
		if !core.Role(name).Valid() {
			warns = append(warns, Warning{
				Kind:    WarnBadRouting,
				Message: fmt.Sprintf("routing role %q is not a recognized role", name),
			})
		}
	}

	for _, role := range requiredRoles {
		rule, ok := c.Routing[string(role)]
		if !ok {
			warns = append(warns, Warning{
				Kind:    WarnEmptyRole,
				Message: fmt.Sprintf("role %q has no routing rule; it resolves to no model", role),
			})
			continue
		}
		models, w := c.resolveRole(role, rule)
		warns = append(warns, w...)
		if len(models) == 0 && w == nil {
			warns = append(warns, Warning{
				Kind:    WarnEmptyRole,
				Message: fmt.Sprintf("role %q resolves to no model", role),
			})
		}
	}
	return warns
}

// resolveRole returns the model ids a routing rule selects, plus any warnings
// about the rule itself. A rule that pins a Model or a Policy resolves without
// consulting tiers (a pinned model or a dynamic policy may legitimately reference
// a runtime-resolved id like "codex:best", so an empty tier lookup is not a gap
// there). A Tier rule resolves to that tier's surviving members. A rule with no
// selector is malformed.
func (c *Config) resolveRole(role core.Role, r Routing) ([]string, []Warning) {
	switch {
	case r.Model != "":
		return []string{r.Model}, nil
	case r.Policy != "":
		// A policy draws from a runtime-computed pool (e.g. build's
		// success×speed×cost over local+mid); treat it as satisfiable here.
		return c.policyPool(), nil
	case r.Tier != "":
		ids := c.Tiers[r.Tier]
		if _, known := c.Tiers[r.Tier]; !known {
			return nil, []Warning{{
				Kind: WarnBadRouting,
				Message: fmt.Sprintf(
					"role %q references unknown tier %q", role, r.Tier),
			}}
		}
		return ids, nil
	default:
		return nil, []Warning{{
			Kind: WarnBadRouting,
			Message: fmt.Sprintf(
				"role %q has no tier, model, or policy set", role),
		}}
	}
}

// policyPool returns every declared model id, representing the widest pool a
// dynamic policy could draw from. It exists so a policy-driven role (build) is
// considered satisfiable whenever any model survives.
func (c *Config) policyPool() []string {
	return sortedKeys(c.Models)
}

// sortedKeys returns the keys of m in sorted order, so validation output and
// pruning are deterministic regardless of Go's map iteration order.
func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
