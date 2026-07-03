package registry

import (
	"testing"

	"github.com/reissui/clex/internal/core"
)

// TestEscalate_WalksLadderAndStops verifies the ladder climbs local → mid → top
// and returns false at the top.
func TestEscalate_WalksLadderAndStops(t *testing.T) {
	cfg := threeProviderConfig()
	reg := New(cfg, healthyRunners("claude", "codex", "ollama"), WithHistory(newFakeHistory()))

	// local → mid
	local := core.Model{ID: "qwen3-coder", Provider: "ollama"}
	mid, ok := reg.Escalate(local)
	if !ok {
		t.Fatalf("escalating from local returned false, want a mid model")
	}
	if r := rankOf(reg.tierOf(mid.ID)); r != rankOf(tierMid) {
		t.Fatalf("escalated from local to tier rank %d (%s), want mid", r, reg.tierOf(mid.ID))
	}

	// mid → top
	top, ok := reg.Escalate(mid)
	if !ok {
		t.Fatalf("escalating from mid returned false, want a top model")
	}
	if r := rankOf(reg.tierOf(top.ID)); r != rankOf(tierTop) {
		t.Fatalf("escalated from mid to tier rank %d (%s), want top", r, reg.tierOf(top.ID))
	}

	// top → stop
	if _, ok := reg.Escalate(top); ok {
		t.Fatalf("escalating from top returned true, want false (nowhere to climb)")
	}
}

// TestEscalate_PrefersProviderDiversity: escalating a claude mid model should
// prefer a non-claude top model when one exists, to escape provider-specific
// failure modes.
func TestEscalate_PrefersProviderDiversity(t *testing.T) {
	cfg := threeProviderConfig()
	// top tier is [opus-4-8(claude), gpt-5-5(codex), fable-5(claude)].
	reg := New(cfg, healthyRunners("claude", "codex", "ollama"), WithHistory(newFakeHistory()))

	sonnet := core.Model{ID: "sonnet-5", Provider: "claude"}
	got, ok := reg.Escalate(sonnet)
	if !ok {
		t.Fatalf("escalate from mid returned false")
	}
	if got.Provider == "claude" {
		t.Fatalf("escalation from a claude model chose another claude model %q; want provider diversity (gpt-5-5)", got.ID)
	}
	if got.ID != "gpt-5-5" {
		t.Errorf("escalation chose %q, want gpt-5-5 (the different-provider top model)", got.ID)
	}
}

// TestEscalate_SkipsEmptyTier: with no mid tier, escalating from local should
// climb straight to top rather than stopping.
func TestEscalate_SkipsEmptyTier(t *testing.T) {
	cfg := threeProviderConfig()
	cfg.Tiers["mid"] = nil
	reg := New(cfg, healthyRunners("claude", "codex", "ollama"), WithHistory(newFakeHistory()))

	local := core.Model{ID: "qwen3-coder", Provider: "ollama"}
	got, ok := reg.Escalate(local)
	if !ok {
		t.Fatalf("escalate from local with empty mid returned false, want top")
	}
	if r := rankOf(reg.tierOf(got.ID)); r != rankOf(tierTop) {
		t.Fatalf("escalated to rank %d, want top (should skip empty mid)", r)
	}
}

// TestEscalate_UntieredModelStops: a model on no known tier has nowhere defined
// to climb and returns false.
func TestEscalate_UntieredModelStops(t *testing.T) {
	cfg := threeProviderConfig()
	reg := New(cfg, healthyRunners("claude", "codex", "ollama"), WithHistory(newFakeHistory()))
	orphan := core.Model{ID: "mystery", Provider: "claude"}
	if _, ok := reg.Escalate(orphan); ok {
		t.Fatalf("escalating an untiered model returned true, want false")
	}
}
