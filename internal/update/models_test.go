package update

import (
	"context"
	"errors"
	"testing"

	"github.com/reissui/clex/internal/config"
	"github.com/reissui/clex/internal/core"
)

// baseConfig is the canonical three-provider config used by the model-diff
// tests: claude (subscription) + ollama (autodetect, free local).
func baseConfig() *config.Config {
	return &config.Config{
		Providers: map[string]config.Provider{
			"claude": {Kind: "claude-cli"},
			"ollama": {Kind: "ollama", Autodetect: true},
		},
		Models: map[string]config.Model{
			"sonnet-5":    {Provider: "claude", Billing: core.BillingSubscription},
			"opus-4-8":    {Provider: "claude", Billing: core.BillingSubscription},
			"qwen3-coder": {Provider: "ollama", Billing: core.BillingFree},
		},
		Tiers: core.TierMap{
			"top":   {"opus-4-8"},
			"mid":   {"sonnet-5"},
			"local": {"qwen3-coder"},
		},
	}
}

// fakeReprober returns fixture discovered models and records the re-probe.
type fakeReprober struct {
	discovered DiscoveredModels
	err        error
	calls      int
}

func (f *fakeReprober) Reprobe(context.Context) (DiscoveredModels, error) {
	f.calls++
	return f.discovered, f.err
}

// findProposal returns the first proposal of kind k, or nil.
func findProposal(ps []Proposal, k ProposalKind) *Proposal {
	for i := range ps {
		if ps[i].Kind == k {
			return &ps[i]
		}
	}
	return nil
}

func TestDiffModels_added(t *testing.T) {
	cfg := baseConfig()
	// claude now reports a new model sonnet-5.1 alongside the configured ones.
	discovered := DiscoveredModels{
		"claude": {"sonnet-5", "opus-4-8", "sonnet-5.1"},
		"ollama": {"qwen3-coder"},
	}
	ps := DiffModels(cfg, discovered)
	add := findProposal(ps, KindModelAdd)
	if add == nil {
		t.Fatalf("expected a KindModelAdd proposal, got %+v", ps)
	}
	if add.Model.Model != "sonnet-5.1" {
		t.Errorf("added model = %q, want sonnet-5.1", add.Model.Model)
	}
	// sibling-tier guess: sonnet-5 lives in "mid" → new claude model guessed mid.
	if add.Model.Tier != "mid" {
		t.Errorf("guessed tier = %q, want mid (sibling of sonnet-5)", add.Model.Tier)
	}
}

func TestDiffModels_addedLocalGuessesLocalTier(t *testing.T) {
	cfg := baseConfig()
	// ollama autodetect reports a brand-new local model.
	discovered := DiscoveredModels{
		"ollama": {"qwen3-coder", "llama4-local"},
	}
	ps := DiffModels(cfg, discovered)
	add := findProposal(ps, KindModelAdd)
	if add == nil {
		t.Fatalf("expected KindModelAdd, got %+v", ps)
	}
	if add.Model.Model != "llama4-local" || add.Model.Tier != "local" {
		t.Errorf("got %+v, want llama4-local -> local", add.Model)
	}
}

func TestDiffModels_removed(t *testing.T) {
	cfg := baseConfig()
	// claude no longer reports opus-4-8 (retired), still reports sonnet-5.
	discovered := DiscoveredModels{
		"claude": {"sonnet-5"},
		"ollama": {"qwen3-coder"},
	}
	ps := DiffModels(cfg, discovered)
	rem := findProposal(ps, KindModelRemove)
	if rem == nil {
		t.Fatalf("expected KindModelRemove, got %+v", ps)
	}
	if rem.Model.Model != "opus-4-8" {
		t.Errorf("removed model = %q, want opus-4-8", rem.Model.Model)
	}
	// It must NOT be reported as an add or rename.
	if findProposal(ps, KindModelRename) != nil {
		t.Error("single removal should not be a rename")
	}
}

func TestDiffModels_renamed(t *testing.T) {
	cfg := baseConfig()
	// ollama: qwen3-coder disappears, qwen3.5-coder appears — exactly one add +
	// one remove on the same provider ⇒ a rename.
	discovered := DiscoveredModels{
		"claude": {"sonnet-5", "opus-4-8"},
		"ollama": {"qwen3.5-coder"},
	}
	ps := DiffModels(cfg, discovered)
	ren := findProposal(ps, KindModelRename)
	if ren == nil {
		t.Fatalf("expected KindModelRename, got %+v", ps)
	}
	if ren.Model.OldModel != "qwen3-coder" || ren.Model.Model != "qwen3.5-coder" {
		t.Errorf("rename = %s -> %s, want qwen3-coder -> qwen3.5-coder",
			ren.Model.OldModel, ren.Model.Model)
	}
	// A rename must not also surface as a separate add or remove.
	if findProposal(ps, KindModelAdd) != nil || findProposal(ps, KindModelRemove) != nil {
		t.Errorf("rename should consume its add+remove, got %+v", ps)
	}
}

func TestDiffModels_noChange(t *testing.T) {
	cfg := baseConfig()
	discovered := DiscoveredModels{
		"claude": {"sonnet-5", "opus-4-8"},
		"ollama": {"qwen3-coder"},
	}
	if ps := DiffModels(cfg, discovered); len(ps) != 0 {
		t.Errorf("no change should yield no proposals, got %+v", ps)
	}
}

func TestDiffModels_emptyCatalogueDoesNotFlagRemovals(t *testing.T) {
	cfg := baseConfig()
	// claude returns no catalogue at all (a subscription CLI that doesn't
	// enumerate) — its configured models must NOT be flagged missing.
	discovered := DiscoveredModels{
		"claude": {}, // no models reported
		"ollama": {"qwen3-coder"},
	}
	if ps := DiffModels(cfg, discovered); len(ps) != 0 {
		t.Errorf("empty catalogue must not produce removals, got %+v", ps)
	}
}

func TestModelDiffer_Propose_reprobesAndDiffs(t *testing.T) {
	cfg := baseConfig()
	rp := &fakeReprober{discovered: DiscoveredModels{
		"claude": {"sonnet-5", "opus-4-8", "sonnet-5.1"},
		"ollama": {"qwen3-coder"},
	}}
	d := &ModelDiffer{Reprober: rp}
	ps, err := d.Propose(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Propose: %v", err)
	}
	if rp.calls != 1 {
		t.Errorf("Reprobe calls = %d, want 1", rp.calls)
	}
	if findProposal(ps, KindModelAdd) == nil {
		t.Errorf("expected an add proposal from the re-probe, got %+v", ps)
	}
}

func TestModelDiffer_Propose_reprobeError(t *testing.T) {
	d := &ModelDiffer{Reprober: &fakeReprober{err: errors.New("probe failed")}}
	if _, err := d.Propose(context.Background(), baseConfig()); err == nil {
		t.Error("Propose should surface a re-probe error")
	}
}

func TestModelDiffer_Propose_noReprober(t *testing.T) {
	d := &ModelDiffer{}
	if _, err := d.Propose(context.Background(), baseConfig()); err == nil {
		t.Error("missing Reprober should error")
	}
}

// --- update.auto gate (off/patch) ---

func TestAutoAllowsAutoStage(t *testing.T) {
	tests := []struct {
		auto      string
		wantStage bool
	}{
		{"patch", true},
		{"off", false},
		{"", false},
		{"unknown", false},
	}
	for _, tt := range tests {
		t.Run(tt.auto, func(t *testing.T) {
			if got := AutoAllowsAutoStage(tt.auto); got != tt.wantStage {
				t.Errorf("AutoAllowsAutoStage(%q) = %v, want %v", tt.auto, got, tt.wantStage)
			}
		})
	}
}
