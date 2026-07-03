// Package config loads, validates, merges, and defaults clex configuration.
//
// clex reads two TOML files: a global config at ~/.clex/config.toml (Telegram
// credentials, providers/models/tiers/routing, budget, provider caps, worktree
// root) and an optional per-repo config at .clex/config.toml (head branch,
// verification default, repo-specific routing/skills). The per-repo file
// overlays the global one per-key (shallow). The schema mirrors the spec's
// "Routing: model tiers and the escalation ladder" and "Configuration" sections.
//
// The load path is intentionally forgiving: unknown keys become warnings (not
// errors) for forward compatibility, deleting a whole provider block drops the
// models that referenced it (with a warning) rather than failing, and a routing
// role that resolves to zero models yields a structured warning so clex doctor
// can report the gap. Only genuinely broken references (a model naming an
// undeclared provider, a tier naming an undeclared model) and malformed TOML are
// treated specially — and even those degrade to warnings-with-drop rather than
// hard failure, so a subscription can be removed by deleting one config block.
//
// Types reuse the shared vocabulary in internal/core (core.Model, core.TierMap,
// core.BillingMode, core.Role) wherever they fit; config never redefines them.
package config

import "github.com/reissui/clex/internal/core"

// Config is the fully-resolved clex configuration: the global file with any
// per-repo overlay already merged in. Zero values are meaningful (an empty
// Providers map is a valid, if useless, config that Validate will warn about).
type Config struct {
	// TelegramToken is the bot token from @BotFather. Secret: never logged, never
	// passed into prompts (spec: Configuration, Security model).
	TelegramToken string `toml:"telegram_token"`
	// TelegramChatID is the owner's chat id; every message and callback is
	// authorized against it (spec: Telegram authorization is per-user).
	TelegramChatID int64 `toml:"telegram_chat_id"`
	// WorktreeRoot is where clex creates per-issue git worktrees.
	WorktreeRoot string `toml:"worktree_root"`

	// HeadBranch is the target branch for PRs. Global default "main"; a per-repo
	// config commonly overrides it (spec: Configuration — per-repo head branch).
	HeadBranch string `toml:"head_branch"`
	// Verification is the repo's default verification command, used when an issue
	// body does not carry its own owner-authored command (spec: Security model —
	// verification commands).
	Verification string `toml:"verification"`
	// Skills names repo-specific skills injected on top of the bundled pack.
	Skills []string `toml:"skills"`

	// Providers maps a provider name (e.g. "claude") to its block. Deleting a
	// provider here is a supported operation, not an error.
	Providers map[string]Provider `toml:"providers"`
	// Models maps a model id (e.g. "opus-4-8") to its declaration. Reuses
	// core.BillingMode via Model.
	Models map[string]Model `toml:"models"`
	// Tiers maps a tier name ("top"/"mid"/"local") to an ordered list of model
	// ids. Reuses core.TierMap directly.
	Tiers core.TierMap `toml:"tiers"`
	// Routing maps a role name ("plan"/"build"/"review"/"lint"/"bot") to its
	// routing rule.
	Routing map[string]Routing `toml:"routing"`

	// Budget holds cost-gate thresholds for metered models.
	Budget Budget `toml:"budget"`
	// Update holds self-update policy.
	Update Update `toml:"update"`
	// Caps maps a provider name to its concurrency cap.
	Caps map[string]Cap `toml:"caps"`
}

// Provider is a declared runner backend. Deleting a Provider block is the
// documented way to drop a subscription (spec: Providers are pluggable and
// disposable).
type Provider struct {
	// Kind selects the adapter, e.g. "claude-cli", "codex-cli", "ollama". It is
	// deliberately a free string so new adapter kinds need no config-schema
	// change.
	Kind string `toml:"kind"`
	// Binary optionally overrides the executable path for provider kinds that
	// shell out to a CLI. It is mainly used by tests and by the fake provider;
	// production users normally rely on PATH.
	Binary string `toml:"binary"`
	// Script points the fake provider at a clex-fake-runner JSON script. Other
	// provider kinds ignore it.
	Script string `toml:"script"`
	// Autodetect, when true, lets the adapter discover models dynamically (Ollama
	// list). Discovered models join the local tier (spec: Routing).
	Autodetect bool `toml:"autodetect"`
}

// Model is a model declaration in the [models] table. It carries the same data
// as core.Model but with TOML tags and without the redundant ID field (the id is
// the map key). toModel promotes it to core.Model.
type Model struct {
	// Provider names the [providers.*] block that runs this model. Must reference
	// a declared provider or the model is dropped with a warning.
	Provider string `toml:"provider"`
	// Billing is subscription | metered | free (core.BillingMode).
	Billing core.BillingMode `toml:"billing"`
	// Effort is the default reasoning/thinking level; the adapter translates it.
	Effort string `toml:"effort"`
	// Fast is the default fast-output mode where the provider supports it.
	Fast bool `toml:"fast"`
}

// toModel promotes a config Model to the shared core.Model, stamping in the id
// (the [models] map key).
func (m Model) toModel(id string) core.Model {
	return core.Model{
		ID:       id,
		Provider: m.Provider,
		Billing:  m.Billing,
		Effort:   m.Effort,
		Fast:     m.Fast,
	}
}

// Routing is one [routing.<role>] rule. Exactly one of Tier, Model, or Policy
// selects the model pool; Effort and Fast are per-role overrides. The three
// selectors are mutually exclusive — Validate warns if none is set (the role
// resolves to nothing) and prefers Model, then Tier, then Policy if several are.
type Routing struct {
	// Tier names a [tiers] entry; the role draws from that tier's models.
	Tier string `toml:"tier"`
	// Model pins a single model id (or a provider shorthand like "codex:best"
	// that the registry resolves at runtime).
	Model string `toml:"model"`
	// Policy selects a dynamic strategy, e.g. "auto" for success×speed×cost build
	// routing (spec: build routing weighs success, speed, and cost).
	Policy string `toml:"policy"`
	// Effort overrides the reasoning/thinking level for this role.
	Effort string `toml:"effort"`
	// Fast overrides fast-output mode for this role.
	Fast bool `toml:"fast"`
}

// Budget holds cost-gate thresholds (spec: Cost gates). Zero values disable the
// respective gate.
type Budget struct {
	// ConfirmOverUSD: metered estimates above this require a Telegram confirm.
	ConfirmOverUSD float64 `toml:"confirm_over_usd"`
	// MaxUSDPerEpic: optional hard cap; reaching it pauses the epic.
	MaxUSDPerEpic float64 `toml:"max_usd_per_epic"`
}

// Update holds self-update policy (spec: Self-update).
type Update struct {
	// Auto is the auto-apply level, e.g. "patch" (patch releases auto-apply;
	// anything larger is a one-tap confirm) or "off".
	Auto string `toml:"auto"`
}

// Cap is a per-provider concurrency cap (spec: provider caps; scheduler).
type Cap struct {
	// MaxConcurrent is the maximum number of simultaneous runners for a provider.
	MaxConcurrent int `toml:"max_concurrent"`
}
