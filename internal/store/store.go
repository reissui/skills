// Package store is the embedded SQLite layer for clex's runtime bookkeeping.
//
// GitHub is the source of truth for pipeline state; this database holds only
// derived, runtime data — runner sessions (PIDs / CLI session ids for resume),
// per-model token and cost accounting, the Telegram message ↔ issue mapping, a
// pending-image queue, and an append-only event log. Losing this database must
// never lose pipeline state: worst case, in-flight runs are re-dispatched from
// GitHub (spec: Source of truth: GitHub — "SQLite (runtime only)").
//
// The driver is modernc.org/sqlite, a pure-Go (no cgo) SQLite. clex ships as a
// single static binary and CI builds with CGO_ENABLED=0, so no C toolchain may
// be required. All access goes through typed methods on *Store; callers never
// see SQL.
package store

import (
	"database/sql"
	"errors"
	"fmt"
	"time"

	// Registers the pure-Go "sqlite" driver with database/sql. No cgo.
	_ "modernc.org/sqlite"
)

// driverName is the database/sql driver registered by modernc.org/sqlite.
const driverName = "sqlite"

// ErrNotFound is returned by lookup methods when no matching row exists.
var ErrNotFound = errors.New("store: not found")

// Store is a handle to the clex runtime database. It is safe for concurrent use
// by multiple goroutines; the underlying *sql.DB pools connections.
type Store struct {
	db *sql.DB
}

// Open opens (creating if necessary) the SQLite database at path and applies
// any pending schema migrations. path may be a filesystem path or the special
// value ":memory:" for an ephemeral in-memory database (used by tests).
//
// Migrations are versioned and idempotent: opening a fresh path creates the
// full schema, and reopening an up-to-date database is a no-op. Open is safe to
// call on a database created by an older clex build.
func Open(path string) (*Store, error) {
	// Busy timeout keeps concurrent writers from failing immediately on a
	// locked database; foreign_keys enforces the telegram/image → issue links.
	dsn := path + "?_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)"
	db, err := sql.Open(driverName, dsn)
	if err != nil {
		return nil, fmt.Errorf("store: open %q: %w", path, err)
	}
	// A single writer connection avoids "database is locked" churn under WAL
	// for this low-volume bookkeeping workload while still allowing readers.
	db.SetMaxOpenConns(1)

	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("store: ping %q: %w", path, err)
	}

	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

// Close releases the underlying database handle.
func (s *Store) Close() error {
	return s.db.Close()
}

// exec1 runs an UPDATE/DELETE expected to touch exactly one row, returning
// ErrNotFound when no row matched. what names the operation for error context.
func (s *Store) exec1(what, query string, args ...any) error {
	res, err := s.db.Exec(query, args...)
	if err != nil {
		return fmt.Errorf("store: %s: %w", what, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("store: %s rows affected: %w", what, err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// wrapLookup normalizes an error from a single-row lookup: sql.ErrNoRows becomes
// ErrNotFound; anything else is wrapped with operation context.
func wrapLookup(what string, err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	return fmt.Errorf("store: %s: %w", what, err)
}

// unixOrZero converts a time to Unix seconds, mapping the zero time to 0 so a
// not-yet-set timestamp round-trips as 0 rather than a negative epoch.
func unixOrZero(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.Unix()
}

// timeOrZero is the inverse of unixOrZero: 0 becomes the zero time.
func timeOrZero(sec int64) time.Time {
	if sec == 0 {
		return time.Time{}
	}
	return time.Unix(sec, 0)
}
