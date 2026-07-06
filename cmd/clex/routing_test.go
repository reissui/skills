package main

import (
	"path/filepath"
	"testing"

	"github.com/BurntSushi/toml"
	"github.com/reissui/clex/internal/config"
	"github.com/reissui/clex/internal/core"
)

// --- AC: with both claude and codex installed, the scaffold splits roles —
// Claude plans/reviews/chats (top tier), codex (GPT) builds ---

func TestScaffoldDualProviderSplitsRoles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	// missing = nil: everything installed.
	if err := writeConfigScaffold(path, "tok", 42, nil); err != nil {
		t.Fatalf("writeConfigScaffold: %v", err)
	}
	var cfg config.Config
	if _, err := toml.DecodeFile(path, &cfg); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if _, ok := cfg.Providers["claude"]; !ok {
		t.Fatal("claude provider missing")
	}
	if _, ok := cfg.Providers["codex"]; !ok {
		t.Fatal("codex provider missing")
	}
	for role, wantTier := range map[core.Role]string{
		core.RolePlan:   "top",
		core.RoleReview: "top",
		core.RoleBot:    "top",
		core.RoleBuild:  "build",
	} {
		if got := cfg.Routing[string(role)].Tier; got != wantTier {
			t.Errorf("routing.%s tier = %q, want %q", role, got, wantTier)
		}
	}
	if got := cfg.Tiers["top"]; len(got) != 1 || cfg.Models[got[0]].Provider != "claude" {
		t.Errorf("top tier = %v, want a claude model (Claude is the planner)", got)
	}
	if got := cfg.Tiers["build"]; len(got) != 1 || cfg.Models[got[0]].Provider != "codex" {
		t.Errorf("build tier = %v, want a codex model (GPT is the developer)", got)
	}
	// The result must validate cleanly — the wizard writes runnable config.
	if warns := cfg.Validate(); len(warns) != 0 {
		t.Errorf("dual-provider scaffold has validation warnings: %v", warns)
	}
}

// --- AC: single-provider installs keep the Default shape ---

func TestScaffoldSingleProviderKeepsDefault(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := writeConfigScaffold(path, "tok", 42, []string{"codex"}); err != nil {
		t.Fatalf("writeConfigScaffold: %v", err)
	}
	var cfg config.Config
	if _, err := toml.DecodeFile(path, &cfg); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if _, ok := cfg.Providers["codex"]; ok {
		t.Error("codex provider written despite being missing")
	}
	if got := cfg.Routing[string(core.RoleBuild)].Tier; got != "default" {
		t.Errorf("single-provider build tier = %q, want %q", got, "default")
	}
}
