package store

import (
	"database/sql"
	"fmt"
	"time"

	"github.com/reissui/clex/internal/core"
)

// UsageRecord is one completed stage's token/cost/latency accounting for a
// model. These rows feed build routing (success × speed history) and the
// /costs report (spec: SQLite runtime only — token/usage tracking; Routing —
// build routing weighs success, speed, and cost).
type UsageRecord struct {
	ID         int64
	Model      string          // model id
	Stage      string          // routing role / stage type (e.g. "build")
	Difficulty core.Difficulty // issue difficulty, "" when not applicable
	Tokens     core.Usage      // in/out token counts
	CostUSD    float64         // estimated cost in USD
	Duration   time.Duration   // wall-clock duration of the stage
	Success    bool
	TS         time.Time // when the stage completed
}

// RecordUsage appends a usage row and returns its id. TS defaults to now when
// zero. Durations are stored at millisecond resolution.
func (st *Store) RecordUsage(u UsageRecord) (int64, error) {
	if u.TS.IsZero() {
		u.TS = time.Now()
	}
	res, err := st.db.Exec(
		`INSERT INTO usage (model, stage, difficulty, in_tokens, out_tokens, cost_usd, duration_ms, success, ts)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		u.Model, u.Stage, string(u.Difficulty),
		u.Tokens.In, u.Tokens.Out, u.CostUSD,
		u.Duration.Milliseconds(), boolToInt(u.Success), u.TS.Unix(),
	)
	if err != nil {
		return 0, fmt.Errorf("store: record usage: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("store: record usage id: %w", err)
	}
	return id, nil
}

// UsageForModel returns every usage row for a model, newest first. Primarily for
// inspection and tests; routing uses the aggregate queries.
func (st *Store) UsageForModel(model string) ([]UsageRecord, error) {
	rows, err := st.db.Query(
		`SELECT id, model, stage, difficulty, in_tokens, out_tokens, cost_usd, duration_ms, success, ts
		 FROM usage WHERE model = ? ORDER BY ts DESC, id DESC`, model)
	if err != nil {
		return nil, fmt.Errorf("store: usage for model %q: %w", model, err)
	}
	defer rows.Close()

	var out []UsageRecord
	for rows.Next() {
		var (
			u       UsageRecord
			diff    string
			durMS   int64
			success int
			ts      int64
		)
		if err := rows.Scan(&u.ID, &u.Model, &u.Stage, &diff, &u.Tokens.In, &u.Tokens.Out,
			&u.CostUSD, &durMS, &success, &ts); err != nil {
			return nil, fmt.Errorf("store: scan usage: %w", err)
		}
		u.Difficulty = core.Difficulty(diff)
		u.Duration = time.Duration(durMS) * time.Millisecond
		u.Success = success != 0
		u.TS = time.Unix(ts, 0)
		out = append(out, u)
	}
	return out, rows.Err()
}

// boolToInt maps a bool to SQLite's 0/1 integer convention.
func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// nullFloat unwraps a nullable aggregate (e.g. AVG over no rows) to 0.
func nullFloat(n sql.NullFloat64) float64 {
	if n.Valid {
		return n.Float64
	}
	return 0
}
