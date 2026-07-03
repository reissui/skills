package pipeline

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Repo knowledge files live in .clex/context/, committed to the repo (spec:
// Context & token economy). This file implements the append/refresh helpers:
//   - LOG.md  — one line per landed issue ("have we done this before?" index).
//   - PATTERNS.md — appended when a planner resolves a recurring question.
//   - MAP.md  — refresh trigger after merges touch new areas.
//
// The helpers operate on a repo directory and are idempotent where the spec
// implies it: LOG.md will not append a duplicate line for an issue already
// recorded, so re-running the assemble/merge path after a crash does not double-
// log.

// contextDir is the committed knowledge-file directory, relative to a repo root.
const contextDir = ".clex/context"

const (
	logFile      = "LOG.md"
	patternsFile = "PATTERNS.md"
	mapFile      = "MAP.md"
)

// LogEntry is one landed-issue record for LOG.md.
type LogEntry struct {
	// Issue is the child issue number.
	Issue int
	// Summary is a short "what changed" phrase.
	Summary string
	// Where is a short "where" locator (e.g. the primary touched path).
	Where string
}

// line renders the entry as a single LOG.md line, e.g.
// "- #42 add rate limiter — internal/gh".
func (e LogEntry) line() string {
	parts := []string{fmt.Sprintf("- #%d", e.Issue)}
	if s := strings.TrimSpace(e.Summary); s != "" {
		parts = append(parts, s)
	}
	line := strings.Join(parts, " ")
	if w := strings.TrimSpace(e.Where); w != "" {
		line += " — " + w
	}
	return line
}

// AppendLog appends one line to <repoDir>/.clex/context/LOG.md for a landed
// issue, creating the file (and directory) if needed. It is idempotent: if a
// line for the same issue number already exists it is a no-op and returns false.
// The bool reports whether a line was written.
func AppendLog(repoDir string, e LogEntry) (bool, error) {
	path := filepath.Join(repoDir, contextDir, logFile)
	existing, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return false, fmt.Errorf("read LOG.md: %w", err)
	}
	if logHasIssue(string(existing), e.Issue) {
		return false, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return false, fmt.Errorf("mkdir context dir: %w", err)
	}
	var b strings.Builder
	b.Write(existing)
	if len(existing) > 0 && !strings.HasSuffix(string(existing), "\n") {
		b.WriteString("\n")
	}
	b.WriteString(e.line())
	b.WriteString("\n")
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		return false, fmt.Errorf("write LOG.md: %w", err)
	}
	return true, nil
}

// logHasIssue reports whether LOG.md content already has a line for issue n
// (matches a leading "- #<n> " or "- #<n>" token), so appends stay idempotent.
func logHasIssue(content string, n int) bool {
	needlePrefixSpace := fmt.Sprintf("- #%d ", n)
	needleExact := fmt.Sprintf("- #%d", n)
	for _, raw := range strings.Split(content, "\n") {
		line := strings.TrimSpace(raw)
		if strings.HasPrefix(line, needlePrefixSpace) || line == needleExact {
			return true
		}
	}
	return false
}

// AppendPattern appends a "how we do X here" note to PATTERNS.md under an
// optional heading, creating the file if needed. Unlike LOG.md it does not
// dedupe by content (planners decide what is worth recording); it does avoid
// appending an exact-duplicate trailing note so an accidental double call is a
// no-op. The bool reports whether text was written.
func AppendPattern(repoDir, heading, note string) (bool, error) {
	note = strings.TrimSpace(note)
	if note == "" {
		return false, nil
	}
	path := filepath.Join(repoDir, contextDir, patternsFile)
	existing, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return false, fmt.Errorf("read PATTERNS.md: %w", err)
	}
	var block strings.Builder
	if h := strings.TrimSpace(heading); h != "" {
		fmt.Fprintf(&block, "## %s\n\n", h)
	}
	block.WriteString(note)
	block.WriteString("\n")

	if strings.Contains(string(existing), strings.TrimRight(block.String(), "\n")) {
		return false, nil // exact block already present
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return false, fmt.Errorf("mkdir context dir: %w", err)
	}
	var b strings.Builder
	b.Write(existing)
	if len(existing) > 0 && !strings.HasSuffix(string(existing), "\n") {
		b.WriteString("\n")
	}
	if len(existing) > 0 {
		b.WriteString("\n")
	}
	b.WriteString(block.String())
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		return false, fmt.Errorf("write PATTERNS.md: %w", err)
	}
	return true, nil
}

// MapNeedsRefresh reports whether a merge touching the given globs introduced an
// area MAP.md does not yet mention, which is the trigger to regenerate the map
// (spec: MAP.md "refreshed incrementally after merges touch new areas"). It is a
// cheap heuristic: for each touched glob it derives the top directory segment and
// checks whether MAP.md already references it. Missing MAP.md always needs a
// refresh.
//
// The actual regeneration is a top-model run wired at a higher layer (#16/init);
// this stage only detects and reports the trigger, and returns the list of new
// areas so the caller can scope the refresh.
func MapNeedsRefresh(repoDir string, touched []string) (bool, []string, error) {
	path := filepath.Join(repoDir, contextDir, mapFile)
	content, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return true, topAreas(touched), nil
		}
		return false, nil, fmt.Errorf("read MAP.md: %w", err)
	}
	var missing []string
	seen := map[string]bool{}
	for _, area := range topAreas(touched) {
		if seen[area] {
			continue
		}
		seen[area] = true
		if !strings.Contains(string(content), area) {
			missing = append(missing, area)
		}
	}
	return len(missing) > 0, missing, nil
}

// topAreas maps touched globs to their top directory segment (e.g.
// "internal/gh/**" → "internal/gh"), de-duplicated in first-seen order. A bare
// wildcard or a top-level file yields nothing meaningful and is skipped.
func topAreas(touched []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, g := range touched {
		g = strings.TrimSpace(g)
		if g == "" || g == "**" || g == "*" {
			continue
		}
		// Strip glob suffixes to get a directory-ish prefix.
		g = strings.TrimSuffix(g, "/**")
		g = strings.TrimSuffix(g, "/*")
		// Take the first two path segments as the "area" when present, else the
		// first segment.
		segs := strings.Split(g, "/")
		var area string
		switch {
		case len(segs) >= 2 && !strings.Contains(segs[1], "*"):
			area = segs[0] + "/" + segs[1]
		default:
			area = segs[0]
		}
		if strings.Contains(area, "*") || area == "" {
			continue
		}
		if !seen[area] {
			seen[area] = true
			out = append(out, area)
		}
	}
	return out
}
