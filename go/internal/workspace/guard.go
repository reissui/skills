package workspace

import (
	"context"
	"os"
	"path/filepath"
	"strings"
)

// guardPrimaryOnly asserts that dir is a repository's primary checkout — the
// place from which epics are cut and worktrees are administered — and not a
// linked worktree. Administration (CreateEpicBranch, CreateWorktree, GC,
// RebaseEpicOntoMain) runs here.
//
// It deliberately does NOT refuse a checkout that is on main: a primary checkout
// is routinely on main, and these operations create refs or add worktrees
// without ever moving or committing to main (the epic branch is created with
// "branch --force" and rebases target main rather than mutate it). The
// no-touching-main invariant is upheld by construction here and enforced
// positively for runner worktrees by guardWorktreeOnly.
//
// It is the mirror image of guardWorktreeOnly: the two guards partition every
// git directory into "primary checkout" and "linked worktree", and each
// operation declares which side it needs.
func (m *Manager) guardPrimaryOnly(ctx context.Context, dir string) error {
	linked, err := m.isLinkedWorktree(ctx, dir)
	if err != nil {
		return err
	}
	if linked {
		// A linked worktree is not a primary checkout; refuse rather than
		// administer worktrees from inside another worktree.
		return ErrPrimaryCheckout
	}
	return nil
}

// guardWorktreeOnly asserts that dir is a linked worktree (never the primary
// checkout) and is not the main branch. Rebase/cleanup of an issue branch runs
// here — runners are only ever given worktrees, so operating on the primary
// checkout is a bug and returns ErrPrimaryCheckout.
func (m *Manager) guardWorktreeOnly(ctx context.Context, dir string) error {
	linked, err := m.isLinkedWorktree(ctx, dir)
	if err != nil {
		return err
	}
	if !linked {
		return ErrPrimaryCheckout
	}
	branch, err := m.currentBranch(ctx, dir)
	if err != nil {
		return err
	}
	if branch == MainBranch {
		return ErrMainBranch
	}
	return nil
}

// isLinkedWorktree reports whether dir is a linked worktree as opposed to the
// repository's primary checkout. git records this as the boolean config
// core.bare's cousin: "git rev-parse --is-inside-work-tree" is true for both, so
// instead we compare the common git dir with the per-worktree git dir — they
// differ only for linked worktrees.
func (m *Manager) isLinkedWorktree(ctx context.Context, dir string) (bool, error) {
	gitDir, err := m.git(ctx, dir, "rev-parse", "--absolute-git-dir")
	if err != nil {
		return false, err
	}
	commonDir, err := m.git(ctx, dir, "rev-parse", "--git-common-dir")
	if err != nil {
		return false, err
	}
	g := strings.TrimSpace(gitDir)
	c := strings.TrimSpace(commonDir)
	// --git-common-dir may be relative to dir; resolve it for comparison.
	if !filepath.IsAbs(c) {
		c = filepath.Join(dir, c)
	}
	return !sameDir(g, c), nil
}

// currentBranch returns the short branch name checked out in dir, or "" when the
// HEAD is detached.
func (m *Manager) currentBranch(ctx context.Context, dir string) (string, error) {
	out, err := m.git(ctx, dir, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return "", err
	}
	b := strings.TrimSpace(out)
	if b == "HEAD" {
		return "", nil // detached
	}
	return b, nil
}

// primaryCheckoutOf returns the primary checkout directory that owns the linked
// worktree dir. It is the parent of the common git dir (…/.git).
func (m *Manager) primaryCheckoutOf(ctx context.Context, dir string) (string, error) {
	commonDir, err := m.git(ctx, dir, "rev-parse", "--git-common-dir")
	if err != nil {
		return "", err
	}
	c := strings.TrimSpace(commonDir)
	if !filepath.IsAbs(c) {
		c = filepath.Join(dir, c)
	}
	// commonDir is the primary repo's .git directory; its parent is the primary
	// working tree.
	return filepath.Dir(filepath.Clean(c)), nil
}

// trunkBase returns the ref to base epic branches and epic rebases on: the
// freshly fetched origin/main when a remote exists, otherwise the local main
// ref. Falling back to local main keeps test repositories (whose "origin" is a
// sibling bare/clone directory) working without a network.
func (m *Manager) trunkBase(ctx context.Context, dir string) string {
	if _, err := m.git(ctx, dir, "rev-parse", "--verify", "--quiet", "refs/remotes/origin/"+MainBranch); err == nil {
		return "origin/" + MainBranch
	}
	return MainBranch
}

// repoKey reduces a repository identifier (an "owner/name" slug or a filesystem
// path) to the single path component used to group its worktrees: its base
// name. "reissui/clex" and "/src/clex" both key to "clex".
func repoKey(repo string) string {
	repo = strings.TrimRight(repo, "/")
	return sanitizeSlug(filepath.Base(repo))
}

// sanitizeSlug turns an arbitrary label into a safe single git-ref / path
// component: lowercased, with runs of non-alphanumeric characters collapsed to
// single hyphens and leading/trailing hyphens trimmed. Empty input yields
// "unnamed" so a ref/path component is always present.
func sanitizeSlug(s string) string {
	var b strings.Builder
	lastHyphen := false
	for _, r := range strings.ToLower(s) {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'):
			b.WriteRune(r)
			lastHyphen = false
		default:
			if !lastHyphen && b.Len() > 0 {
				b.WriteByte('-')
				lastHyphen = true
			}
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		return "unnamed"
	}
	return out
}

// worktreeEntry is one record from "git worktree list --porcelain".
type worktreeEntry struct {
	path string
	bare bool
}

// parseWorktreeList parses the porcelain output of "git worktree list". Records
// are separated by blank lines; the "worktree" line carries the path and a bare
// "bare" line marks the bare primary of a bare repository.
func parseWorktreeList(out string) []worktreeEntry {
	var entries []worktreeEntry
	var cur *worktreeEntry
	flush := func() {
		if cur != nil {
			entries = append(entries, *cur)
			cur = nil
		}
	}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimRight(line, "\r")
		switch {
		case line == "":
			flush()
		case strings.HasPrefix(line, "worktree "):
			flush()
			cur = &worktreeEntry{path: strings.TrimPrefix(line, "worktree ")}
		case line == "bare":
			if cur != nil {
				cur.bare = true
			}
		}
	}
	flush()
	return entries
}

// dirExists reports whether path exists and is a directory.
func dirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// sameDir reports whether two paths refer to the same directory, comparing
// cleaned absolute forms and, where possible, resolved symlinks (macOS /tmp is a
// symlink to /private/tmp, so raw string comparison is not enough).
func sameDir(a, b string) bool {
	ca, cb := filepath.Clean(a), filepath.Clean(b)
	if ca == cb {
		return true
	}
	ra, erra := filepath.EvalSymlinks(ca)
	rb, errb := filepath.EvalSymlinks(cb)
	if erra == nil && errb == nil {
		return ra == rb
	}
	return false
}

// underDir reports whether path is dir or lies beneath it, resolving symlinks so
// that comparisons under /tmp behave on macOS.
func underDir(path, dir string) bool {
	rp, err1 := filepath.EvalSymlinks(filepath.Clean(path))
	if err1 != nil {
		rp = filepath.Clean(path)
	}
	rd, err2 := filepath.EvalSymlinks(filepath.Clean(dir))
	if err2 != nil {
		rd = filepath.Clean(dir)
	}
	rel, err := filepath.Rel(rd, rp)
	if err != nil {
		return false
	}
	return rel == "." || (!strings.HasPrefix(rel, ".."+string(filepath.Separator)) && rel != "..")
}
