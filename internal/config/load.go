package config

import (
	"fmt"
	"os"

	"github.com/BurntSushi/toml"
)

// Warning is a non-fatal configuration issue surfaced to the caller (and, via
// clex doctor, to the user). Loading returns a slice of these alongside the
// Config; an empty slice means a clean load. Warnings never abort loading —
// only malformed TOML or an unreadable file does.
type Warning struct {
	// Kind classifies the warning so callers (and doctor) can group or filter.
	Kind WarningKind
	// Message is the human-readable description.
	Message string
}

func (w Warning) String() string { return fmt.Sprintf("%s: %s", w.Kind, w.Message) }

// WarningKind enumerates the categories of configuration warning.
type WarningKind string

const (
	// WarnUnknownKey: a key or table this clex version does not recognize
	// (forward compatibility — a newer config on an older binary).
	WarnUnknownKey WarningKind = "unknown-key"
	// WarnOrphanModel: a model references a provider block that is not declared
	// (e.g. after deleting a subscription); the model is dropped.
	WarnOrphanModel WarningKind = "orphan-model"
	// WarnDanglingTierEntry: a tier lists a model id that is not declared (or was
	// dropped); the entry is removed from the tier.
	WarnDanglingTierEntry WarningKind = "dangling-tier-entry"
	// WarnEmptyRole: a routing role resolves to zero usable models; clex doctor
	// reports it. Not fatal.
	WarnEmptyRole WarningKind = "empty-role"
	// WarnBadRouting: a routing rule is malformed (no selector, or an unknown
	// role name).
	WarnBadRouting WarningKind = "bad-routing"
	// WarnBadValue: a value is present but invalid (e.g. an unrecognized billing
	// mode); the surrounding item is dropped or the value ignored.
	WarnBadValue WarningKind = "bad-value"
)

// LoadGlobal reads and validates the global config at path. A missing file is
// reported via the returned error (callers decide whether that is fatal or
// should trigger the init wizard); malformed TOML is always an error. Semantic
// problems (orphan models, empty roles, unknown keys) come back as warnings, not
// errors, so a config with a deleted provider still loads.
func LoadGlobal(path string) (*Config, []Warning, error) {
	cfg, warns, err := parseFile(path)
	if err != nil {
		return nil, nil, err
	}
	cfg.applyGlobalDefaults()
	warns = append(warns, cfg.Validate()...)
	return cfg, warns, nil
}

// Load reads the global config at globalPath and, if repoPath is non-empty and
// the file exists, overlays the per-repo config on top of it (per-key, shallow)
// before validating the merged result. A missing per-repo file is not an error
// (most repos rely purely on global config); a missing global file is. Malformed
// TOML in either file is an error.
func Load(globalPath, repoPath string) (*Config, []Warning, error) {
	global, gWarns, err := parseFile(globalPath)
	if err != nil {
		return nil, nil, fmt.Errorf("global config: %w", err)
	}
	global.applyGlobalDefaults()
	warns := gWarns

	if repoPath != "" {
		if _, statErr := os.Stat(repoPath); statErr == nil {
			repo, rWarns, err := parseFile(repoPath)
			if err != nil {
				return nil, nil, fmt.Errorf("repo config: %w", err)
			}
			global.merge(repo)
			warns = append(warns, rWarns...)
		}
	}

	warns = append(warns, global.Validate()...)
	return global, warns, nil
}

// parseFile decodes a single TOML file into a Config and collects any undecoded
// (unknown) keys as WarnUnknownKey warnings. It does not apply defaults or run
// validation — callers do that after any merge.
func parseFile(path string) (*Config, []Warning, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}
	return parseBytes(data)
}

// parseBytes decodes TOML from b. Split out from parseFile so tests can decode
// in-memory fixtures without touching the filesystem.
func parseBytes(b []byte) (*Config, []Warning, error) {
	var cfg Config
	md, err := toml.Decode(string(b), &cfg)
	if err != nil {
		return nil, nil, fmt.Errorf("parse toml: %w", err)
	}
	var warns []Warning
	for _, key := range md.Undecoded() {
		warns = append(warns, Warning{
			Kind:    WarnUnknownKey,
			Message: fmt.Sprintf("unrecognized key %q (ignored)", key.String()),
		})
	}
	return &cfg, warns, nil
}

// applyGlobalDefaults fills in global-scope defaults that make a sparse config
// runnable. It is only called for the global config (not per-repo overlays,
// which must not resurrect keys the repo left blank on purpose).
func (c *Config) applyGlobalDefaults() {
	if c.HeadBranch == "" {
		c.HeadBranch = defaultHeadBranch
	}
	if c.WorktreeRoot == "" {
		c.WorktreeRoot = defaultWorktreeRoot
	}
	if c.Update.Auto == "" {
		c.Update.Auto = defaultUpdateAuto
	}
}

// merge overlays repo onto c per-key (shallow): a scalar or map set in repo
// replaces the corresponding value in c wholesale; a value the repo leaves at
// its zero value is left untouched so the global value shows through. Maps are
// replaced whole (shallow), not deep-merged — a per-repo [routing.plan] block
// replaces the global plan rule entirely, matching the spec's "per-key, shallow"
// contract.
func (c *Config) merge(repo *Config) {
	if repo.TelegramToken != "" {
		c.TelegramToken = repo.TelegramToken
	}
	if repo.TelegramChatID != 0 {
		c.TelegramChatID = repo.TelegramChatID
	}
	if repo.WorktreeRoot != "" {
		c.WorktreeRoot = repo.WorktreeRoot
	}
	if repo.HeadBranch != "" {
		c.HeadBranch = repo.HeadBranch
	}
	if repo.Verification != "" {
		c.Verification = repo.Verification
	}
	if repo.Skills != nil {
		c.Skills = repo.Skills
	}
	if repo.Providers != nil {
		c.Providers = mergeMap(c.Providers, repo.Providers)
	}
	if repo.Models != nil {
		c.Models = mergeMap(c.Models, repo.Models)
	}
	if repo.Tiers != nil {
		c.Tiers = mergeMap(c.Tiers, repo.Tiers)
	}
	if repo.Routing != nil {
		c.Routing = mergeMap(c.Routing, repo.Routing)
	}
	if repo.Budget != (Budget{}) {
		c.Budget = repo.Budget
	}
	if repo.Update != (Update{}) {
		c.Update = repo.Update
	}
	if repo.Caps != nil {
		c.Caps = mergeMap(c.Caps, repo.Caps)
	}
}

// mergeMap returns base with every key from overlay set on top (shallow): keys
// present only in base survive, keys in overlay replace or add. base may be nil.
func mergeMap[K comparable, V any](base, overlay map[K]V) map[K]V {
	if base == nil {
		base = make(map[K]V, len(overlay))
	}
	for k, v := range overlay {
		base[k] = v
	}
	return base
}
