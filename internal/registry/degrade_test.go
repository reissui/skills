package registry

import (
	"testing"

	"github.com/reissui/clex/internal/core"
)

// TestSingleProvider_AllRolesResolve is the single-provider degradation
// acceptance criterion: with only Ollama configured (one free local model), every
// routing role still resolves to at least one run option, and no call errors.
// Roles whose config points at a tier the single model does not occupy (plan,
// review) must fall through to the local model with a warning rather than
// returning empty.
func TestSingleProvider_AllRolesResolve(t *testing.T) {
	cfg := singleProviderConfig()
	reg := New(cfg, healthyRunners("ollama"), WithHistory(newFakeHistory()))

	roles := []core.Role{core.RolePlan, core.RoleBuild, core.RoleReview, core.RoleLint, core.RoleBot}
	for _, role := range roles {
		opts, warns := reg.Available(role)
		if len(opts) == 0 {
			t.Errorf("role %q resolved to zero options in single-provider config (warns=%v)", role, warns)
			continue
		}
		// The only model is qwen3-coder; every role must land on it.
		if opts[0].Model.ID != "qwen3-coder" {
			t.Errorf("role %q resolved to %q, want qwen3-coder (the only model)", role, opts[0].Model.ID)
		}
	}

	// Build must also produce a decision on the single free model.
	dec := reg.Build(core.DifficultyStandard, BuildOptions{})
	if !dec.Ok {
		t.Fatalf("build produced no decision in single-provider config: %v", dec.Warnings)
	}
	if dec.Winner.Option.Model.ID != "qwen3-coder" {
		t.Errorf("single-provider build winner = %q, want qwen3-coder", dec.Winner.Option.Model.ID)
	}
}

// TestSingleProvider_DegradationWarns confirms that a role whose declared tier is
// unpopulated (plan → "local" but imagine plan wanted "top") surfaces a warning
// while still resolving. Here we point review at a nonexistent "top" tier to
// force the fall-through path.
func TestSingleProvider_DegradationWarns(t *testing.T) {
	cfg := singleProviderConfig()
	// Point review at a tier that has no models in this single-provider config.
	rule := cfg.Routing[string(core.RoleReview)]
	rule.Tier = "top" // no such tier populated
	cfg.Routing[string(core.RoleReview)] = rule

	reg := New(cfg, healthyRunners("ollama"), WithHistory(newFakeHistory()))
	opts, warns := reg.Available(core.RoleReview)
	if len(opts) == 0 {
		t.Fatalf("review resolved to nothing; degradation must fall through to the local model")
	}
	if opts[0].Model.ID != "qwen3-coder" {
		t.Errorf("degraded review resolved to %q, want qwen3-coder", opts[0].Model.ID)
	}
	if len(warns) == 0 {
		t.Errorf("expected a degradation warning when review's tier is empty")
	}
}

// TestNoConfig_Available returns a warning, not a panic.
func TestNoConfig_Available(t *testing.T) {
	reg := New(nil, nil)
	opts, warns := reg.Available(core.RoleBuild)
	if len(opts) != 0 {
		t.Errorf("nil-config Available returned options: %v", opts)
	}
	if len(warns) == 0 {
		t.Errorf("nil-config Available should warn")
	}
}
