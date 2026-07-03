package workspace

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// requireGit skips the test when the git binary is unavailable.
func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git binary not available")
	}
}

// run executes a command in dir and returns its combined output, failing the
// test on error. Used by fixtures to drive git directly.
func run(t *testing.T, dir string, name string, args ...string) string {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	// Deterministic, network-free, identity-stable environment for git.
	cmd.Env = append(os.Environ(),
		"GIT_TERMINAL_PROMPT=0",
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=clex-test",
		"GIT_AUTHOR_EMAIL=clex-test@example.com",
		"GIT_COMMITTER_NAME=clex-test",
		"GIT_COMMITTER_EMAIL=clex-test@example.com",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %s (in %s): %v\n%s", name, strings.Join(args, " "), dir, err, out)
	}
	return string(out)
}

// gitConfig sets a stable identity and disables gpg signing for a checkout so
// commits succeed in CI without global config.
func gitConfig(t *testing.T, dir string) {
	t.Helper()
	run(t, dir, "git", "config", "user.name", "clex-test")
	run(t, dir, "git", "config", "user.email", "clex-test@example.com")
	run(t, dir, "git", "config", "commit.gpgsign", "false")
}

// writeFile writes content to path, creating parent dirs, failing on error.
func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// readFile reads path as a string, failing on error.
func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

// fileExists reports whether path exists as a regular file.
func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

// commitInWorktree writes and commits a file inside an already-checked-out
// worktree (staying on its current branch).
func commitInWorktree(t *testing.T, worktree, file, content, msg string) {
	t.Helper()
	writeFile(t, filepath.Join(worktree, file), content)
	run(t, worktree, "git", "add", file)
	run(t, worktree, "git", "commit", "-m", msg)
}

// currentBranchT returns the short branch name checked out in dir.
func currentBranchT(t *testing.T, dir string) string {
	t.Helper()
	return strings.TrimSpace(run(t, dir, "git", "rev-parse", "--abbrev-ref", "HEAD"))
}

// revParse resolves a revision to a full sha in dir.
func revParse(t *testing.T, dir, rev string) string {
	t.Helper()
	return strings.TrimSpace(run(t, dir, "git", "rev-parse", rev))
}

// branchExists reports whether a local branch exists in dir.
func branchExists(t *testing.T, dir, branch string) bool {
	t.Helper()
	cmd := exec.Command("git", "rev-parse", "--verify", "--quiet", "refs/heads/"+branch)
	cmd.Dir = dir
	return cmd.Run() == nil
}

// assertNotRebasing fails if dir is mid-rebase (rebase-merge/rebase-apply state
// dirs present in the worktree's git dir).
func assertNotRebasing(t *testing.T, dir string) {
	t.Helper()
	gitDir := strings.TrimSpace(run(t, dir, "git", "rev-parse", "--absolute-git-dir"))
	for _, name := range []string{"rebase-merge", "rebase-apply"} {
		if _, err := os.Stat(filepath.Join(gitDir, name)); err == nil {
			t.Fatalf("worktree %s is mid-rebase (%s exists)", dir, name)
		}
	}
}

// assertCleanTree fails if dir has staged or unstaged changes.
func assertCleanTree(t *testing.T, dir string) {
	t.Helper()
	out := strings.TrimSpace(run(t, dir, "git", "status", "--porcelain"))
	if out != "" {
		t.Fatalf("worktree %s not clean:\n%s", dir, out)
	}
}

// containsPath reports whether paths contains p (cleaned comparison).
func containsPath(paths []string, p string) bool {
	cp := filepath.Clean(p)
	for _, x := range paths {
		if filepath.Clean(x) == cp {
			return true
		}
	}
	return false
}
