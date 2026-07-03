package config

import "github.com/reissui/clex/internal/core"

// Default-value constants applied to a sparse global config.
const (
	defaultHeadBranch   = "main"
	defaultWorktreeRoot = "~/.clex/worktrees"
	defaultUpdateAuto   = "patch"
)

// Default returns a minimal but runnable configuration given only a Telegram
// bot token, chat id, and a single provider (its name and adapter kind). Every
// routing role is pointed at the one provider's model so the result passes
// Validate with no empty-role warnings — this is the config the init wizard
// writes for a first-run, single-provider setup (spec: Configuration; issue
// acceptance criterion "Default() returns a runnable config given only a
// Telegram token and one provider").
//
// The single declared model is a subscription model named modelID, on the tier
// "default", which every role routes to. Callers that know a better model id
// (e.g. from a probe) can overwrite Models/Tiers afterward; the shape is what
// matters here.
func Default(telegramToken string, chatID int64, providerName, providerKind, modelID string) *Config {
	tier := "default"
	c := &Config{
		TelegramToken:  telegramToken,
		TelegramChatID: chatID,
		Providers: map[string]Provider{
			providerName: {Kind: providerKind},
		},
		Models: map[string]Model{
			modelID: {Provider: providerName, Billing: core.BillingSubscription},
		},
		Tiers: core.TierMap{
			tier: {modelID},
		},
		Routing: map[string]Routing{
			string(core.RolePlan):   {Tier: tier},
			string(core.RoleBuild):  {Tier: tier},
			string(core.RoleReview): {Tier: tier},
			string(core.RoleLint):   {Tier: tier},
			string(core.RoleBot):    {Tier: tier},
		},
	}
	c.applyGlobalDefaults()
	return c
}

// ModelsForRole returns the resolved core.Model list backing a routing role, in
// tier order, after any Validate() pruning. It is the accessor the registry and
// scheduler use instead of walking Routing/Tiers/Models themselves. A pinned
// Model rule returns that single model if it is declared (an empty slice if it
// is a runtime shorthand like "codex:best" that is not in the [models] table);
// a Policy or Tier rule returns the corresponding declared models. Unknown or
// unset roles return nil.
func (c *Config) ModelsForRole(role core.Role) []core.Model {
	rule, ok := c.Routing[string(role)]
	if !ok {
		return nil
	}
	ids, warns := c.resolveRole(role, rule)
	if warns != nil {
		return nil
	}
	out := make([]core.Model, 0, len(ids))
	for _, id := range ids {
		if m, ok := c.Models[id]; ok {
			out = append(out, m.toModel(id))
		}
	}
	return out
}

// CoreModels returns every declared model as a core.Model slice, in sorted id
// order. Convenience for the registry, which tracks all models regardless of
// which tier or role references them.
func (c *Config) CoreModels() []core.Model {
	ids := sortedKeys(c.Models)
	out := make([]core.Model, 0, len(ids))
	for _, id := range ids {
		out = append(out, c.Models[id].toModel(id))
	}
	return out
}
