package store

import (
	"errors"
	"path/filepath"
	"testing"
	"time"
)

// openTemp opens a Store on a fresh file under t.TempDir() and registers
// cleanup. Using a real file (not :memory:) exercises the on-disk path and lets
// a test reopen the same database.
func openTemp(t *testing.T) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "clex.db")
	st, err := Open(path)
	if err != nil {
		t.Fatalf("Open(%q): %v", path, err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st, path
}

func TestOpenCreatesSchema(t *testing.T) {
	st, _ := openTemp(t)

	v, err := st.Version()
	if err != nil {
		t.Fatalf("Version: %v", err)
	}
	if v != schemaVersion {
		t.Fatalf("fresh db version = %d, want %d", v, schemaVersion)
	}

	// A working insert proves the tables exist.
	if _, err := st.AppendEvent(LogEntry{Kind: "boot", Detail: "created"}); err != nil {
		t.Fatalf("AppendEvent on fresh schema: %v", err)
	}
}

func TestReopenIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "clex.db")

	// First open creates the schema and seeds a row.
	st1, err := Open(path)
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	id, err := st1.AppendEvent(LogEntry{Kind: "seed", Detail: "row"})
	if err != nil {
		t.Fatalf("AppendEvent: %v", err)
	}
	if err := st1.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Reopening must migrate idempotently: same version, no data loss, no error.
	st2, err := Open(path)
	if err != nil {
		t.Fatalf("reopen Open: %v", err)
	}
	defer st2.Close()

	v, err := st2.Version()
	if err != nil {
		t.Fatalf("Version after reopen: %v", err)
	}
	if v != schemaVersion {
		t.Fatalf("reopened db version = %d, want %d", v, schemaVersion)
	}

	got, err := st2.EventsForIssue(0)
	if err != nil {
		t.Fatalf("EventsForIssue: %v", err)
	}
	if len(got) != 1 || got[0].ID != id {
		t.Fatalf("reopen lost data: got %d rows, want the 1 seeded row", len(got))
	}
}

// TestMigrateNoopOnCurrent proves calling migrate again on an up-to-date, open
// database changes nothing (the idempotency guarantee at the migration level).
func TestMigrateNoopOnCurrent(t *testing.T) {
	st, _ := openTemp(t)

	before, err := st.Version()
	if err != nil {
		t.Fatalf("Version: %v", err)
	}
	if err := st.migrate(); err != nil {
		t.Fatalf("second migrate: %v", err)
	}
	after, err := st.Version()
	if err != nil {
		t.Fatalf("Version: %v", err)
	}
	if before != after {
		t.Fatalf("migrate was not a no-op: %d -> %d", before, after)
	}
}

func TestOpenBadPath(t *testing.T) {
	// A path whose parent directory does not exist cannot be opened.
	_, err := Open(filepath.Join(t.TempDir(), "no-such-dir", "clex.db"))
	if err == nil {
		t.Fatal("Open on nonexistent directory: want error, got nil")
	}
}

// TestNotFound checks the shared ErrNotFound contract on a representative
// lookup and a representative update.
func TestNotFound(t *testing.T) {
	st, _ := openTemp(t)

	if _, err := st.GetSession(999); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetSession(missing) err = %v, want ErrNotFound", err)
	}
	if err := st.ConsumeImage(999); !errors.Is(err, ErrNotFound) {
		t.Fatalf("ConsumeImage(missing) err = %v, want ErrNotFound", err)
	}
	if _, err := st.TelegramByMsg(999); !errors.Is(err, ErrNotFound) {
		t.Fatalf("TelegramByMsg(missing) err = %v, want ErrNotFound", err)
	}
}

// ts is a fixed reference time for deterministic fixtures.
var ts = time.Date(2026, 7, 3, 12, 0, 0, 0, time.UTC)
