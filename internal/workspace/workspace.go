// Package workspace implements the clex git branch model: epic integration
// branches, per-issue worktrees, and the rebase/merge choreography that keeps
// them in sync (spec: Workspace manager & branch model).
//
// Every operation shells out to the git binary — never a Go git library — so
// that behavior matches the git the operator has installed and configured. Each
// git invocation is logged at debug level with its exact argument vector.
//
// Two guardrails are enforced everywhere (spec: Error handling & safety):
// runners are only ever handed a worktree, so no operation may run against a
// repository's primary checkout, and nothing may commit to or move the main
// branch directly. Violations return a typed error rather than mutating the
// repository.
package workspace

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os/exec"
	"path/filepath"
	"strings"
)

// MainBranch is the protected trunk branch. No workspace operation ever commits
// to it, moves it, or runs a runner against it (spec: never commit to main).
const MainBranch = "main"

// Sentinel errors returned by the package. Callers match with errors.Is.
var (
	// ErrConflict is returned when a rebase hits a conflict. When it is
	// returned the worktree has already been restored to a clean state via
	// "git rebase --abort" — a worktree is never left mid-rebase.
	ErrConflict = errors.New("workspace: rebase conflict")

	// ErrPrimaryCheckout is returned when an operation is asked to run against
	// a repository's primary (non-worktree) checkout. Runners only ever get
	// worktrees.
	ErrPrimaryCheckout = errors.New("workspace: refusing to operate on primary checkout")

	// ErrMainBranch is returned when an operation would commit to, move, or
	// otherwise mutate the protected main branch directly.
	ErrMainBranch = errors.New("workspace: refusing to operate on main branch")
)

// Manager owns the on-disk layout for one worktree root and runs git commands
// beneath it. It is safe to construct many; it holds no mutable state of its
// own. The zero value is not usable — call New.
type Manager struct {
	// root is the worktree root, e.g. ~/.clex. Worktrees live under
	// <root>/worktrees/<repo>/<issue>-<slug>.
	root string
	// log receives one debug record per git invocation. Never nil after New.
	log *slog.Logger
}

// New returns a Manager rooted at root (the clex home, e.g. ~/.clex). Worktrees
// are created under <root>/worktrees. If log is nil a no-op logger is used so
// callers never have to guard it.
func New(root string, log *slog.Logger) *Manager {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Manager{root: filepath.Clean(root), log: log}
}

// EpicBranch is the integration branch name for epic n: "clex/epic-<n>".
func EpicBranch(epicNum int) string {
	return fmt.Sprintf("clex/epic-%d", epicNum)
}

// IssueBranch is the branch name for an issue worktree: "clex/<issue>-<slug>".
// The slug is sanitized to a safe git ref component.
func IssueBranch(issueNum int, slug string) string {
	return fmt.Sprintf("clex/%d-%s", issueNum, sanitizeSlug(slug))
}

// worktreesDir is <root>/worktrees.
func (m *Manager) worktreesDir() string {
	return filepath.Join(m.root, "worktrees")
}

// WorktreePath returns the path a worktree for the given repo and issue would
// occupy: <root>/worktrees/<repo>/<issue>-<slug>. repo is a repository
// identifier such as "owner/name" or a filesystem path; only its base name is
// used so worktrees group by repository name.
func (m *Manager) WorktreePath(repo string, issueNum int, slug string) string {
	return filepath.Join(m.worktreesDir(), repoKey(repo), fmt.Sprintf("%d-%s", issueNum, sanitizeSlug(slug)))
}

// CreateEpicBranch cuts the integration branch clex/epic-<n> from the latest
// origin/main and leaves the primary checkout on its original branch. It fetches
// first so the branch is based on up-to-date trunk (spec: cut from latest main).
//
// repoDir is the repository's primary checkout. The operation runs git there but
// never checks out or moves main: it creates the branch ref pointed at the
// freshly fetched origin/main without switching to it.
func (m *Manager) CreateEpicBranch(ctx context.Context, repoDir string, epicNum int) (string, error) {
	if err := m.guardPrimaryOnly(ctx, repoDir); err != nil {
		return "", err
	}
	branch := EpicBranch(epicNum)
	if branch == MainBranch {
		return "", ErrMainBranch
	}
	if _, err := m.git(ctx, repoDir, "fetch", "origin", MainBranch); err != nil {
		return "", fmt.Errorf("fetch origin %s: %w", MainBranch, err)
	}
	// Base the integration branch on the freshly fetched trunk. Prefer
	// origin/main; fall back to the local main ref when there is no remote
	// (e.g. a bare local test repo whose "origin" is a sibling directory that
	// tracks main). Create the ref without checking it out so the primary
	// checkout's branch and working tree are untouched.
	base := m.trunkBase(ctx, repoDir)
	if _, err := m.git(ctx, repoDir, "branch", "--force", branch, base); err != nil {
		return "", fmt.Errorf("create branch %s from %s: %w", branch, base, err)
	}
	return branch, nil
}

// CreateWorktree adds a worktree for one issue under
// <root>/worktrees/<repo>/<issue>-<slug> with a new branch clex/<issue>-<slug>
// based on the epic integration branch, and returns the worktree path. The epic
// branch must already exist (see CreateEpicBranch).
//
// repoDir is the repository's primary checkout that owns the worktree.
func (m *Manager) CreateWorktree(ctx context.Context, repoDir string, epicNum, issueNum int, slug string) (string, error) {
	if err := m.guardPrimaryOnly(ctx, repoDir); err != nil {
		return "", err
	}
	epic := EpicBranch(epicNum)
	branch := IssueBranch(issueNum, slug)
	if branch == MainBranch || epic == MainBranch {
		return "", ErrMainBranch
	}
	path := m.WorktreePath(repoDir, issueNum, slug)
	// "git worktree add -b <branch> <path> <epic>" creates the branch off the
	// epic and checks it out into a brand-new working tree. git creates parent
	// directories for <path> itself.
	if _, err := m.git(ctx, repoDir, "worktree", "add", "-b", branch, path, epic); err != nil {
		return "", fmt.Errorf("add worktree %s (%s off %s): %w", path, branch, epic, err)
	}
	return path, nil
}

// RebaseOntoEpic rebases the issue branch checked out in worktreeDir onto its
// epic integration branch. On success the worktree sits atop the current epic
// tip. On conflict the rebase is aborted so the worktree is returned to a clean
// state and ErrConflict is returned — the worktree is never left mid-rebase
// (spec: Error handling & safety).
//
// The epic branch name is derived from the issue branch's clex/<n>-<slug>
// lineage is not stored on disk, so the caller passes the epic number.
func (m *Manager) RebaseOntoEpic(ctx context.Context, worktreeDir string, epicNum int) error {
	if err := m.guardWorktreeOnly(ctx, worktreeDir); err != nil {
		return err
	}
	return m.rebase(ctx, worktreeDir, EpicBranch(epicNum))
}

// RebaseEpicOntoMain rebases the epic integration branch onto the latest main in
// preparation for the single final PR (spec: integration branch rebases onto
// main). It fetches first, checks out the epic branch in the primary checkout,
// and rebases it onto origin/main. main itself is never moved. On conflict the
// rebase is aborted and ErrConflict is returned, leaving the epic branch intact.
func (m *Manager) RebaseEpicOntoMain(ctx context.Context, repoDir string, epicNum int) error {
	if err := m.guardPrimaryOnly(ctx, repoDir); err != nil {
		return err
	}
	epic := EpicBranch(epicNum)
	if epic == MainBranch {
		return ErrMainBranch
	}
	if _, err := m.git(ctx, repoDir, "fetch", "origin", MainBranch); err != nil {
		return fmt.Errorf("fetch origin %s: %w", MainBranch, err)
	}
	if _, err := m.git(ctx, repoDir, "checkout", epic); err != nil {
		return fmt.Errorf("checkout %s: %w", epic, err)
	}
	return m.rebase(ctx, repoDir, m.trunkBase(ctx, repoDir))
}

// rebase runs "git rebase <onto>" in dir and, on any failure, aborts the rebase
// so dir is left clean and maps the failure to ErrConflict. A rebase can fail
// for reasons other than a textual conflict (e.g. a hook), but in every failing
// case the safe response is identical: abort and surface ErrConflict so the
// caller retries or escalates rather than inspecting a half-applied tree.
func (m *Manager) rebase(ctx context.Context, dir, onto string) error {
	// Note: rebasing a worktree *onto* main is legitimate (it is how issue and
	// epic branches catch up); what is forbidden is running *on* main, which the
	// guards above already prevent.
	if _, err := m.git(ctx, dir, "rebase", onto); err != nil {
		// Best-effort abort; ignore its error because we are already on the
		// failure path and the abort is what restores cleanliness.
		if _, abortErr := m.git(ctx, dir, "rebase", "--abort"); abortErr != nil {
			m.log.DebugContext(ctx, "rebase abort failed", "dir", dir, "err", abortErr)
		}
		return fmt.Errorf("%w: rebase %s in %s: %v", ErrConflict, onto, dir, err)
	}
	return nil
}

// Cleanup removes a single issue worktree and prunes its administrative files.
// It is used once an issue has merged or closed. Removing a worktree does not
// delete its branch; that is left to git's own housekeeping and the caller's
// policy. Cleanup refuses to remove a primary checkout.
func (m *Manager) Cleanup(ctx context.Context, worktreeDir string) error {
	if err := m.guardWorktreeOnly(ctx, worktreeDir); err != nil {
		return err
	}
	// Find the owning repository so we can run "git worktree remove" from it;
	// git refuses to remove a worktree when invoked from inside that worktree.
	primary, err := m.primaryCheckoutOf(ctx, worktreeDir)
	if err != nil {
		return err
	}
	if _, err := m.git(ctx, primary, "worktree", "remove", "--force", worktreeDir); err != nil {
		return fmt.Errorf("remove worktree %s: %w", worktreeDir, err)
	}
	// Prune any now-dangling administrative entries.
	if _, err := m.git(ctx, primary, "worktree", "prune"); err != nil {
		return fmt.Errorf("prune worktrees: %w", err)
	}
	return nil
}

// GC removes orphaned worktrees for a repository: those whose working directory
// has gone missing on disk. It first prunes git's administrative records, then
// lists the remaining registered worktrees and removes any whose path no longer
// exists. Active worktrees (present on disk) are left untouched. It returns the
// paths it removed.
//
// This mirrors "clex gc" (spec: Worktrees cleaned up after merge/close; clex gc
// for manual cleanup). Deciding which issues are merged/closed is the caller's
// job — GC handles the on-disk consequence.
func (m *Manager) GC(ctx context.Context, repoDir string) ([]string, error) {
	if err := m.guardPrimaryOnly(ctx, repoDir); err != nil {
		return nil, err
	}
	// Prune records for worktrees whose directory has disappeared, then ask git
	// what it still tracks.
	if _, err := m.git(ctx, repoDir, "worktree", "prune"); err != nil {
		return nil, fmt.Errorf("prune worktrees: %w", err)
	}
	out, err := m.git(ctx, repoDir, "worktree", "list", "--porcelain")
	if err != nil {
		return nil, fmt.Errorf("list worktrees: %w", err)
	}
	var removed []string
	for _, wt := range parseWorktreeList(out) {
		// Never touch the primary checkout or anything outside our root.
		if wt.bare || sameDir(wt.path, repoDir) {
			continue
		}
		if !underDir(wt.path, m.worktreesDir()) {
			continue
		}
		if dirExists(wt.path) {
			continue // active
		}
		if _, err := m.git(ctx, repoDir, "worktree", "remove", "--force", wt.path); err != nil {
			// prune should already have handled a missing dir; if remove still
			// fails, report it rather than silently skipping.
			return removed, fmt.Errorf("remove orphaned worktree %s: %w", wt.path, err)
		}
		removed = append(removed, wt.path)
	}
	return removed, nil
}

// git runs "git <args...>" in dir, logging the exact command, and returns its
// combined stdout. On non-zero exit it returns an error carrying stderr.
func (m *Manager) git(ctx context.Context, dir string, args ...string) (string, error) {
	m.log.DebugContext(ctx, "git", "dir", dir, "args", args)
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return stdout.String(), &GitError{
			Args:   args,
			Dir:    dir,
			Stderr: strings.TrimSpace(stderr.String()),
			Err:    err,
		}
	}
	return stdout.String(), nil
}

// GitError carries the context of a failed git invocation.
type GitError struct {
	Args   []string
	Dir    string
	Stderr string
	Err    error
}

func (e *GitError) Error() string {
	msg := fmt.Sprintf("git %s (in %s): %v", strings.Join(e.Args, " "), e.Dir, e.Err)
	if e.Stderr != "" {
		msg += ": " + e.Stderr
	}
	return msg
}

func (e *GitError) Unwrap() error { return e.Err }
