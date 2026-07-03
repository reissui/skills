package workspace

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// testRepo is a git fixture: a bare "origin" plus a primary checkout cloned from
// it, with one commit on main. All workspace operations run against primary; the
// worktree root is a separate temp dir so worktree paths never collide with the
// checkout.
type testRepo struct {
	t       *testing.T
	root    string // worktree root passed to New
	primary string // primary checkout dir
	origin  string // bare origin dir
	mgr     *Manager
}

func newTestRepo(t *testing.T) *testRepo {
	t.Helper()
	requireGit(t)
	base := t.TempDir()
	origin := filepath.Join(base, "origin.git")
	primary := filepath.Join(base, "checkout")
	root := filepath.Join(base, "clexroot")

	// Bare origin.
	run(t, base, "git", "init", "--bare", "-b", "main", origin)
	// Clone it, configure identity, make an initial commit on main, push.
	run(t, base, "git", "clone", origin, primary)
	gitConfig(t, primary)
	writeFile(t, filepath.Join(primary, "README.md"), "# fixture\n")
	run(t, primary, "git", "add", "README.md")
	run(t, primary, "git", "commit", "-m", "initial")
	run(t, primary, "git", "push", "-u", "origin", "main")

	return &testRepo{
		t:       t,
		root:    root,
		primary: primary,
		origin:  origin,
		mgr:     New(root, nil),
	}
}

// commitOn checks out branch in dir (creating nothing), writes file with content,
// and commits it. Used to build divergent history for conflict tests.
func commitOn(t *testing.T, dir, branch, file, content, msg string) {
	t.Helper()
	run(t, dir, "git", "checkout", branch)
	writeFile(t, filepath.Join(dir, file), content)
	run(t, dir, "git", "add", file)
	run(t, dir, "git", "commit", "-m", msg)
}

func TestCreateEpicBranch(t *testing.T) {
	r := newTestRepo(t)
	ctx := context.Background()

	branch, err := r.mgr.CreateEpicBranch(ctx, r.primary, 42)
	if err != nil {
		t.Fatalf("CreateEpicBranch: %v", err)
	}
	if branch != "clex/epic-42" {
		t.Fatalf("branch = %q, want clex/epic-42", branch)
	}
	// The branch exists...
	if !branchExists(t, r.primary, "clex/epic-42") {
		t.Fatal("epic branch was not created")
	}
	// ...and the primary checkout was left on main (not switched to the epic).
	if got := currentBranchT(t, r.primary); got != "main" {
		t.Fatalf("primary checkout on %q, want main (epic creation must not switch)", got)
	}
	// The epic tip matches main's tip (cut from latest main).
	if epicRev, mainRev := revParse(t, r.primary, "clex/epic-42"), revParse(t, r.primary, "main"); epicRev != mainRev {
		t.Fatalf("epic %s != main %s; not cut from main tip", epicRev, mainRev)
	}
}

func TestCreateWorktree(t *testing.T) {
	r := newTestRepo(t)
	ctx := context.Background()
	if _, err := r.mgr.CreateEpicBranch(ctx, r.primary, 1); err != nil {
		t.Fatalf("CreateEpicBranch: %v", err)
	}

	path, err := r.mgr.CreateWorktree(ctx, r.primary, 1, 7, "workspace")
	if err != nil {
		t.Fatalf("CreateWorktree: %v", err)
	}
	wantPath := filepath.Join(r.root, "worktrees", "checkout", "7-workspace")
	if path != wantPath {
		t.Fatalf("path = %q, want %q", path, wantPath)
	}
	if !dirExists(path) {
		t.Fatalf("worktree dir %q does not exist", path)
	}
	// The worktree is on its issue branch, based off the epic.
	if got := currentBranchT(t, path); got != "clex/7-workspace" {
		t.Fatalf("worktree branch = %q, want clex/7-workspace", got)
	}
	if base, epic := revParse(t, path, "HEAD"), revParse(t, r.primary, "clex/epic-1"); base != epic {
		t.Fatalf("worktree HEAD %s != epic %s; not branched off epic", base, epic)
	}
}

// TestFullLifecycle covers the acceptance criterion: epic branch → two issue
// worktrees → commits → rebase → cleanup.
func TestFullLifecycle(t *testing.T) {
	r := newTestRepo(t)
	ctx := context.Background()

	if _, err := r.mgr.CreateEpicBranch(ctx, r.primary, 5); err != nil {
		t.Fatalf("CreateEpicBranch: %v", err)
	}

	// Two issue worktrees off the epic.
	wtA, err := r.mgr.CreateWorktree(ctx, r.primary, 5, 10, "alpha")
	if err != nil {
		t.Fatalf("CreateWorktree A: %v", err)
	}
	wtB, err := r.mgr.CreateWorktree(ctx, r.primary, 5, 11, "beta")
	if err != nil {
		t.Fatalf("CreateWorktree B: %v", err)
	}

	// Each worktree commits a distinct file (no overlap → no conflict).
	commitInWorktree(t, wtA, "alpha.txt", "alpha work\n", "issue 10 work")
	commitInWorktree(t, wtB, "beta.txt", "beta work\n", "issue 11 work")

	// Land issue 10 into the epic: fast-forward the epic branch to A's tip
	// (simulating the merge that would happen after review), then rebase B onto
	// the advanced epic.
	run(t, r.primary, "git", "checkout", "clex/epic-5")
	run(t, r.primary, "git", "merge", "--ff-only", "clex/10-alpha")
	// Return primary off the epic branch so guards on it stay valid and so the
	// epic branch is not "checked out" when B's worktree shares the repo.
	run(t, r.primary, "git", "checkout", "main")

	if err := r.mgr.RebaseOntoEpic(ctx, wtB, 5); err != nil {
		t.Fatalf("RebaseOntoEpic B: %v", err)
	}
	// After rebase, B contains alpha.txt (from the epic) and its own beta.txt.
	if !fileExists(filepath.Join(wtB, "alpha.txt")) {
		t.Fatal("rebased worktree B missing alpha.txt from epic")
	}
	if !fileExists(filepath.Join(wtB, "beta.txt")) {
		t.Fatal("rebased worktree B missing its own beta.txt")
	}

	// Cleanup both worktrees.
	if err := r.mgr.Cleanup(ctx, wtA); err != nil {
		t.Fatalf("Cleanup A: %v", err)
	}
	if err := r.mgr.Cleanup(ctx, wtB); err != nil {
		t.Fatalf("Cleanup B: %v", err)
	}
	if dirExists(wtA) || dirExists(wtB) {
		t.Fatal("worktree dirs remain after Cleanup")
	}
	// git no longer tracks them.
	list := run(t, r.primary, "git", "worktree", "list", "--porcelain")
	if strings.Contains(list, wtA) || strings.Contains(list, wtB) {
		t.Fatalf("git still tracks removed worktrees:\n%s", list)
	}
}

// TestRebaseConflictAborts covers: conflict during rebase returns ErrConflict
// and leaves the worktree clean.
func TestRebaseConflictAborts(t *testing.T) {
	r := newTestRepo(t)
	ctx := context.Background()
	if _, err := r.mgr.CreateEpicBranch(ctx, r.primary, 3); err != nil {
		t.Fatalf("CreateEpicBranch: %v", err)
	}
	wt, err := r.mgr.CreateWorktree(ctx, r.primary, 3, 20, "conflict")
	if err != nil {
		t.Fatalf("CreateWorktree: %v", err)
	}

	// Both the epic and the issue branch touch the SAME file with different
	// content on top of a shared base → guaranteed rebase conflict.
	commitInWorktree(t, wt, "clash.txt", "issue side\n", "issue edits clash")
	commitOn(t, r.primary, "clex/epic-3", "clash.txt", "epic side\n", "epic edits clash")
	run(t, r.primary, "git", "checkout", "main") // leave epic un-checked-out

	err = r.mgr.RebaseOntoEpic(ctx, wt, 3)
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("RebaseOntoEpic err = %v, want ErrConflict", err)
	}
	// The worktree must be clean (rebase aborted), not mid-rebase.
	assertNotRebasing(t, wt)
	assertCleanTree(t, wt)
	// And still on its own branch with its own content intact.
	if got := currentBranchT(t, wt); got != "clex/20-conflict" {
		t.Fatalf("after abort branch = %q, want clex/20-conflict", got)
	}
	if got := readFile(t, filepath.Join(wt, "clash.txt")); got != "issue side\n" {
		t.Fatalf("after abort clash.txt = %q, want issue side", got)
	}
}

// TestGuardPrimaryCheckout covers: rebase/cleanup against the primary checkout
// returns the guard error, and epic/worktree creation against a linked worktree
// is refused.
func TestGuardPrimaryCheckout(t *testing.T) {
	r := newTestRepo(t)
	ctx := context.Background()
	if _, err := r.mgr.CreateEpicBranch(ctx, r.primary, 8); err != nil {
		t.Fatalf("CreateEpicBranch: %v", err)
	}
	wt, err := r.mgr.CreateWorktree(ctx, r.primary, 8, 30, "guard")
	if err != nil {
		t.Fatalf("CreateWorktree: %v", err)
	}
	run(t, r.primary, "git", "checkout", "main")

	// Worktree-only ops must refuse the primary checkout.
	if err := r.mgr.RebaseOntoEpic(ctx, r.primary, 8); !errors.Is(err, ErrPrimaryCheckout) {
		t.Fatalf("RebaseOntoEpic(primary) = %v, want ErrPrimaryCheckout", err)
	}
	if err := r.mgr.Cleanup(ctx, r.primary); !errors.Is(err, ErrPrimaryCheckout) {
		t.Fatalf("Cleanup(primary) = %v, want ErrPrimaryCheckout", err)
	}
	// Primary-only ops must refuse a linked worktree.
	if _, err := r.mgr.CreateEpicBranch(ctx, wt, 9); !errors.Is(err, ErrPrimaryCheckout) {
		t.Fatalf("CreateEpicBranch(worktree) = %v, want ErrPrimaryCheckout", err)
	}
	if _, err := r.mgr.CreateWorktree(ctx, wt, 8, 31, "x"); !errors.Is(err, ErrPrimaryCheckout) {
		t.Fatalf("CreateWorktree(worktree) = %v, want ErrPrimaryCheckout", err)
	}
	if _, err := r.mgr.GC(ctx, wt); !errors.Is(err, ErrPrimaryCheckout) {
		t.Fatalf("GC(worktree) = %v, want ErrPrimaryCheckout", err)
	}
}

// TestGuardMainBranch covers: a runner worktree that is sitting on the protected
// main branch is refused by the worktree-only operations (ErrMainBranch). This
// is the positive enforcement of "nothing runs against main" — runners only get
// worktrees, and a worktree must never be on main.
func TestGuardMainBranch(t *testing.T) {
	r := newTestRepo(t)
	ctx := context.Background()
	if _, err := r.mgr.CreateEpicBranch(ctx, r.primary, 1); err != nil {
		t.Fatalf("CreateEpicBranch: %v", err)
	}
	// Free up main so it can be checked out in a linked worktree: move the
	// primary onto the epic branch first (git forbids main in two worktrees).
	run(t, r.primary, "git", "checkout", "clex/epic-1")

	// A linked worktree checked out on main. This is exactly the state the
	// guard must reject when a runner op targets it.
	wtMain := filepath.Join(r.root, "worktrees", "checkout", "on-main")
	run(t, r.primary, "git", "worktree", "add", wtMain, "main")
	if got := currentBranchT(t, wtMain); got != "main" {
		t.Fatalf("precondition: worktree on %q, want main", got)
	}

	if err := r.mgr.RebaseOntoEpic(ctx, wtMain, 1); !errors.Is(err, ErrMainBranch) {
		t.Fatalf("RebaseOntoEpic(worktree-on-main) = %v, want ErrMainBranch", err)
	}
	if err := r.mgr.Cleanup(ctx, wtMain); !errors.Is(err, ErrMainBranch) {
		t.Fatalf("Cleanup(worktree-on-main) = %v, want ErrMainBranch", err)
	}
}

// TestGC covers: GC removes only orphaned worktrees (one active, one orphaned).
func TestGC(t *testing.T) {
	r := newTestRepo(t)
	ctx := context.Background()
	if _, err := r.mgr.CreateEpicBranch(ctx, r.primary, 2); err != nil {
		t.Fatalf("CreateEpicBranch: %v", err)
	}
	active, err := r.mgr.CreateWorktree(ctx, r.primary, 2, 50, "active")
	if err != nil {
		t.Fatalf("CreateWorktree active: %v", err)
	}
	orphan, err := r.mgr.CreateWorktree(ctx, r.primary, 2, 51, "orphan")
	if err != nil {
		t.Fatalf("CreateWorktree orphan: %v", err)
	}

	// Orphan it: delete the working directory out from under git without
	// telling git. GC should notice and reap the administrative record.
	if err := os.RemoveAll(orphan); err != nil {
		t.Fatalf("rm orphan dir: %v", err)
	}

	removed, err := r.mgr.GC(ctx, r.primary)
	if err != nil {
		t.Fatalf("GC: %v", err)
	}
	// GC must never report having removed the active worktree. It may or may not
	// list the orphan explicitly: "worktree prune" can clear a missing dir
	// before the explicit "worktree remove" runs, so an empty removed slice is
	// valid. The tracked-state invariants below are the real contract.
	if containsPath(removed, active) {
		t.Fatalf("GC reported removing the active worktree: %v", removed)
	}
	// git must no longer track the orphan, but must still track the active one.
	list := run(t, r.primary, "git", "worktree", "list", "--porcelain")
	if strings.Contains(list, orphan) {
		t.Fatalf("GC left orphan tracked:\n%s", list)
	}
	if !strings.Contains(list, active) {
		t.Fatalf("GC removed the active worktree; list:\n%s", list)
	}
	if !dirExists(active) {
		t.Fatal("GC deleted the active worktree directory")
	}
}

func TestRebaseEpicOntoMain(t *testing.T) {
	r := newTestRepo(t)
	ctx := context.Background()
	if _, err := r.mgr.CreateEpicBranch(ctx, r.primary, 6); err != nil {
		t.Fatalf("CreateEpicBranch: %v", err)
	}
	// Advance main (and origin/main) after the epic was cut, on a distinct file.
	commitOn(t, r.primary, "main", "trunk.txt", "trunk moved\n", "advance main")
	run(t, r.primary, "git", "push", "origin", "main")
	// Put a commit on the epic on its own file (no conflict with trunk).
	commitOn(t, r.primary, "clex/epic-6", "epicwork.txt", "epic work\n", "epic work")
	run(t, r.primary, "git", "checkout", "main")

	if err := r.mgr.RebaseEpicOntoMain(ctx, r.primary, 6); err != nil {
		t.Fatalf("RebaseEpicOntoMain: %v", err)
	}
	// The epic now contains the advanced trunk file plus its own work.
	if !fileExists(filepath.Join(r.primary, "trunk.txt")) {
		t.Fatal("epic missing trunk.txt after rebase onto main")
	}
	// main itself was not moved to the epic tip (rebase must not touch main).
	epicTip := revParse(t, r.primary, "clex/epic-6")
	mainTip := revParse(t, r.primary, "main")
	if epicTip == mainTip {
		t.Fatal("rebase moved main to the epic tip; main must be untouched")
	}
	// But the epic is now a descendant of main (its base is main's tip).
	base := run(t, r.primary, "git", "merge-base", "clex/epic-6", "main")
	if strings.TrimSpace(base) != mainTip {
		t.Fatalf("epic not rebased onto main tip: merge-base %s, main %s", strings.TrimSpace(base), mainTip)
	}
}

func TestRebaseEpicOntoMainConflictAborts(t *testing.T) {
	r := newTestRepo(t)
	ctx := context.Background()
	if _, err := r.mgr.CreateEpicBranch(ctx, r.primary, 4); err != nil {
		t.Fatalf("CreateEpicBranch: %v", err)
	}
	// main and epic edit the same file → conflict on rebase.
	commitOn(t, r.primary, "main", "shared.txt", "main version\n", "main edits shared")
	run(t, r.primary, "git", "push", "origin", "main")
	commitOn(t, r.primary, "clex/epic-4", "shared.txt", "epic version\n", "epic edits shared")
	run(t, r.primary, "git", "checkout", "main")

	err := r.mgr.RebaseEpicOntoMain(ctx, r.primary, 4)
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("RebaseEpicOntoMain err = %v, want ErrConflict", err)
	}
	// The epic checkout must be clean (aborted), not mid-rebase.
	assertNotRebasing(t, r.primary)
	assertCleanTree(t, r.primary)
}
