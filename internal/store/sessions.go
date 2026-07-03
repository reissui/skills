package store

import (
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// SessionState is the lifecycle of a runner session row. It is deliberately
// distinct from core.State (a GitHub-materialized pipeline label): this tracks
// the OS/CLI process, not the issue's pipeline position (spec: SQLite runtime
// only — runner sessions, CLI session ids for resume).
type SessionState string

const (
	SessionRunning SessionState = "running"
	SessionStopped SessionState = "stopped"
	SessionDone    SessionState = "done"
)

// Session is one runner invocation against an issue. CLISession, when non-empty,
// is the provider CLI's own session id and lets clex resume rather than restart
// (spec: Resume, don't restart).
type Session struct {
	ID         int64
	Issue      int
	Repo       string
	Model      string // runner / model id
	CLISession string // provider CLI session id, "" until known
	State      SessionState
	StartedAt  time.Time
	EndedAt    time.Time // zero while running
}

// CreateSession inserts a new running session and returns its assigned id. If
// s.State is empty it defaults to SessionRunning. StartedAt defaults to now when
// zero. EndedAt is stored as 0 when zero.
func (st *Store) CreateSession(s Session) (int64, error) {
	if s.State == "" {
		s.State = SessionRunning
	}
	if s.StartedAt.IsZero() {
		s.StartedAt = time.Now()
	}
	res, err := st.db.Exec(
		`INSERT INTO sessions (issue, repo, model, cli_session, state, started_at, ended_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		s.Issue, s.Repo, s.Model, s.CLISession, string(s.State),
		s.StartedAt.Unix(), unixOrZero(s.EndedAt),
	)
	if err != nil {
		return 0, fmt.Errorf("store: create session: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("store: create session id: %w", err)
	}
	return id, nil
}

// GetSession returns the session with the given id, or ErrNotFound.
func (st *Store) GetSession(id int64) (Session, error) {
	row := st.db.QueryRow(
		`SELECT id, issue, repo, model, cli_session, state, started_at, ended_at
		 FROM sessions WHERE id = ?`, id)
	return scanSession(row)
}

// SetSessionCLIID records the provider CLI session id for a session so a later
// stage can resume it.
func (st *Store) SetSessionCLIID(id int64, cliSession string) error {
	return st.exec1("update session cli id",
		`UPDATE sessions SET cli_session = ? WHERE id = ?`, cliSession, id)
}

// FinishSession marks a session terminal (SessionStopped or SessionDone) and
// stamps EndedAt with the given time (now if zero).
func (st *Store) FinishSession(id int64, state SessionState, endedAt time.Time) error {
	if endedAt.IsZero() {
		endedAt = time.Now()
	}
	return st.exec1("finish session",
		`UPDATE sessions SET state = ?, ended_at = ? WHERE id = ?`,
		string(state), endedAt.Unix(), id)
}

// RunningSessions returns all sessions currently in the SessionRunning state,
// oldest first. On restart the daemon uses this to reconcile against GitHub.
func (st *Store) RunningSessions() ([]Session, error) {
	rows, err := st.db.Query(
		`SELECT id, issue, repo, model, cli_session, state, started_at, ended_at
		 FROM sessions WHERE state = ? ORDER BY started_at ASC, id ASC`,
		string(SessionRunning))
	if err != nil {
		return nil, fmt.Errorf("store: list running sessions: %w", err)
	}
	defer rows.Close()
	return collectSessions(rows)
}

// SessionsForIssue returns every session recorded for an issue, newest first.
func (st *Store) SessionsForIssue(issue int) ([]Session, error) {
	rows, err := st.db.Query(
		`SELECT id, issue, repo, model, cli_session, state, started_at, ended_at
		 FROM sessions WHERE issue = ? ORDER BY started_at DESC, id DESC`, issue)
	if err != nil {
		return nil, fmt.Errorf("store: list sessions for issue %d: %w", issue, err)
	}
	defer rows.Close()
	return collectSessions(rows)
}

// scanner is satisfied by both *sql.Row and *sql.Rows.
type scanner interface {
	Scan(dest ...any) error
}

func scanSession(sc scanner) (Session, error) {
	var (
		s              Session
		state          string
		started, ended int64
	)
	if err := sc.Scan(&s.ID, &s.Issue, &s.Repo, &s.Model, &s.CLISession, &state, &started, &ended); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Session{}, ErrNotFound
		}
		return Session{}, fmt.Errorf("store: scan session: %w", err)
	}
	s.State = SessionState(state)
	s.StartedAt = time.Unix(started, 0)
	s.EndedAt = timeOrZero(ended)
	return s, nil
}

func collectSessions(rows *sql.Rows) ([]Session, error) {
	var out []Session
	for rows.Next() {
		s, err := scanSession(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}
