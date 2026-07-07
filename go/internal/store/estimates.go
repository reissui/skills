package store

import (
	"fmt"
	"time"
)

// Estimate records a metered-model dispatch's predicted versus realized cost so
// drift is visible in /costs (spec: Cost gates — "actuals are always recorded
// against estimates so drift is visible"). ActualUSD is 0 until the stage
// finishes and RecordActual is called.
type Estimate struct {
	ID           int64
	Issue        int
	Stage        string
	Model        string
	EstimatedUSD float64
	ActualUSD    float64
	TS           time.Time
}

// RecordEstimate stores a pre-dispatch cost estimate and returns its id. TS
// defaults to now when zero.
func (st *Store) RecordEstimate(e Estimate) (int64, error) {
	if e.TS.IsZero() {
		e.TS = time.Now()
	}
	res, err := st.db.Exec(
		`INSERT INTO estimates (issue, stage, model, estimated_usd, actual_usd, ts)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		e.Issue, e.Stage, e.Model, e.EstimatedUSD, e.ActualUSD, e.TS.Unix(),
	)
	if err != nil {
		return 0, fmt.Errorf("store: record estimate: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("store: record estimate id: %w", err)
	}
	return id, nil
}

// RecordActual fills in the realized cost for a previously recorded estimate.
func (st *Store) RecordActual(id int64, actualUSD float64) error {
	return st.exec1("record actual cost",
		`UPDATE estimates SET actual_usd = ? WHERE id = ?`, actualUSD, id)
}

// EstimatesForIssue returns every estimate recorded for an issue, newest first.
func (st *Store) EstimatesForIssue(issue int) ([]Estimate, error) {
	rows, err := st.db.Query(
		`SELECT id, issue, stage, model, estimated_usd, actual_usd, ts
		 FROM estimates WHERE issue = ? ORDER BY ts DESC, id DESC`, issue)
	if err != nil {
		return nil, fmt.Errorf("store: estimates for issue %d: %w", issue, err)
	}
	defer rows.Close()

	var out []Estimate
	for rows.Next() {
		var (
			e  Estimate
			ts int64
		)
		if err := rows.Scan(&e.ID, &e.Issue, &e.Stage, &e.Model, &e.EstimatedUSD, &e.ActualUSD, &ts); err != nil {
			return nil, fmt.Errorf("store: scan estimate: %w", err)
		}
		e.TS = time.Unix(ts, 0)
		out = append(out, e)
	}
	return out, rows.Err()
}
