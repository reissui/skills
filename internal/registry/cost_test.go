package registry

import (
	"testing"
	"time"

	"github.com/reissui/clex/internal/config"
	"github.com/reissui/clex/internal/core"
)

// TestGate covers the full cost-gate truth table.
func TestGate(t *testing.T) {
	budget := config.Budget{ConfirmOverUSD: 2.00, MaxUSDPerEpic: 25.00}
	tests := []struct {
		name     string
		billing  core.BillingMode
		estimate float64
		spent    float64
		budget   config.Budget
		want     GateDecision
	}{
		{"subscription always proceeds even if huge", core.BillingSubscription, 1000, 1000, budget, GateProceed},
		{"free always proceeds", core.BillingFree, 1000, 0, budget, GateProceed},
		{"metered under confirm threshold proceeds", core.BillingMetered, 1.50, 0, budget, GateProceed},
		{"metered over confirm threshold confirms", core.BillingMetered, 6.20, 0, budget, GateConfirm},
		{"metered at confirm threshold proceeds (strict >)", core.BillingMetered, 2.00, 0, budget, GateProceed},
		{"metered reaching epic cap blocks", core.BillingMetered, 5.00, 20.00, budget, GateBlock},
		{"metered exceeding epic cap blocks", core.BillingMetered, 10.00, 20.00, budget, GateBlock},
		{"block takes precedence over confirm", core.BillingMetered, 6.20, 24.00, budget, GateBlock},
		{"zero confirm threshold disables confirm gate", core.BillingMetered, 6.20, 0, config.Budget{ConfirmOverUSD: 0, MaxUSDPerEpic: 25}, GateProceed},
		{"zero epic cap disables block gate", core.BillingMetered, 6.20, 1000, config.Budget{ConfirmOverUSD: 2, MaxUSDPerEpic: 0}, GateConfirm},
		{"both gates disabled always proceeds", core.BillingMetered, 6.20, 1000, config.Budget{}, GateProceed},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Gate(tt.billing, tt.estimate, tt.spent, tt.budget)
			if got != tt.want {
				t.Errorf("Gate(%s, est=%.2f, spent=%.2f) = %q, want %q", tt.billing, tt.estimate, tt.spent, got, tt.want)
			}
		})
	}
}

// TestEstimateCost covers subscription/free zero-cost and metered cold-start
// scaling by issue size.
func TestEstimateCost(t *testing.T) {
	cfg := threeProviderConfig()
	reg := New(cfg, healthyRunners("claude", "codex", "ollama"), WithHistory(newFakeHistory()))

	sub := core.Model{ID: "opus-4-8", Provider: "claude", Billing: core.BillingSubscription}
	free := core.Model{ID: "qwen3-coder", Provider: "ollama", Billing: core.BillingFree}
	metered := core.Model{ID: "fable-5", Provider: "claude", Billing: core.BillingMetered}

	if got := reg.EstimateCost(sub, "build", IssueSizeLarge); got != 0 {
		t.Errorf("subscription estimate = %.2f, want 0", got)
	}
	if got := reg.EstimateCost(free, "build", IssueSizeLarge); got != 0 {
		t.Errorf("free estimate = %.2f, want 0", got)
	}

	tests := []struct {
		size IssueSize
		want float64
	}{
		{IssueSizeSmall, 0.50},
		{IssueSizeMedium, 2.00},
		{IssueSizeLarge, 6.00},
		{"", defaultColdStartUSD},
	}
	for _, tt := range tests {
		if got := reg.EstimateCost(metered, "plan", tt.size); got != tt.want {
			t.Errorf("metered estimate for size %q = %.2f, want %.2f", tt.size, got, tt.want)
		}
	}
}

// TestGateModel wires EstimateCost + SpendSince + Gate together and checks the
// epic-cap block path reads spend from history.
func TestGateModel(t *testing.T) {
	cfg := threeProviderConfig() // confirm_over_usd=2, max_usd_per_epic=25
	hist := newFakeHistory()
	epicStart := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	reg := New(cfg, healthyRunners("claude", "codex", "ollama"),
		WithHistory(hist), WithClock(fixedClock(epicStart.Add(48*time.Hour))))

	metered := core.Model{ID: "fable-5", Provider: "claude", Billing: core.BillingMetered}

	// Under threshold on a small issue ($0.50): proceed.
	if dec, est := reg.GateModel(metered, "build", IssueSizeSmall, epicStart); dec != GateProceed {
		t.Errorf("small metered dispatch = %q (est %.2f), want proceed", dec, est)
	}
	// Large issue ($6.00) over confirm threshold: confirm.
	if dec, _ := reg.GateModel(metered, "build", IssueSizeLarge, epicStart); dec != GateConfirm {
		t.Errorf("large metered dispatch = %q, want confirm", dec)
	}
	// Epic already near cap: block. $23 spent + $6 large ≥ $25.
	hist.spend[""] = 23.00
	if dec, _ := reg.GateModel(metered, "build", IssueSizeLarge, epicStart); dec != GateBlock {
		t.Errorf("near-cap metered dispatch = %q, want block", dec)
	}

	// A subscription model bypasses regardless of spend.
	sub := core.Model{ID: "opus-4-8", Provider: "claude", Billing: core.BillingSubscription}
	if dec, _ := reg.GateModel(sub, "build", IssueSizeLarge, epicStart); dec != GateProceed {
		t.Errorf("subscription dispatch = %q, want proceed", dec)
	}
}
