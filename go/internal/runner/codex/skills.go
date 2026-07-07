package codex

import (
	"os"
	"sort"
	"strings"
)

// skillsMarker delimits the clex-managed skills region of a worktree's
// AGENTS.md. Everything from this marker to end-of-file is owned by clex and
// rewritten on every Run, which is what makes injection idempotent: any prior
// block is replaced, never appended to (spec: Skills layer — Codex rendered into
// AGENTS.md, idempotent on re-run).
const skillsMarker = "<!-- clex:skills -->"

// InjectSkills renders the named skills into dir/AGENTS.md below the clex marker.
// Content above the marker (the repo's own AGENTS.md) is preserved untouched;
// the marker and everything after it are regenerated from skills. Running twice
// with the same skills is a no-op in effect — exactly one skills block results.
//
// This issue's scope is a plain concatenation with headers; the richer rendering
// helper is coordinated with the skills layer (#12). skills are deduplicated and
// order-stable so provider-side prompt caching can hit (spec: Stable prompts).
func InjectSkills(dir string, skills []string) error {
	path := agentsFile(dir)

	existing, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}

	preamble := stripSkillsBlock(string(existing))
	block := renderSkills(skills)

	var b strings.Builder
	if preamble != "" {
		b.WriteString(preamble)
		if !strings.HasSuffix(preamble, "\n") {
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}
	b.WriteString(block)

	// 0644: AGENTS.md is worktree content the CLI must read; the worktree dir
	// itself carries the restrictive perms (spec: config/spool 0700/0600).
	return os.WriteFile(path, []byte(b.String()), 0o644)
}

// stripSkillsBlock returns content with the clex skills marker and everything
// after it removed, trimming trailing blank lines. Content without the marker is
// returned unchanged (minus trailing whitespace).
func stripSkillsBlock(content string) string {
	if idx := strings.Index(content, skillsMarker); idx >= 0 {
		content = content[:idx]
	}
	return strings.TrimRight(content, "\n \t")
}

// renderSkills produces the marker-led skills block: the marker, a heading, and
// one section per skill. Names are trimmed, de-duplicated, and sorted so the
// output is deterministic for a given skill set.
func renderSkills(skills []string) string {
	var b strings.Builder
	b.WriteString(skillsMarker)
	b.WriteString("\n# clex skills\n\n")
	b.WriteString("The following skills are injected by clex for this task. Follow them.\n")

	for _, name := range dedupeSorted(skills) {
		b.WriteString("\n## ")
		b.WriteString(name)
		b.WriteString("\n")
	}
	// Trailing newline keeps the file POSIX-clean.
	if !strings.HasSuffix(b.String(), "\n") {
		b.WriteString("\n")
	}
	return b.String()
}

// dedupeSorted returns the unique, non-empty, trimmed skill names in sorted
// order.
func dedupeSorted(skills []string) []string {
	seen := make(map[string]bool, len(skills))
	out := make([]string, 0, len(skills))
	for _, s := range skills {
		s = strings.TrimSpace(s)
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}
