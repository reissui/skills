package update

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/reissui/clex/internal/config"
)

// Layer 3 of self-update: models (spec: Self-update layer 3). After a CLI update
// and on the daily tick the registry re-probes; newly available models (incl.
// fresh `ollama list` entries) are announced with a proposed tier —
// `sonnet-5.1 detected — add to mid? [✓] [ignore]` — and retired or renamed
// models trigger a config fix-up proposal, so tiers never silently rot. This
// package only PRODUCES the proposals (Proposal / ProposalKind in events.go);
// #17/#18 consume them. The diff is a pure function over (discovered, config),
// so fixtures for added/removed/renamed each yield a deterministic proposal.

// DiscoveredModels maps a provider name to the model ids a re-probe reported for
// it (from core.Availability.Models — e.g. the dynamic `ollama list` set, or a
// provider that publishes its catalogue). It is the input to the model diff.
type DiscoveredModels map[string][]string

// ModelReprober triggers a registry re-probe and returns the freshly discovered
// models per provider. The re-probe itself is (*registry.Registry).Probe (see
// Prober); exposing the discovered set is done via an injected function at
// wiring time (the registry folds probe results internally). Tests supply a fake
// that returns fixture data, so no runner is spawned.
type ModelReprober interface {
	// Reprobe re-probes providers and returns the discovered models per provider.
	Reprobe(ctx context.Context) (DiscoveredModels, error)
}

// ModelDiffer produces model proposals by comparing a re-probe against config.
type ModelDiffer struct {
	// Reprober triggers the re-probe and yields discovered models; required.
	Reprober ModelReprober
}

// Propose re-probes and diffs the result against cfg, returning proposals for
// added / removed / renamed models. It performs the re-probe via the injected
// Reprober (no live runner in tests) and then delegates to the pure DiffModels.
func (d *ModelDiffer) Propose(ctx context.Context, cfg *config.Config) ([]Proposal, error) {
	if d.Reprober == nil {
		return nil, fmt.Errorf("update: ModelDiffer has no ModelReprober")
	}
	discovered, err := d.Reprober.Reprobe(ctx)
	if err != nil {
		return nil, err
	}
	return DiffModels(cfg, discovered), nil
}

// DiffModels compares the models a re-probe discovered against those declared in
// cfg and returns proposals. It is pure (no I/O), so it is the unit fixtures
// drive directly. Rules:
//
//   - discovered ∧ ¬configured → KindModelAdd with a guessed tier.
//   - configured ∧ ¬discovered (for a provider that WAS probed) → either a
//     KindModelRename (when exactly one added and one removed model share a
//     provider and look like a rename) or a KindModelRemove config fix-up.
//   - a provider that returned no discovered models at all is treated as "did
//     not report a catalogue" and its configured models are NOT flagged missing
//     (avoids false removals when a provider simply doesn't enumerate models).
//
// Proposals are returned in a stable order (adds, then renames, then removes;
// each group sorted by model id) so golden tests are deterministic.
func DiffModels(cfg *config.Config, discovered DiscoveredModels) []Proposal {
	configured := configuredByProvider(cfg)

	var adds, removes []ModelProposal
	for provider, ids := range discovered {
		if len(ids) == 0 {
			continue // provider reported no catalogue; skip its config models
		}
		haveDiscovered := toSet(ids)
		haveConfigured := toSet(configured[provider])

		// Added: discovered but not configured.
		for _, id := range sortedKeys(haveDiscovered) {
			if !haveConfigured[id] {
				adds = append(adds, ModelProposal{
					Provider: provider,
					Model:    id,
					Tier:     guessTier(provider, id, cfg),
				})
			}
		}
		// Removed: configured but no longer discovered (only for a provider that
		// DID report a catalogue, guarded above).
		for _, id := range sortedKeys(haveConfigured) {
			if !haveDiscovered[id] {
				removes = append(removes, ModelProposal{Provider: provider, Model: id})
			}
		}
	}

	// Detect renames: within a provider, exactly one add + one remove is treated
	// as a rename (old→new). Consumed adds/removes are withdrawn from their
	// groups so they are not double-reported.
	renames, adds, removes := extractRenames(adds, removes)

	var out []Proposal
	for _, a := range adds {
		out = append(out, Proposal{
			Kind:    KindModelAdd,
			Message: fmt.Sprintf("%s detected — add to %s? [✓] [ignore]", a.Model, a.Tier),
			Model:   &ModelProposal{Provider: a.Provider, Model: a.Model, Tier: a.Tier},
		})
	}
	for _, r := range renames {
		rr := r
		out = append(out, Proposal{
			Kind: KindModelRename,
			Message: fmt.Sprintf("model %s looks renamed to %s — update config? [✓] [ignore]",
				rr.OldModel, rr.Model),
			Model: &rr,
		})
	}
	for _, r := range removes {
		rr := r
		out = append(out, Proposal{
			Kind:    KindModelRemove,
			Message: fmt.Sprintf("model %s no longer available — remove from config? [✓] [ignore]", rr.Model),
			Model:   &rr,
		})
	}
	return out
}

// configuredByProvider inverts cfg.Models into provider → []model-id.
func configuredByProvider(cfg *config.Config) map[string][]string {
	out := map[string][]string{}
	if cfg == nil {
		return out
	}
	for id, m := range cfg.Models {
		out[m.Provider] = append(out[m.Provider], id)
	}
	return out
}

// extractRenames pulls rename pairs out of the add/remove groups. Within a
// single provider, if there is exactly one added model and exactly one removed
// model, they are assumed to be a rename (old removed id → new added id). This
// matches the spec's "renamed models trigger a config fix-up" without needing
// fuzzy matching, and it is deterministic. Providers with any other add/remove
// balance keep their adds and removes as-is. Returned slices are the leftover
// adds and removes after renames are extracted.
func extractRenames(adds, removes []ModelProposal) (renames, restAdds, restRemoves []ModelProposal) {
	addsBy := groupByProvider(adds)
	remsBy := groupByProvider(removes)

	for provider := range addsBy {
		a := addsBy[provider]
		r := remsBy[provider]
		if len(a) == 1 && len(r) == 1 {
			renames = append(renames, ModelProposal{
				Provider: provider,
				Model:    a[0].Model, // new id
				OldModel: r[0].Model, // old id
			})
			delete(addsBy, provider)
			delete(remsBy, provider)
		}
	}
	for _, a := range addsBy {
		restAdds = append(restAdds, a...)
	}
	for _, r := range remsBy {
		restRemoves = append(restRemoves, r...)
	}
	sortProposals(renames)
	sortProposals(restAdds)
	sortProposals(restRemoves)
	return renames, restAdds, restRemoves
}

// guessTier proposes a tier for a newly discovered model. The heuristic is
// deliberately simple and spec-aligned (Routing: discovered local models join
// the local tier):
//   - a model on an autodetect provider (Ollama) → "local" if that tier exists.
//   - otherwise, the tier of any already-configured sibling model on the same
//     provider (keeps a new provider model with its peers).
//   - failing both, the middle tier ("mid") if present, else the first tier by
//     name, else "" (caller renders "add to ?").
func guessTier(provider, _ string, cfg *config.Config) string {
	if cfg == nil {
		return ""
	}
	if prov, ok := cfg.Providers[provider]; ok && prov.Autodetect {
		if _, ok := cfg.Tiers["local"]; ok {
			return "local"
		}
	}
	// Sibling tier: find a configured model on this provider and use its tier.
	for id, m := range cfg.Models {
		if m.Provider == provider {
			if t := tierOfModel(cfg, id); t != "" {
				return t
			}
		}
	}
	if _, ok := cfg.Tiers["mid"]; ok {
		return "mid"
	}
	for _, name := range sortedKeys(toSet(tierNames(cfg))) {
		return name
	}
	return ""
}

// tierOfModel returns the tier that lists id, or "" if none.
func tierOfModel(cfg *config.Config, id string) string {
	for tier, ids := range cfg.Tiers {
		for _, m := range ids {
			if m == id {
				return tier
			}
		}
	}
	return ""
}

// tierNames returns the configured tier names.
func tierNames(cfg *config.Config) []string {
	var out []string
	for name := range cfg.Tiers {
		out = append(out, name)
	}
	return out
}

// --- small set/sort helpers (stdlib only) ---

func toSet(ss []string) map[string]bool {
	m := make(map[string]bool, len(ss))
	for _, s := range ss {
		if s = strings.TrimSpace(s); s != "" {
			m[s] = true
		}
	}
	return m
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func groupByProvider(ps []ModelProposal) map[string][]ModelProposal {
	out := map[string][]ModelProposal{}
	for _, p := range ps {
		out[p.Provider] = append(out[p.Provider], p)
	}
	return out
}

func sortProposals(ps []ModelProposal) {
	sort.Slice(ps, func(i, j int) bool { return ps[i].Model < ps[j].Model })
}
