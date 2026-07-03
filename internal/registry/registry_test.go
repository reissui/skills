package registry

import (
	"context"
	"testing"

	"github.com/reissui/clex/internal/core"
	"github.com/reissui/clex/internal/store"
)

// storeSatisfiesHistory is a compile-time assertion that the concrete
// *store.Store satisfies the narrow History interface the registry depends on.
// If the store's method set drifts, this fails to compile — the intended
// tripwire.
var _ History = (*store.Store)(nil)

// TestAvailable_ResolvesRoleTierWithOverrides checks a tier-backed role returns
// its models with the role's effort/fast overrides applied.
func TestAvailable_ResolvesRoleTierWithOverrides(t *testing.T) {
	cfg := threeProviderConfig() // routing.plan = {tier: top, effort: max}
	reg := New(cfg, healthyRunners("claude", "codex", "ollama"), WithHistory(newFakeHistory()))

	opts, warns := reg.Available(core.RolePlan)
	if len(warns) != 0 {
		t.Fatalf("unexpected warnings resolving plan: %v", warns)
	}
	if len(opts) == 0 {
		t.Fatalf("plan role resolved to no options")
	}
	for _, o := range opts {
		if o.Effort != "max" {
			t.Errorf("plan option %q effort = %q, want max (role override)", o.Model.ID, o.Effort)
		}
	}
	// Metered fable-5 is in the top tier, so it legitimately appears for the plan
	// role (plan is not the build pool). The tier order should lead with opus-4-8.
	if opts[0].Model.ID != "opus-4-8" {
		t.Errorf("plan lead option = %q, want opus-4-8 (tier order)", opts[0].Model.ID)
	}
}

// TestAvailable_ResolvesShorthandBot checks the bot role's "codex:best" pin
// resolves to the highest-tier healthy codex model.
func TestAvailable_ResolvesShorthandBot(t *testing.T) {
	cfg := threeProviderConfig() // routing.bot = {model: "codex:best", fast: true}
	reg := New(cfg, healthyRunners("claude", "codex", "ollama"), WithHistory(newFakeHistory()))

	opts, warns := reg.Available(core.RoleBot)
	if len(opts) == 0 {
		t.Fatalf("bot role resolved to nothing; warns=%v", warns)
	}
	// codex models are gpt-5-5 (top) and codex-mini (mid); best = gpt-5-5.
	if opts[0].Model.ID != "gpt-5-5" {
		t.Errorf("codex:best resolved to %q, want gpt-5-5", opts[0].Model.ID)
	}
	if !opts[0].Fast {
		t.Errorf("bot option fast = false, want true (role override)")
	}
}

// TestHeadroom_RateLimitFlipsUnhealthyAndRoutingSkips is the headroom acceptance
// criterion: a rate-limit signal flips a provider unhealthy and routing skips
// every model on it.
func TestHeadroom_RateLimitFlipsUnhealthyAndRoutingSkips(t *testing.T) {
	cfg := threeProviderConfig()
	reg := New(cfg, healthyRunners("claude", "codex", "ollama"), WithHistory(newFakeHistory()))

	// Baseline: build pool includes the local ollama model.
	before := reg.Build(core.DifficultyTrivial, BuildOptions{})
	if before.Winner.Option.Model.ID != "qwen3-coder" {
		t.Fatalf("precondition: trivial winner = %q, want qwen3-coder", before.Winner.Option.Model.ID)
	}

	// Ollama hits its cap.
	reg.NoteRateLimit("ollama", "rate limited")
	if reg.providerHealthy("ollama") {
		t.Fatalf("ollama still healthy after NoteRateLimit")
	}

	after := reg.Build(core.DifficultyTrivial, BuildOptions{})
	for _, c := range after.Ranked {
		if c.Option.Model.Provider == "ollama" {
			t.Fatalf("routing did not skip rate-limited ollama; %q present", c.Option.Model.ID)
		}
	}
	// The winner must now be a mid subscription model (sonnet-5 or codex-mini).
	if after.Winner.Option.Model.Provider == "ollama" {
		t.Fatalf("winner is still on rate-limited provider")
	}
}

// TestProbe_UnhealthyProbeSkipsProvider: a provider whose Probe reports unhealthy
// is skipped by routing; a healthy re-probe clears it.
func TestProbe_UnhealthyProbeSkipsProvider(t *testing.T) {
	cfg := threeProviderConfig()
	ollama := &fakeRunner{av: core.Availability{Healthy: false, Detail: "ollama not running"}}
	runners := map[string]core.Runner{
		"claude": &fakeRunner{av: core.Availability{Healthy: true}},
		"codex":  &fakeRunner{av: core.Availability{Healthy: true}},
		"ollama": ollama,
	}
	reg := New(cfg, runners, WithHistory(newFakeHistory()))
	reg.Probe(context.Background())

	if reg.providerHealthy("ollama") {
		t.Fatalf("ollama healthy after an unhealthy probe")
	}
	dec := reg.Build(core.DifficultyTrivial, BuildOptions{})
	for _, c := range dec.Ranked {
		if c.Option.Model.Provider == "ollama" {
			t.Fatalf("routing included unhealthy ollama model %q", c.Option.Model.ID)
		}
	}

	// Bring ollama back and re-probe: it should recover.
	ollama.av = core.Availability{Healthy: true}
	reg.Probe(context.Background())
	if !reg.providerHealthy("ollama") {
		t.Fatalf("ollama did not recover after a healthy re-probe")
	}
}

// TestProbe_ErrorRedactsSecret ensures a probe error carrying a token is redacted
// in stored health detail (spec: secrets never logged).
func TestProbe_ErrorRedactsSecret(t *testing.T) {
	cfg := threeProviderConfig()
	runners := map[string]core.Runner{
		"claude": &fakeRunner{probeErr: errWithToken{}},
		"codex":  &fakeRunner{av: core.Availability{Healthy: true}},
		"ollama": &fakeRunner{av: core.Availability{Healthy: true}},
	}
	reg := New(cfg, runners, WithHistory(newFakeHistory()))
	reg.Probe(context.Background())

	reg.mu.RLock()
	detail := reg.health["claude"].detail
	reg.mu.RUnlock()
	if want := "[redacted]"; !contains(detail, want) {
		t.Errorf("probe error detail = %q, expected it to contain %q", detail, want)
	}
	if contains(detail, "sk-livesecret") {
		t.Errorf("probe error detail leaked the raw token: %q", detail)
	}
}

type errWithToken struct{}

func (errWithToken) Error() string {
	return "auth failed for sk-livesecret0123456789abcdef token"
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
