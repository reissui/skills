package store

import (
	"math"
	"testing"
	"time"

	"github.com/reissui/clex/internal/core"
)

// seedUsage inserts a fixed set of usage rows used by the aggregate tests.
func seedUsage(t *testing.T, st *Store) {
	t.Helper()
	rows := []UsageRecord{
		// model A / build / standard: two rows, durations 60s & 120s (avg 90s),
		// one success + one failure (rate 0.5), costs 1.00 + 2.00.
		{Model: "A", Stage: "build", Difficulty: core.DifficultyStandard, Duration: 60 * time.Second, Success: true, CostUSD: 1.00, TS: ts},
		{Model: "A", Stage: "build", Difficulty: core.DifficultyStandard, Duration: 120 * time.Second, Success: false, CostUSD: 2.00, TS: ts.Add(time.Hour)},
		// model A / build / complex: one success (rate 1.0).
		{Model: "A", Stage: "build", Difficulty: core.DifficultyComplex, Duration: 300 * time.Second, Success: true, CostUSD: 4.00, TS: ts.Add(2 * time.Hour)},
		// model A / plan / standard: different stage, must not pollute build avg.
		{Model: "A", Stage: "plan", Difficulty: core.DifficultyStandard, Duration: 10 * time.Second, Success: true, CostUSD: 0.50, TS: ts},
		// model B: different model, must be isolated.
		{Model: "B", Stage: "build", Difficulty: core.DifficultyStandard, Duration: 999 * time.Second, Success: false, CostUSD: 9.00, TS: ts},
	}
	for i, r := range rows {
		if _, err := st.RecordUsage(r); err != nil {
			t.Fatalf("seed usage row %d: %v", i, err)
		}
	}
}

func almost(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func TestAvgDuration(t *testing.T) {
	st, _ := openTemp(t)
	seedUsage(t, st)

	got, err := st.AvgDuration("A", "build")
	if err != nil {
		t.Fatalf("AvgDuration: %v", err)
	}
	// (60 + 120 + 300) / 3 = 160s.
	if want := 160 * time.Second; got != want {
		t.Fatalf("AvgDuration(A,build) = %v, want %v", got, want)
	}

	// Unknown pair yields 0, not an error.
	got, err = st.AvgDuration("A", "review")
	if err != nil {
		t.Fatalf("AvgDuration unknown: %v", err)
	}
	if got != 0 {
		t.Fatalf("AvgDuration(unknown) = %v, want 0", got)
	}
}

func TestSuccessRate(t *testing.T) {
	st, _ := openTemp(t)
	seedUsage(t, st)

	rate, err := st.SuccessRate("A", core.DifficultyStandard)
	if err != nil {
		t.Fatalf("SuccessRate: %v", err)
	}
	// build/standard has 1 success of 2; plan/standard has 1 of 1 → 2 of 3.
	if want := 2.0 / 3.0; !almost(rate, want) {
		t.Fatalf("SuccessRate(A,standard) = %v, want %v", rate, want)
	}

	rate, err = st.SuccessRate("A", core.DifficultyComplex)
	if err != nil {
		t.Fatalf("SuccessRate complex: %v", err)
	}
	if !almost(rate, 1.0) {
		t.Fatalf("SuccessRate(A,complex) = %v, want 1.0", rate)
	}

	// Unknown difficulty yields 0.
	rate, err = st.SuccessRate("A", core.DifficultyTrivial)
	if err != nil {
		t.Fatalf("SuccessRate trivial: %v", err)
	}
	if !almost(rate, 0) {
		t.Fatalf("SuccessRate(A,trivial) = %v, want 0", rate)
	}
}

func TestSpendSince(t *testing.T) {
	st, _ := openTemp(t)
	seedUsage(t, st)

	// From the very start, model A total = 1.00 + 2.00 + 4.00 + 0.50 = 7.50.
	total, err := st.SpendSince(ts.Add(-time.Minute), "A")
	if err != nil {
		t.Fatalf("SpendSince: %v", err)
	}
	if !almost(total, 7.50) {
		t.Fatalf("SpendSince(all,A) = %v, want 7.50", total)
	}

	// Cutoff after the first hour excludes the two rows at ts (1.00 + 0.50),
	// leaving 2.00 + 4.00 = 6.00.
	total, err = st.SpendSince(ts.Add(30*time.Minute), "A")
	if err != nil {
		t.Fatalf("SpendSince cutoff: %v", err)
	}
	if !almost(total, 6.00) {
		t.Fatalf("SpendSince(cutoff,A) = %v, want 6.00", total)
	}

	// Model B is isolated.
	total, err = st.SpendSince(ts.Add(-time.Minute), "B")
	if err != nil {
		t.Fatalf("SpendSince B: %v", err)
	}
	if !almost(total, 9.00) {
		t.Fatalf("SpendSince(all,B) = %v, want 9.00", total)
	}

	// No rows in range yields 0.
	total, err = st.SpendSince(ts.Add(24*time.Hour), "A")
	if err != nil {
		t.Fatalf("SpendSince future: %v", err)
	}
	if !almost(total, 0) {
		t.Fatalf("SpendSince(future,A) = %v, want 0", total)
	}
}
