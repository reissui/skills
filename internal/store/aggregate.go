package store

import (
	"database/sql"
	"fmt"
	"time"

	"github.com/reissui/clex/internal/core"
)

// AvgDuration returns the mean wall-clock duration of completed stages for a
// model/stage pair, computed over the usage log. It feeds the router's
// per-model, per-stage speed history (spec: Routing — observed speed). With no
// matching rows it returns 0.
func (st *Store) AvgDuration(model, stage string) (time.Duration, error) {
	var avgMS sql.NullFloat64
	err := st.db.QueryRow(
		`SELECT AVG(duration_ms) FROM usage WHERE model = ? AND stage = ?`,
		model, stage).Scan(&avgMS)
	if err != nil {
		return 0, fmt.Errorf("store: avg duration for %q/%q: %w", model, stage, err)
	}
	return time.Duration(nullFloat(avgMS)) * time.Millisecond, nil
}

// SuccessRate returns a model's historical success fraction (0..1) at a given
// difficulty, computed over the usage log. This is the "track record" the router
// weighs against an issue's difficulty estimate when choosing a builder (spec:
// Routing — predicted success = difficulty vs. that model's track record). With
// no matching rows it returns 0.
func (st *Store) SuccessRate(model string, difficulty core.Difficulty) (float64, error) {
	var rate sql.NullFloat64
	// AVG over the 0/1 success column is the success fraction.
	err := st.db.QueryRow(
		`SELECT AVG(success) FROM usage WHERE model = ? AND difficulty = ?`,
		model, string(difficulty)).Scan(&rate)
	if err != nil {
		return 0, fmt.Errorf("store: success rate for %q/%q: %w", model, difficulty, err)
	}
	return nullFloat(rate), nil
}

// SpendSince returns the total estimated USD cost recorded for a model since
// time t (inclusive), summed over the usage log. It backs headroom/spend
// reporting in /costs (spec: Cost gates, /costs). With no matching rows it
// returns 0.
func (st *Store) SpendSince(t time.Time, model string) (float64, error) {
	var total sql.NullFloat64
	err := st.db.QueryRow(
		`SELECT SUM(cost_usd) FROM usage WHERE model = ? AND ts >= ?`,
		model, t.Unix()).Scan(&total)
	if err != nil {
		return 0, fmt.Errorf("store: spend since for %q: %w", model, err)
	}
	return nullFloat(total), nil
}
