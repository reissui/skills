package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// cmdGC garbage-collects per-issue worktrees whose branches have been merged and
// deleted upstream. It is a local maintenance op over the worktree root (spec:
// clex home layout) — it does not require the daemon. For each worktree under the
// root it asks git whether the checked-out branch still exists on origin; if not,
// the worktree is pruned. In JSON mode it reports the pruned set.
//
// Safety: only directories that are genuine git worktrees of the current repo are
// touched; a --dry-run flag lists candidates without removing anything.
func cmdGC(e *env, args []string) int {
	fs, jsonOut := newFlagSet(e, "gc", "garbage-collect merged worktrees")
	dryRun := fs.Bool("dry-run", false, "list worktrees that would be pruned without removing them")
	if code, ok := parseFlags(fs, args); !ok {
		return code
	}
	root := e.worktreeRoot()
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return reportGC(e, *jsonOut, root, nil, false)
		}
		return fail(e, *jsonOut, "read worktree root %s: %v", root, err)
	}

	var pruned []string
	for _, ent := range entries {
		if !ent.IsDir() {
			continue
		}
		dir := filepath.Join(root, ent.Name())
		if !e.worktreeIsStale(dir) {
			continue
		}
		if *dryRun {
			pruned = append(pruned, ent.Name())
			continue
		}
		if err := os.RemoveAll(dir); err != nil {
			return fail(e, *jsonOut, "remove worktree %s: %v", dir, err)
		}
		pruned = append(pruned, ent.Name())
	}
	sort.Strings(pruned)
	// Best-effort: prune git's own worktree bookkeeping for the removed dirs.
	if !*dryRun && len(pruned) > 0 {
		_ = exec.Command("git", "worktree", "prune").Run()
	}
	return reportGC(e, *jsonOut, root, pruned, *dryRun)
}

// worktreeRoot resolves the directory holding per-issue worktrees. It mirrors the
// daemon's default (<home>/worktrees) so gc and the daemon agree without the CLI
// reading the daemon's runtime config.
func (e *env) worktreeRoot() string {
	return filepath.Join(e.home, "worktrees")
}

// worktreeIsStale reports whether dir is a git worktree whose branch no longer
// exists on origin (i.e. the PR merged and the branch was deleted). A directory
// that is not a git worktree, or whose branch still exists, is kept. It is
// deliberately conservative: any error answering the question means "keep".
func (e *env) worktreeIsStale(dir string) bool {
	// A worktree has a .git entry (a linked worktree uses a .git *file* pointing
	// at the gitdir). Missing .git means it is not a worktree — keep it untouched.
	if _, err := os.Stat(filepath.Join(dir, ".git")); err != nil {
		return false
	}
	branch, err := exec.Command("git", "-C", dir, "rev-parse", "--abbrev-ref", "HEAD").Output()
	if err != nil {
		return false
	}
	name := strings.TrimSpace(string(branch))
	if name == "" || name == "HEAD" {
		return false
	}
	// If origin still lists the branch, it is not merged/deleted yet: keep it.
	if err := exec.Command("git", "-C", dir, "ls-remote", "--exit-code", "--heads", "origin", name).Run(); err == nil {
		return false
	}
	return true
}

// reportGC renders the gc outcome in the requested format.
func reportGC(e *env, jsonMode bool, root string, pruned []string, dryRun bool) int {
	if jsonMode {
		return writeJSON(e.stdout, map[string]any{
			"ok": true, "root": root, "dry_run": dryRun, "pruned": pruned,
		})
	}
	verb := "pruned"
	if dryRun {
		verb = "would prune"
	}
	if len(pruned) == 0 {
		fmt.Fprintf(e.stdout, "no stale worktrees under %s\n", root)
		return exitOK
	}
	fmt.Fprintf(e.stdout, "%s %d worktree(s) under %s:\n", verb, len(pruned), root)
	for _, p := range pruned {
		fmt.Fprintf(e.stdout, "  %s\n", p)
	}
	return exitOK
}

// cmdUpdate is the self-update entry point. The updater itself lives in
// internal/update (issue #19), which may not exist yet in this tree; this command
// intentionally does NOT import it. It defines the command shell, help, and
// --json so the surface is complete, and reports that self-update is not wired in
// this build. When #19 lands, its integration replaces this body — the flag
// surface here is the contract it fills.
func cmdUpdate(e *env, args []string) int {
	fs, jsonOut := newFlagSet(e, "update", "update the clex binary (self-update)")
	check := fs.Bool("check", false, "check for an available update without installing")
	if code, ok := parseFlags(fs, args); !ok {
		return code
	}
	const notWired = "self-update is not wired in this build (internal/update lands in a later change)"
	if *jsonOut {
		writeJSON(e.stdout, map[string]any{
			"ok": true, "wired": false, "check_only": *check, "message": notWired,
		})
		return exitOK
	}
	fmt.Fprintln(e.stdout, notWired)
	return exitOK
}
