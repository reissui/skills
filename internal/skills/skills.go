// Package skills owns clex's skill layer: the bundled skill content, the
// installer that materializes it into the user's skills root (and fetches the
// vendored third-party skills), and the resolution/injection helpers that the
// runner adapters call to make skills available inside a worktree.
//
// Two clex-authored skills ship in-repo under skills/ and are embedded here:
// clex-plan (the "dumb issue" planning contract) and clex-issue-lint (the
// pre-gate scorer). The installer additionally fetches Matt Pocock's to-prd,
// to-issues, grill-me, and grill-with-docs; the network step is injectable so
// tests never touch the network, and every fetched file is pinned by URL,
// version, and sha256 in a lockfile (spec: Skills layer, Security model —
// supply chain).
//
// Relevant spec sections: Skills layer; The "dumb issue" contract; Security
// model (Supply chain); Context & token economy.
package skills

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// SkillDir is a resolved skill: its short name (the directory basename, e.g.
// "clex-plan") and the absolute path to the directory that holds its SKILL.md.
type SkillDir struct {
	Name string // directory basename, e.g. "clex-plan"
	Path string // absolute path to the skill directory
}

// Discovery precedence directory names (spec: Skills layer — discovery order
// repo .clex/skills → user ~/.clex/skills → bundled).
const (
	// repoSkillsRel is the per-repo skills directory, relative to a repo root.
	repoSkillsRel = ".clex/skills"
)

// Resolve returns the directory for each requested skill name, applying clex's
// discovery precedence: a skill present in the repo's .clex/skills wins over the
// same-named skill in the user root, which wins over the bundled copy (spec:
// Skills layer). names are resolved independently; unknown names (found at no
// level) are skipped. repoDir is the repo root whose .clex/skills is consulted
// first; pass "" to skip the repo level. userRoot is the user skills root
// (typically ~/.clex/skills). The bundled level is always available in-process.
//
// The returned slice preserves the order of names, de-duplicated (first
// occurrence wins).
func Resolve(names []string, repoDir, userRoot string) []SkillDir {
	seen := make(map[string]bool, len(names))
	out := make([]SkillDir, 0, len(names))
	for _, name := range names {
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		if dir, ok := resolveOne(name, repoDir, userRoot); ok {
			out = append(out, dir)
		}
	}
	return out
}

// resolveOne finds a single skill by name across the precedence chain. The
// bundled level is reported with its logical embed path (prefixed to make it
// recognizable); callers that need bundled bytes read them via BundledFS.
func resolveOne(name, repoDir, userRoot string) (SkillDir, bool) {
	// 1. repo .clex/skills
	if repoDir != "" {
		if p := skillDirIfPresent(filepath.Join(repoDir, repoSkillsRel, name)); p != "" {
			return SkillDir{Name: name, Path: p}, true
		}
	}
	// 2. user root
	if userRoot != "" {
		if p := skillDirIfPresent(filepath.Join(userRoot, name)); p != "" {
			return SkillDir{Name: name, Path: p}, true
		}
	}
	// 3. bundled (embedded). Reported as a virtual path under the embed root so
	// it is distinguishable from on-disk skills.
	if bundledHas(name) {
		return SkillDir{Name: name, Path: bundledVirtualPath(name)}, true
	}
	return SkillDir{}, false
}

// skillDirIfPresent returns dir if it is a directory containing a SKILL.md,
// else "".
func skillDirIfPresent(dir string) string {
	info, err := os.Stat(filepath.Join(dir, skillFileName))
	if err != nil || info.IsDir() {
		return ""
	}
	return dir
}

// SymlinkInto makes each skill directory available inside worktree by
// symlinking it into worktree/.claude/skills/<name>. This is the Claude Code
// injection mechanism (spec: Skills layer — Claude Code: symlink into the
// worktree's .claude/skills). Bundled (embedded) skills have no on-disk source
// to symlink, so they are materialized (copied) into place instead. Existing
// entries for a given skill name are replaced. The .claude/skills directory is
// created if absent.
func SymlinkInto(worktree string, dirs []SkillDir) error {
	dest := filepath.Join(worktree, ".claude", "skills")
	if err := os.MkdirAll(dest, 0o700); err != nil {
		return fmt.Errorf("skills: create %s: %w", dest, err)
	}
	for _, d := range dirs {
		target := filepath.Join(dest, d.Name)
		if err := os.RemoveAll(target); err != nil {
			return fmt.Errorf("skills: clear %s: %w", target, err)
		}
		if isBundledPath(d.Path) {
			// No on-disk source: materialize the embedded copy.
			if err := writeBundledSkill(d.Name, target); err != nil {
				return err
			}
			continue
		}
		abs, err := filepath.Abs(d.Path)
		if err != nil {
			return fmt.Errorf("skills: abs %s: %w", d.Path, err)
		}
		if err := os.Symlink(abs, target); err != nil {
			return fmt.Errorf("skills: symlink %s -> %s: %w", target, abs, err)
		}
	}
	return nil
}

// Marker lines delimiting the clex-managed block in AGENTS.md. Everything
// between them is owned by clex and rewritten idempotently; content outside is
// never touched (spec: Skills layer — Codex: rendered into AGENTS.md).
const (
	agentsMarkerBegin = "<!-- clex:skills:begin -->"
	agentsMarkerEnd   = "<!-- clex:skills:end -->"
	agentsFileName    = "AGENTS.md"
	skillFileName     = "SKILL.md"
)

// RenderAgentsMD writes the clex-managed skills block into worktree/AGENTS.md,
// listing each skill so a Codex runner picks them up (spec: Skills layer — the
// Codex injection mechanism). The block is delimited by stable marker comments
// and the operation is idempotent: calling it again with the same dirs leaves
// the file byte-identical, and calling it with different dirs replaces only the
// managed block, preserving any surrounding user content. A missing AGENTS.md
// is created.
func RenderAgentsMD(worktree string, dirs []SkillDir) error {
	path := filepath.Join(worktree, agentsFileName)
	existing, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("skills: read %s: %w", path, err)
	}
	updated := replaceManagedBlock(string(existing), renderBlock(dirs))
	if err := os.MkdirAll(worktree, 0o700); err != nil {
		return fmt.Errorf("skills: create %s: %w", worktree, err)
	}
	if err := os.WriteFile(path, []byte(updated), 0o600); err != nil {
		return fmt.Errorf("skills: write %s: %w", path, err)
	}
	return nil
}

// renderBlock builds the managed block body (including the begin/end markers)
// for the given skills. The skill list is sorted so output is deterministic
// regardless of caller ordering, which keeps the render idempotent.
func renderBlock(dirs []SkillDir) string {
	names := make([]string, 0, len(dirs))
	for _, d := range dirs {
		names = append(names, d.Name)
	}
	sort.Strings(names)

	var b strings.Builder
	b.WriteString(agentsMarkerBegin)
	b.WriteString("\n## clex skills\n\n")
	if len(names) == 0 {
		b.WriteString("_No skills required for this task._\n")
	} else {
		b.WriteString("The following skills are available for this task; consult a skill's SKILL.md before acting in its area:\n\n")
		for _, n := range names {
			fmt.Fprintf(&b, "- `%s`\n", n)
		}
	}
	b.WriteString(agentsMarkerEnd)
	return b.String()
}

// replaceManagedBlock returns existing with the clex-managed block replaced by
// block. If no managed block is present, block is appended (with a separating
// blank line when existing is non-empty). If the markers are present, only the
// region between them (inclusive) is swapped, leaving surrounding text intact.
func replaceManagedBlock(existing, block string) string {
	begin := strings.Index(existing, agentsMarkerBegin)
	end := strings.Index(existing, agentsMarkerEnd)
	if begin >= 0 && end > begin {
		endAfter := end + len(agentsMarkerEnd)
		return existing[:begin] + block + existing[endAfter:]
	}
	if strings.TrimSpace(existing) == "" {
		return block + "\n"
	}
	sep := "\n"
	if !strings.HasSuffix(existing, "\n") {
		sep = "\n\n"
	} else if !strings.HasSuffix(existing, "\n\n") {
		sep = "\n"
	}
	return existing + sep + block + "\n"
}
