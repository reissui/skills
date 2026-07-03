package registry

import (
	"context"
	"time"

	"github.com/reissui/clex/internal/config"
	"github.com/reissui/clex/internal/core"
)

// fakeRunner is a test double for core.Runner. It never spawns a process; Run is
// unused by the registry (the registry only calls Probe), and Probe returns a
// scripted Availability, optionally after asserting the context is live.
type fakeRunner struct {
	av       core.Availability
	probeErr error
	probes   int // how many times Probe was called
}

func (f *fakeRunner) Run(context.Context, core.Task, string) (<-chan core.Event, error) {
	// The registry never calls Run; return a closed channel to satisfy the
	// interface if some future caller does.
	ch := make(chan core.Event)
	close(ch)
	return ch, nil
}

func (f *fakeRunner) Probe(ctx context.Context) (core.Availability, error) {
	f.probes++
	if f.probeErr != nil {
		return core.Availability{}, f.probeErr
	}
	return f.av, nil
}

// fakeHistory is a deterministic History for tests. Missing keys return the zero
// value with no error, exercising cold-start paths.
type fakeHistory struct {
	success  map[successKey]float64        // (model,difficulty) → rate
	duration map[durationKey]time.Duration // (model,stage) → avg
	spend    map[string]float64            // model → total spend since any t ("" = all)
	err      error                         // if set, every call returns it
}

type successKey struct {
	model string
	diff  core.Difficulty
}
type durationKey struct {
	model string
	stage string
}

func newFakeHistory() *fakeHistory {
	return &fakeHistory{
		success:  map[successKey]float64{},
		duration: map[durationKey]time.Duration{},
		spend:    map[string]float64{},
	}
}

func (h *fakeHistory) SuccessRate(model string, d core.Difficulty) (float64, error) {
	if h.err != nil {
		return 0, h.err
	}
	return h.success[successKey{model, d}], nil
}

func (h *fakeHistory) AvgDuration(model, stage string) (time.Duration, error) {
	if h.err != nil {
		return 0, h.err
	}
	return h.duration[durationKey{model, stage}], nil
}

func (h *fakeHistory) SpendSince(_ time.Time, model string) (float64, error) {
	if h.err != nil {
		return 0, h.err
	}
	return h.spend[model], nil
}

// fixedClock returns a clock function pinned to t.
func fixedClock(t time.Time) func() time.Time {
	return func() time.Time { return t }
}

// threeProviderConfig is the canonical spec config: claude/codex/ollama
// providers, five models across top/mid/local tiers, build=auto policy.
func threeProviderConfig() *config.Config {
	return &config.Config{
		Providers: map[string]config.Provider{
			"claude": {Kind: "claude-cli"},
			"codex":  {Kind: "codex-cli"},
			"ollama": {Kind: "ollama"},
		},
		Models: map[string]config.Model{
			"fable-5":     {Provider: "claude", Billing: core.BillingMetered},
			"opus-4-8":    {Provider: "claude", Billing: core.BillingSubscription},
			"gpt-5-5":     {Provider: "codex", Billing: core.BillingSubscription},
			"sonnet-5":    {Provider: "claude", Billing: core.BillingSubscription},
			"codex-mini":  {Provider: "codex", Billing: core.BillingSubscription},
			"qwen3-coder": {Provider: "ollama", Billing: core.BillingFree},
		},
		Tiers: core.TierMap{
			"top":   {"opus-4-8", "gpt-5-5", "fable-5"},
			"mid":   {"sonnet-5", "codex-mini"},
			"local": {"qwen3-coder"},
		},
		Routing: map[string]config.Routing{
			string(core.RolePlan):   {Tier: "top", Effort: "max"},
			string(core.RoleBuild):  {Policy: "auto"},
			string(core.RoleReview): {Tier: "top"},
			string(core.RoleLint):   {Tier: "mid"},
			string(core.RoleBot):    {Model: "codex:best", Fast: true},
		},
		Budget: config.Budget{ConfirmOverUSD: 2.00, MaxUSDPerEpic: 25.00},
	}
}

// singleProviderConfig has only Ollama with one free local model, every role
// pointed at the "local" tier — the degradation scenario.
func singleProviderConfig() *config.Config {
	return &config.Config{
		Providers: map[string]config.Provider{
			"ollama": {Kind: "ollama"},
		},
		Models: map[string]config.Model{
			"qwen3-coder": {Provider: "ollama", Billing: core.BillingFree},
		},
		Tiers: core.TierMap{
			"local": {"qwen3-coder"},
		},
		Routing: map[string]config.Routing{
			string(core.RolePlan):   {Tier: "local"},
			string(core.RoleBuild):  {Policy: "auto"},
			string(core.RoleReview): {Tier: "local"},
			string(core.RoleLint):   {Tier: "local"},
			string(core.RoleBot):    {Tier: "local"},
		},
	}
}

// runnersFor builds a runner map with every provider healthy by default.
func healthyRunners(names ...string) map[string]core.Runner {
	m := map[string]core.Runner{}
	for _, n := range names {
		m[n] = &fakeRunner{av: core.Availability{Healthy: true}}
	}
	return m
}
