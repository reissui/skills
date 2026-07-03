package store

import "fmt"

// migrations is the ordered list of schema versions. Each entry's DDL upgrades
// the database from index i to index i+1. Migrations are applied in order and
// only those beyond the database's current PRAGMA user_version run, so the set
// is versioned and idempotent — appending a new migration is the only supported
// way to evolve the schema. Never edit or reorder an existing entry, as that
// would silently skip it on already-migrated databases.
//
// All timestamps are stored as Unix seconds (INTEGER) so aggregates can range
// over them cheaply and the layer never depends on SQLite's date functions.
var migrations = []string{
	// v1: initial schema — the six runtime-bookkeeping tables.
	`
CREATE TABLE sessions (
	id          INTEGER PRIMARY KEY AUTOINCREMENT,
	issue       INTEGER NOT NULL,
	repo        TEXT    NOT NULL,
	model       TEXT    NOT NULL,          -- runner / model id
	cli_session TEXT    NOT NULL DEFAULT '', -- provider CLI session id (for resume)
	state       TEXT    NOT NULL,          -- running | stopped | done
	started_at  INTEGER NOT NULL,          -- unix seconds
	ended_at    INTEGER NOT NULL DEFAULT 0 -- unix seconds, 0 while running
);
CREATE INDEX idx_sessions_issue ON sessions(issue);
CREATE INDEX idx_sessions_state ON sessions(state);

CREATE TABLE usage (
	id          INTEGER PRIMARY KEY AUTOINCREMENT,
	model       TEXT    NOT NULL,          -- model id
	stage       TEXT    NOT NULL,          -- routing role / stage type
	difficulty  TEXT    NOT NULL DEFAULT '', -- issue difficulty for success stats
	in_tokens   INTEGER NOT NULL,
	out_tokens  INTEGER NOT NULL,
	cost_usd    REAL    NOT NULL,          -- estimated cost in USD
	duration_ms INTEGER NOT NULL,          -- wall-clock duration, milliseconds
	success     INTEGER NOT NULL,          -- 0/1
	ts          INTEGER NOT NULL           -- unix seconds
);
CREATE INDEX idx_usage_model_stage ON usage(model, stage);
CREATE INDEX idx_usage_model_ts    ON usage(model, ts);

CREATE TABLE estimates (
	id            INTEGER PRIMARY KEY AUTOINCREMENT,
	issue         INTEGER NOT NULL,
	stage         TEXT    NOT NULL,
	model         TEXT    NOT NULL,
	estimated_usd REAL    NOT NULL,
	actual_usd    REAL    NOT NULL DEFAULT 0, -- filled in once the stage completes
	ts            INTEGER NOT NULL
);
CREATE INDEX idx_estimates_issue ON estimates(issue);

CREATE TABLE telegram_map (
	msg_id  INTEGER PRIMARY KEY,           -- telegram message id
	issue   INTEGER NOT NULL,              -- issue / epic number it tracks
	is_epic INTEGER NOT NULL DEFAULT 0     -- 0/1
);
CREATE INDEX idx_telegram_map_issue ON telegram_map(issue);

CREATE TABLE image_queue (
	id          INTEGER PRIMARY KEY AUTOINCREMENT,
	path        TEXT    NOT NULL,          -- spooled file path
	issue       INTEGER NOT NULL,          -- target issue
	received_at INTEGER NOT NULL,          -- unix seconds
	consumed    INTEGER NOT NULL DEFAULT 0 -- 0/1
);
CREATE INDEX idx_image_queue_pending ON image_queue(consumed, issue);

CREATE TABLE events (
	id     INTEGER PRIMARY KEY AUTOINCREMENT,
	ts     INTEGER NOT NULL,               -- unix seconds
	issue  INTEGER NOT NULL,               -- 0 when not issue-scoped
	kind   TEXT    NOT NULL,               -- short event kind
	detail TEXT    NOT NULL DEFAULT ''     -- one-line detail (secrets pre-redacted by caller)
);
CREATE INDEX idx_events_issue ON events(issue);
CREATE INDEX idx_events_ts    ON events(ts);
`,
}

// schemaVersion is the number of migrations the current binary knows about; a
// freshly migrated database has PRAGMA user_version == schemaVersion.
var schemaVersion = len(migrations)

// migrate brings the database up to schemaVersion, running only the migrations
// past its current PRAGMA user_version. It is idempotent: on an up-to-date
// database it makes no changes. Each step runs in its own transaction so a
// partially applied upgrade never leaves a mismatched version.
func (s *Store) migrate() error {
	var current int
	if err := s.db.QueryRow("PRAGMA user_version").Scan(&current); err != nil {
		return fmt.Errorf("store: read schema version: %w", err)
	}
	if current < 0 || current > len(migrations) {
		return fmt.Errorf("store: unknown schema version %d (binary knows %d)", current, len(migrations))
	}

	for v := current; v < len(migrations); v++ {
		tx, err := s.db.Begin()
		if err != nil {
			return fmt.Errorf("store: begin migration %d: %w", v+1, err)
		}
		if _, err := tx.Exec(migrations[v]); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("store: apply migration %d: %w", v+1, err)
		}
		// user_version does not accept a bind parameter, so format it in. v+1
		// is an int derived from a slice index, never user input.
		if _, err := tx.Exec(fmt.Sprintf("PRAGMA user_version = %d", v+1)); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("store: bump schema version to %d: %w", v+1, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("store: commit migration %d: %w", v+1, err)
		}
	}
	return nil
}

// Version reports the schema version the database is currently at. It equals
// schemaVersion after a successful Open.
func (s *Store) Version() (int, error) {
	var v int
	if err := s.db.QueryRow("PRAGMA user_version").Scan(&v); err != nil {
		return 0, fmt.Errorf("store: read schema version: %w", err)
	}
	return v, nil
}
