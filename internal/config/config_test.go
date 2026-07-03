package config

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/reissui/clex/internal/core"
)

// warnKinds returns the sorted, deduplicated set of warning kinds present in ws,
// so tests can assert on which categories fired without pinning exact messages.
func warnKinds(ws []Warning) []WarningKind {
	seen := map[WarningKind]bool{}
	for _, w := range ws {
		seen[w.Kind] = true
	}
	out := make([]WarningKind, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

func hasKind(ws []Warning, k WarningKind) bool {
	for _, w := range ws {
		if w.Kind == k {
			return true
		}
	}
	return false
}

func testdata(t *testing.T, name string) string {
	t.Helper()
	return filepath.Join("testdata", name)
}

// TestLoadGlobal is the table-driven acceptance suite: it loads every fixture the
// issue enumerates and asserts on the warnings and the resolved shape.
func TestLoadGlobal(t *testing.T) {
	tests := []struct {
		name    string
		file    string
		wantErr bool
		// wantWarnKinds, when non-nil, must equal the exact set of warning kinds.
		wantWarnKinds []WarningKind
		// check runs extra assertions on a successfully loaded config.
		check func(t *testing.T, c *Config, ws []Warning)
	}{
		{
			// The spec's canonical example (copied verbatim into full.toml)
			// lists codex-mini in the mid tier but does not declare it in
			// [models]. That surfaces exactly one dangling-tier-entry warning —
			// proving the loader tolerates the real spec example gracefully. No
			// role points solely at the emptied slot, so no role goes empty.
			name:          "full config parses, one dangling tier entry from the spec example",
			file:          "full.toml",
			wantWarnKinds: []WarningKind{WarnDanglingTierEntry},
			check: func(t *testing.T, c *Config, ws []Warning) {
				if c.TelegramChatID != 987654321 {
					t.Errorf("chat id = %d, want 987654321", c.TelegramChatID)
				}
				if got := len(c.Providers); got != 3 {
					t.Errorf("providers = %d, want 3", got)
				}
				// sonnet-5 (declared) survives in mid; codex-mini (undeclared)
				// is pruned.
				if !containsID(c.Tiers["mid"], "sonnet-5") {
					t.Errorf("mid tier lost sonnet-5: %v", c.Tiers["mid"])
				}
				if containsID(c.Tiers["mid"], "codex-mini") {
					t.Errorf("mid tier kept undeclared codex-mini: %v", c.Tiers["mid"])
				}
				// Every tier-backed required role resolves to >=1 model. build
				// uses a policy and bot pins codex:best, so both are satisfiable
				// without a tier lookup.
				for _, role := range requiredRoles {
					if got := c.ModelsForRole(role); len(got) == 0 && role != core.RoleBuild && role != core.RoleBot {
						t.Errorf("role %q resolved to no models", role)
					}
				}
				// budget/update/caps decoded.
				if c.Budget.ConfirmOverUSD != 2.00 {
					t.Errorf("confirm_over_usd = %v, want 2.00", c.Budget.ConfirmOverUSD)
				}
				if c.Update.Auto != "patch" {
					t.Errorf("update.auto = %q, want patch", c.Update.Auto)
				}
				if c.Caps["claude"].MaxConcurrent != 2 {
					t.Errorf("caps.claude = %d, want 2", c.Caps["claude"].MaxConcurrent)
				}
			},
		},
		{
			name:          "single provider config validates",
			file:          "single_provider.toml",
			wantWarnKinds: nil,
			check: func(t *testing.T, c *Config, ws []Warning) {
				if len(c.Providers) != 1 {
					t.Fatalf("want exactly one provider, got %d", len(c.Providers))
				}
				for _, role := range requiredRoles {
					if len(c.ModelsForRole(role)) == 0 {
						t.Errorf("single-provider: role %q has no model", role)
					}
				}
			},
		},
		{
			name:          "local only config validates",
			file:          "local_only.toml",
			wantWarnKinds: nil,
			check: func(t *testing.T, c *Config, ws []Warning) {
				for _, m := range c.CoreModels() {
					if m.Billing != core.BillingFree {
						t.Errorf("model %q billing = %q, want free", m.ID, m.Billing)
					}
				}
				for _, role := range requiredRoles {
					if len(c.ModelsForRole(role)) == 0 {
						t.Errorf("local-only: role %q has no model", role)
					}
				}
			},
		},
		{
			name:          "deleted provider drops orphan models and dangling tier entries",
			file:          "deleted_provider.toml",
			wantWarnKinds: []WarningKind{WarnDanglingTierEntry, WarnOrphanModel},
			check: func(t *testing.T, c *Config, ws []Warning) {
				if _, ok := c.Models["gpt-5-5"]; ok {
					t.Errorf("orphan model gpt-5-5 was not dropped")
				}
				if containsID(c.Tiers["top"], "gpt-5-5") {
					t.Errorf("top tier still lists dropped gpt-5-5: %v", c.Tiers["top"])
				}
				if !containsID(c.Tiers["top"], "opus-4-8") {
					t.Errorf("top tier lost the surviving opus-4-8: %v", c.Tiers["top"])
				}
				// mid referenced only codex-mini (undeclared) -> emptied, but no
				// role points at mid, so no empty-role warning here.
				if len(c.Tiers["mid"]) != 0 {
					t.Errorf("mid tier should be empty after pruning, got %v", c.Tiers["mid"])
				}
				if hasKind(ws, WarnEmptyRole) {
					t.Errorf("did not expect an empty-role warning: %v", ws)
				}
			},
		},
		{
			name:          "role with no model warns but does not fail",
			file:          "role_no_model.toml",
			wantWarnKinds: []WarningKind{WarnDanglingTierEntry, WarnEmptyRole},
			check: func(t *testing.T, c *Config, ws []Warning) {
				if !hasKind(ws, WarnEmptyRole) {
					t.Fatalf("expected an empty-role warning, got %v", ws)
				}
				// The empty-role warning must name the lint role specifically.
				var found bool
				for _, w := range ws {
					if w.Kind == WarnEmptyRole && strings.Contains(w.Message, "lint") {
						found = true
					}
				}
				if !found {
					t.Errorf("empty-role warning did not name lint: %v", ws)
				}
			},
		},
		{
			name:          "unknown keys warn but do not fail",
			file:          "unknown_keys.toml",
			wantWarnKinds: []WarningKind{WarnUnknownKey},
			check: func(t *testing.T, c *Config, ws []Warning) {
				if !hasKind(ws, WarnUnknownKey) {
					t.Fatalf("expected unknown-key warnings, got %v", ws)
				}
				// The known parts still decoded.
				if c.Providers["claude"].Kind != "claude-cli" {
					t.Errorf("claude provider did not decode: %+v", c.Providers["claude"])
				}
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c, ws, err := LoadGlobal(testdata(t, tc.file))
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got nil (warnings %v)", ws)
				}
				return
			}
			if err != nil {
				t.Fatalf("LoadGlobal(%s): %v", tc.file, err)
			}
			if tc.wantWarnKinds != nil {
				got := warnKinds(ws)
				if !equalKinds(got, tc.wantWarnKinds) {
					t.Errorf("warning kinds = %v, want %v\nfull warnings: %v", got, tc.wantWarnKinds, ws)
				}
			}
			if tc.check != nil {
				tc.check(t, c, ws)
			}
		})
	}
}

// TestSpecExampleParsesIntoTypes asserts the acceptance criterion that the spec's
// example TOML parses into typed structs that reuse core types.
func TestSpecExampleParsesIntoTypes(t *testing.T) {
	c, _, err := LoadGlobal(testdata(t, "full.toml"))
	if err != nil {
		t.Fatal(err)
	}
	// [models] fable-5 is metered per the spec comment.
	fable, ok := c.Models["fable-5"]
	if !ok {
		t.Fatal("fable-5 missing from parsed models")
	}
	if fable.Billing != core.BillingMetered {
		t.Errorf("fable-5 billing = %q, want %q", fable.Billing, core.BillingMetered)
	}
	// core.TierMap is reused for [tiers].
	var _ core.TierMap = c.Tiers
	if got := c.Tiers["top"]; !containsID(got, "opus-4-8") {
		t.Errorf("top tier = %v, want to contain opus-4-8", got)
	}
	// [routing.plan] carries effort=max.
	if c.Routing["plan"].Effort != "max" {
		t.Errorf("routing.plan effort = %q, want max", c.Routing["plan"].Effort)
	}
	// [routing.bot] pins the codex:best shorthand with fast=true.
	if c.Routing["bot"].Model != "codex:best" || !c.Routing["bot"].Fast {
		t.Errorf("routing.bot = %+v, want model=codex:best fast=true", c.Routing["bot"])
	}
	// CoreModels round-trips ids and billing into core.Model.
	opus := findModel(c.CoreModels(), "opus-4-8")
	if opus.Billing != core.BillingSubscription {
		t.Errorf("opus-4-8 billing = %q, want subscription", opus.Billing)
	}
}

// TestPerRepoOverlay covers the merge semantics: per-repo overlays global,
// per-key, shallow.
func TestPerRepoOverlay(t *testing.T) {
	dir := t.TempDir()
	global := filepath.Join(dir, "global.toml")
	repo := filepath.Join(dir, "repo.toml")
	copyFixture(t, "full.toml", global)
	copyFixture(t, "repo_overlay.toml", repo)

	c, ws, err := Load(global, repo)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	// head_branch overridden by the repo file.
	if c.HeadBranch != "develop" {
		t.Errorf("head_branch = %q, want develop (from repo overlay)", c.HeadBranch)
	}
	// verification came only from the repo file.
	if !strings.Contains(c.Verification, "golangci-lint") {
		t.Errorf("verification = %q, want repo's command", c.Verification)
	}
	// skills came from the repo file.
	if !containsID(c.Skills, "go-review") {
		t.Errorf("skills = %v, want to contain go-review", c.Skills)
	}
	// Global-only values (telegram token) survive the overlay.
	if c.TelegramToken == "" {
		t.Errorf("telegram token lost during overlay")
	}
	// routing.plan replaced wholesale (shallow): repo set tier=mid with no
	// effort, so the global effort=max must be gone.
	plan := c.Routing["plan"]
	if plan.Tier != "mid" {
		t.Errorf("routing.plan tier = %q, want mid (repo overlay)", plan.Tier)
	}
	if plan.Effort != "" {
		t.Errorf("routing.plan effort = %q, want empty (shallow replace dropped global effort=max)", plan.Effort)
	}
	// Other routing roles from global survive (map merge is per-key).
	if c.Routing["review"].Tier != "top" {
		t.Errorf("routing.review lost during overlay: %+v", c.Routing["review"])
	}
	// The only warning is the codex-mini dangling entry inherited from the
	// global full.toml fixture; the overlay itself introduces none.
	if k := warnKinds(ws); !equalKinds(k, []WarningKind{WarnDanglingTierEntry}) {
		t.Errorf("overlay warnings = %v, want just the inherited dangling-tier-entry: %v", k, ws)
	}
}

// TestLoadMissingRepoIsNotError: a missing per-repo file is fine; a missing
// global file is an error.
func TestLoadMissingRepoIsNotError(t *testing.T) {
	dir := t.TempDir()
	global := filepath.Join(dir, "global.toml")
	copyFixture(t, "full.toml", global)

	if _, _, err := Load(global, filepath.Join(dir, "does-not-exist.toml")); err != nil {
		t.Errorf("missing per-repo file should not error: %v", err)
	}
	if _, _, err := Load(filepath.Join(dir, "nope.toml"), ""); err == nil {
		t.Errorf("missing global file should error")
	}
}

// TestMalformedTOMLIsError: bad TOML is a hard error, not a warning.
func TestMalformedTOMLIsError(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "bad.toml")
	if err := os.WriteFile(p, []byte("this is = = not valid toml ["), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := LoadGlobal(p); err == nil {
		t.Errorf("expected error for malformed TOML")
	}
}

// TestDefault covers the acceptance criterion: Default() returns a runnable
// config from just a token + one provider, and it passes Validate with no
// empty-role warnings.
func TestDefault(t *testing.T) {
	c := Default("123456:token", 42, "claude", "claude-cli", "opus-4-8")

	if c.TelegramToken != "123456:token" || c.TelegramChatID != 42 {
		t.Errorf("telegram creds not set: %+v", c)
	}
	if c.HeadBranch != defaultHeadBranch {
		t.Errorf("head_branch = %q, want %q", c.HeadBranch, defaultHeadBranch)
	}
	if c.WorktreeRoot == "" {
		t.Errorf("worktree_root should be defaulted")
	}

	ws := c.Validate()
	if len(ws) != 0 {
		t.Fatalf("Default() config should validate clean, got warnings: %v", ws)
	}
	for _, role := range requiredRoles {
		got := c.ModelsForRole(role)
		if len(got) != 1 || got[0].ID != "opus-4-8" {
			t.Errorf("role %q -> %v, want [opus-4-8]", role, got)
		}
	}
	// The single model is a subscription model.
	if c.Models["opus-4-8"].Billing != core.BillingSubscription {
		t.Errorf("default model billing = %q, want subscription", c.Models["opus-4-8"].Billing)
	}
}

// --- small helpers ---

func containsID(ids []string, want string) bool {
	for _, id := range ids {
		if id == want {
			return true
		}
	}
	return false
}

func equalKinds(a, b []WarningKind) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func findModel(ms []core.Model, id string) core.Model {
	for _, m := range ms {
		if m.ID == id {
			return m
		}
	}
	return core.Model{}
}

func copyFixture(t *testing.T, name, dst string) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	if err := os.WriteFile(dst, b, 0o600); err != nil {
		t.Fatalf("write fixture to %s: %v", dst, err)
	}
}
