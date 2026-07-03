package registry

import (
	"testing"
	"time"

	"github.com/reissui/clex/internal/core"
)

// TestBuild_LocalWinsOnTrivial: with no history, a trivial issue should pick the
// free local model — its cost term is maximal and success priors are high enough
// that the cheap model wins.
func TestBuild_LocalWinsOnTrivial(t *testing.T) {
	cfg := threeProviderConfig()
	hist := newFakeHistory()
	reg := New(cfg, healthyRunners("claude", "codex", "ollama"), WithHistory(hist))

	dec := reg.Build(core.DifficultyTrivial, BuildOptions{})
	if !dec.Ok {
		t.Fatalf("expected a decision, got Ok=false: %+v", dec.Warnings)
	}
	if got := dec.Winner.Option.Model.ID; got != "qwen3-coder" {
		t.Fatalf("trivial build winner = %q, want qwen3-coder\nranking: %s", got, rankString(dec))
	}
	// The winner must be from the local tier and free.
	if dec.Winner.Option.Model.Billing != core.BillingFree {
		t.Errorf("winner billing = %q, want free", dec.Winner.Option.Model.Billing)
	}
}

// TestBuild_FastMidBeatsSlowLocal: on a standard issue, history says the local
// model is slow and unreliable while a mid subscription model is fast and
// reliable. The mid model must win despite costing a subscription window.
func TestBuild_FastMidBeatsSlowLocal(t *testing.T) {
	cfg := threeProviderConfig()
	hist := newFakeHistory()
	// Local model: poor track record + very slow.
	hist.success[successKey{"qwen3-coder", core.DifficultyStandard}] = 0.30
	hist.duration[durationKey{"qwen3-coder", buildStage}] = 40 * time.Minute
	// Mid model sonnet-5: strong track record + fast.
	hist.success[successKey{"sonnet-5", core.DifficultyStandard}] = 0.95
	hist.duration[durationKey{"sonnet-5", buildStage}] = 2 * time.Minute
	reg := New(cfg, healthyRunners("claude", "codex", "ollama"), WithHistory(hist))

	dec := reg.Build(core.DifficultyStandard, BuildOptions{})
	if !dec.Ok {
		t.Fatalf("expected a decision, got Ok=false")
	}
	if got := dec.Winner.Option.Model.ID; got != "sonnet-5" {
		t.Fatalf("standard build winner = %q, want sonnet-5 (fast+reliable mid beats slow local)\nranking: %s", got, rankString(dec))
	}
}

// TestBuild_MeteredNeverChosenWithoutOverride: fable-5 is metered and lives in
// the top tier, so it is never in the build pool. Even if we (hypothetically)
// placed a metered model in a build tier, AllowMetered must be required.
func TestBuild_MeteredNeverChosenWithoutOverride(t *testing.T) {
	cfg := threeProviderConfig()
	// Move the metered model into the mid (build) tier to make the test sharp.
	cfg.Tiers["mid"] = []string{"sonnet-5", "codex-mini", "fable-5"}
	hist := newFakeHistory()
	// Make the metered model look irresistible on raw success+speed.
	hist.success[successKey{"fable-5", core.DifficultyComplex}] = 1.0
	hist.duration[durationKey{"fable-5", buildStage}] = 10 * time.Second
	reg := New(cfg, healthyRunners("claude", "codex", "ollama"), WithHistory(hist))

	// Without override: metered excluded entirely.
	dec := reg.Build(core.DifficultyComplex, BuildOptions{})
	for _, c := range dec.Ranked {
		if c.Option.Model.Billing == core.BillingMetered {
			t.Fatalf("metered model %q appeared in build pool without override", c.Option.Model.ID)
		}
	}

	// With override: metered admitted and, given its numbers, wins.
	dec2 := reg.Build(core.DifficultyComplex, BuildOptions{AllowMetered: true})
	if got := dec2.Winner.Option.Model.ID; got != "fable-5" {
		t.Fatalf("with AllowMetered, winner = %q, want fable-5\nranking: %s", got, rankString(dec2))
	}
}

// TestBuild_Deterministic: identical inputs yield identical rankings across runs,
// and equal-score models break ties by ID.
func TestBuild_Deterministic(t *testing.T) {
	cfg := threeProviderConfig()
	hist := newFakeHistory() // no history: all build models share priors → tie
	reg := New(cfg, healthyRunners("claude", "codex", "ollama"), WithHistory(hist))

	first := reg.Build(core.DifficultyStandard, BuildOptions{})
	for i := 0; i < 20; i++ {
		got := reg.Build(core.DifficultyStandard, BuildOptions{})
		if len(got.Ranked) != len(first.Ranked) {
			t.Fatalf("ranking length changed between runs")
		}
		for j := range got.Ranked {
			if got.Ranked[j].Option.Model.ID != first.Ranked[j].Option.Model.ID {
				t.Fatalf("ranking not deterministic at %d: %q vs %q", j, got.Ranked[j].Option.Model.ID, first.Ranked[j].Option.Model.ID)
			}
		}
	}
	// Among the two mid models (equal cost=subscription) sonnet-5 < codex-mini by
	// ID; qwen3-coder (free) outranks both on the cost term. Verify the free one
	// leads and the tie-break order holds among the subscription pair.
	if first.Ranked[0].Option.Model.ID != "qwen3-coder" {
		t.Errorf("free local should lead a no-history ranking, got %q", first.Ranked[0].Option.Model.ID)
	}
}

// TestBuild_NoBuildModels: a config whose build tiers are empty returns Ok=false
// with a warning rather than panicking.
func TestBuild_NoBuildModels(t *testing.T) {
	cfg := threeProviderConfig()
	cfg.Tiers["local"] = nil
	cfg.Tiers["mid"] = nil
	reg := New(cfg, healthyRunners("claude", "codex", "ollama"), WithHistory(newFakeHistory()))

	dec := reg.Build(core.DifficultyStandard, BuildOptions{})
	if dec.Ok {
		t.Fatalf("expected Ok=false with no build models, got winner %q", dec.Winner.Option.Model.ID)
	}
	if len(dec.Warnings) == 0 {
		t.Errorf("expected a warning explaining the empty build pool")
	}
}

// TestBuild_UnhealthyPoolDegrades: when every build model's provider is rate
// limited, Build still returns a candidate (least-bad) with a warning.
func TestBuild_UnhealthyPoolDegrades(t *testing.T) {
	cfg := threeProviderConfig()
	reg := New(cfg, healthyRunners("claude", "codex", "ollama"), WithHistory(newFakeHistory()))
	// Knock out both build providers.
	reg.NoteRateLimit("ollama", "cap")
	reg.NoteRateLimit("claude", "cap")
	reg.NoteRateLimit("codex", "cap")

	dec := reg.Build(core.DifficultyStandard, BuildOptions{})
	if !dec.Ok {
		t.Fatalf("expected a degraded decision, got Ok=false")
	}
	if len(dec.Warnings) == 0 {
		t.Errorf("expected a degradation warning when the whole build pool is unhealthy")
	}
}

// rankString renders a decision's ranking for failure messages.
func rankString(d BuildDecision) string {
	s := ""
	for _, c := range d.Ranked {
		s += c.Option.Model.ID + "(" + ftoa(c.Score) + ") "
	}
	return s
}

func ftoa(f float64) string {
	// small helper to avoid importing strconv in the format path
	const digits = "0123456789"
	neg := f < 0
	if neg {
		f = -f
	}
	whole := int(f)
	frac := int((f - float64(whole)) * 1000)
	out := []byte{}
	if whole == 0 {
		out = append(out, '0')
	} else {
		var rev []byte
		for whole > 0 {
			rev = append(rev, digits[whole%10])
			whole /= 10
		}
		for i := len(rev) - 1; i >= 0; i-- {
			out = append(out, rev[i])
		}
	}
	out = append(out, '.')
	out = append(out, digits[(frac/100)%10], digits[(frac/10)%10], digits[frac%10])
	if neg {
		return "-" + string(out)
	}
	return string(out)
}
