package daemon

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/reissui/clex/internal/config"
	"github.com/reissui/clex/internal/core"
	"github.com/reissui/clex/internal/gh"
	"github.com/reissui/clex/internal/registry"
	"github.com/reissui/clex/internal/store"
	"github.com/reissui/clex/internal/telegram"
)

// meteredHarness builds a daemon whose sole build model is metered, with a low
// confirm threshold so any dispatch trips GateConfirm and routes to Telegram
// Ask. A very low epic cap lets a separate test trip GateBlock.
func meteredHarness(t *testing.T, confirmOver, maxEpic float64) *harness {
	t.Helper()
	home := t.TempDir()
	if _, err := EnsureHome(home); err != nil {
		t.Fatalf("EnsureHome: %v", err)
	}
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	cfg := &config.Config{
		TelegramToken:  "token-xyz",
		TelegramChatID: 1,
		Verification:   "go test ./...",
		Providers:      map[string]config.Provider{"fake": {Kind: "codex-cli"}},
		Models: map[string]config.Model{
			"metered-model": {Provider: "fake", Billing: core.BillingMetered},
		},
		Tiers: core.TierMap{"mid": {"metered-model"}, "top": {"metered-model"}},
		Routing: map[string]config.Routing{
			string(core.RoleBuild):  {Policy: "auto"},
			string(core.RolePlan):   {Tier: "top"},
			string(core.RoleReview): {Tier: "top"},
			string(core.RoleLint):   {Tier: "mid"},
			string(core.RoleBot):    {Tier: "mid"},
		},
		Budget: config.Budget{ConfirmOverUSD: confirmOver, MaxUSDPerEpic: maxEpic},
	}
	runner := &fakeRunner{}
	rf := &fakeFactory{runner: runner}
	reg := registry.New(cfg, map[string]core.Runner{"fake": runner})

	fg := newFakeGH(testRepo)
	ftg := newFakeTG()
	stages := newFakeStages()

	d, err := New(Deps{
		GH: fg, TG: ftg, Stages: stages, Registry: reg, Store: st, RunnerFactory: rf,
	}, Config{
		Repo: testRepo, Home: home, Owner: "acme", SelfLogin: "clex-bot",
		PollInterval: 20 * time.Millisecond, MaxParallel: 4,
		DefaultVerify: "go test ./...", MaxUSDPerEpic: maxEpic,
	}, slog.New(slog.NewTextHandler(&discardWriter{}, nil)), NewRedactor("token-xyz"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	h := &harness{d: d, gh: fg, tg: ftg, st: st, rf: rf, cfg: cfg}
	// stash stages for assertions via a field-free closure isn't possible; expose
	// through the daemon's deps in the test that needs it.
	t.Cleanup(func() { _ = stages })
	return h
}

// --- Criterion: Confirm decisions route to Telegram Ask before dispatch.
func TestCostGateConfirmAsksTelegram(t *testing.T) {
	h := meteredHarness(t, 0.01 /*confirm over 1c*/, 0 /*no epic cap*/)

	// Owner will approve when asked.
	h.tg.queueAnswer(telegram.Answer{Text: "proceed"})

	h.gh.seed(&gh.Issue{
		Number: 90, Title: "metered", AuthorLogin: "acme",
		State: core.StateApproved,
		Meta:  gh.Metadata{Touches: []string{"m/**"}, Difficulty: core.DifficultyStandard},
	})

	// Directly exercise the gate (deterministic) with the winning model.
	dec := h.d.deps.Registry.Build(core.DifficultyStandard, registry.BuildOptions{AllowMetered: true})
	if !dec.Ok {
		t.Fatalf("expected a build model; warnings=%+v", dec.Warnings)
	}
	proceed := h.d.passCostGate(context.Background(), 90, dec.Winner.Option.Model, "build")
	if !proceed {
		t.Fatal("gate should proceed after owner confirms")
	}
	if h.tg.askCalls != 1 {
		t.Fatalf("expected exactly 1 Telegram Ask, got %d", h.tg.askCalls)
	}
}

// TestCostGateConfirmSkipHolds proves a skipped confirm holds the dispatch.
func TestCostGateConfirmSkipHolds(t *testing.T) {
	h := meteredHarness(t, 0.01, 0)
	h.tg.queueAnswer(telegram.Answer{Skipped: true})

	dec := h.d.deps.Registry.Build(core.DifficultyStandard, registry.BuildOptions{AllowMetered: true})
	if !dec.Ok {
		t.Fatalf("no build model; warnings=%+v", dec.Warnings)
	}
	if h.d.passCostGate(context.Background(), 91, dec.Winner.Option.Model, "build") {
		t.Fatal("gate should hold when the owner skips the confirm")
	}
}

// --- Criterion: Block pauses the epic.
func TestCostGateBlockPausesEpic(t *testing.T) {
	// Epic cap of 1 cent guarantees any real estimate exceeds it → GateBlock.
	h := meteredHarness(t, 0.01, 0.01)

	dec := h.d.deps.Registry.Build(core.DifficultyStandard, registry.BuildOptions{AllowMetered: true})
	if !dec.Ok {
		t.Fatalf("no build model; warnings=%+v", dec.Warnings)
	}
	if h.d.passCostGate(context.Background(), 92, dec.Winner.Option.Model, "build") {
		t.Fatal("gate should block when the epic cap is already exceeded")
	}
	if !h.d.isPaused() {
		t.Fatal("epic should be paused after a GateBlock")
	}
}
