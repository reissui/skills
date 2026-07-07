package store

import (
	"fmt"
	"time"
)

// LogEntry is one line in the append-only runtime event log (spec: SQLite
// runtime only — event log). It is distinct from core.Event, which is a runner's
// streamed output; this is the operator-facing audit trail of pipeline actions.
//
// Detail must already be redacted by the caller: the event log must never
// contain secrets, and known token patterns are stripped before logging (epic
// §Security — "the event log redacts known secret patterns"). Issue is 0 for
// entries not scoped to a specific issue.
type LogEntry struct {
	ID     int64
	TS     time.Time
	Issue  int
	Kind   string
	Detail string
}

// AppendEvent writes one line to the event log and returns its id. TS defaults
// to now when zero. The log is append-only; there is no update or delete.
func (st *Store) AppendEvent(e LogEntry) (int64, error) {
	if e.TS.IsZero() {
		e.TS = time.Now()
	}
	res, err := st.db.Exec(
		`INSERT INTO events (ts, issue, kind, detail) VALUES (?, ?, ?, ?)`,
		e.TS.Unix(), e.Issue, e.Kind, e.Detail)
	if err != nil {
		return 0, fmt.Errorf("store: append event: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("store: append event id: %w", err)
	}
	return id, nil
}

// RecentEvents returns up to limit most-recent log entries across all issues,
// newest first. A limit <= 0 defaults to 100.
func (st *Store) RecentEvents(limit int) ([]LogEntry, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := st.db.Query(
		`SELECT id, ts, issue, kind, detail FROM events
		 ORDER BY ts DESC, id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("store: recent events: %w", err)
	}
	defer rows.Close()
	return collectEvents(rows)
}

// EventsForIssue returns every log entry scoped to an issue, newest first.
func (st *Store) EventsForIssue(issue int) ([]LogEntry, error) {
	rows, err := st.db.Query(
		`SELECT id, ts, issue, kind, detail FROM events
		 WHERE issue = ? ORDER BY ts DESC, id DESC`, issue)
	if err != nil {
		return nil, fmt.Errorf("store: events for issue %d: %w", issue, err)
	}
	defer rows.Close()
	return collectEvents(rows)
}

func collectEvents(rows interface {
	Next() bool
	Scan(...any) error
	Err() error
}) ([]LogEntry, error) {
	var out []LogEntry
	for rows.Next() {
		var (
			e  LogEntry
			ts int64
		)
		if err := rows.Scan(&e.ID, &ts, &e.Issue, &e.Kind, &e.Detail); err != nil {
			return nil, fmt.Errorf("store: scan event: %w", err)
		}
		e.TS = time.Unix(ts, 0)
		out = append(out, e)
	}
	return out, rows.Err()
}
