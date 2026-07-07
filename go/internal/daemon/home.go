package daemon

import (
	"fmt"
	"os"
	"path/filepath"
)

// homeDirPerm is the mode for clex-owned directories under ~/.clex. The spec
// (Security model — "SQLite, config, and spool dirs are 0700/0600") requires
// owner-only access so secrets at rest are not world- or group-readable.
const homeDirPerm = 0o700

// EnsureHome creates the clex home directory and its standard subdirectories
// with mode 0700, returning the resolved home path. It is idempotent: existing
// directories are left in place but re-chmod'd to 0700 so a directory created
// with a looser umask on a prior run is tightened. This is the single place the
// daemon guarantees the "~/.clex dirs are 0700" security property.
//
// Subdirectories created: worktrees (git worktrees), spool (inbound Telegram
// images). The database and socket files live directly under home; their own
// permissions are enforced by their owners (store, ipc).
func EnsureHome(home string) (string, error) {
	if home == "" {
		return "", fmt.Errorf("daemon: empty home directory")
	}
	abs, err := filepath.Abs(home)
	if err != nil {
		return "", fmt.Errorf("daemon: resolve home: %w", err)
	}
	dirs := []string{
		abs,
		filepath.Join(abs, "worktrees"),
		filepath.Join(abs, "spool"),
	}
	for _, d := range dirs {
		if err := os.MkdirAll(d, homeDirPerm); err != nil {
			return "", fmt.Errorf("daemon: mkdir %s: %w", d, err)
		}
		// Re-chmod: MkdirAll honors the umask, which we do not trust to be
		// restrictive. Force 0700 so the guarantee holds regardless of umask.
		if err := os.Chmod(d, homeDirPerm); err != nil {
			return "", fmt.Errorf("daemon: chmod %s: %w", d, err)
		}
	}
	return abs, nil
}

// SpoolDir returns the inbound-image spool path within a clex home.
func SpoolDir(home string) string { return filepath.Join(home, "spool") }

// DBPath returns the runtime SQLite database path within a clex home.
func DBPath(home string) string { return filepath.Join(home, "clex.db") }
