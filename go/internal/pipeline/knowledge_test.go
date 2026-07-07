package pipeline

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func readContext(t *testing.T, repoDir, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(repoDir, contextDir, name))
	if err != nil {
		if os.IsNotExist(err) {
			return ""
		}
		t.Fatalf("read %s: %v", name, err)
	}
	return string(b)
}

// TestAppendLogOneLinePerIssue: LOG.md gains exactly one line per landed issue,
// and re-appending the same issue is a no-op (idempotent) — the lifecycle
// acceptance criterion.
func TestAppendLogOneLinePerIssue(t *testing.T) {
	repo := t.TempDir()

	wrote, err := AppendLog(repo, LogEntry{Issue: 101, Summary: "add widget store", Where: "internal/widget"})
	if err != nil || !wrote {
		t.Fatalf("first append: wrote=%v err=%v", wrote, err)
	}
	wrote2, err := AppendLog(repo, LogEntry{Issue: 102, Summary: "add widget API", Where: "internal/api"})
	if err != nil || !wrote2 {
		t.Fatalf("second append: wrote=%v err=%v", wrote2, err)
	}

	content := readContext(t, repo, logFile)
	lines := nonEmptyLines(content)
	if len(lines) != 2 {
		t.Fatalf("LOG.md has %d lines, want 2:\n%s", len(lines), content)
	}
	if !strings.HasPrefix(lines[0], "- #101 add widget store — internal/widget") {
		t.Errorf("line 0 = %q", lines[0])
	}
	if !strings.HasPrefix(lines[1], "- #102 add widget API — internal/api") {
		t.Errorf("line 1 = %q", lines[1])
	}

	// Idempotent: re-appending #101 must not add a duplicate line.
	wrote3, err := AppendLog(repo, LogEntry{Issue: 101, Summary: "add widget store", Where: "internal/widget"})
	if err != nil {
		t.Fatalf("re-append: %v", err)
	}
	if wrote3 {
		t.Error("re-appending an existing issue must be a no-op")
	}
	if got := len(nonEmptyLines(readContext(t, repo, logFile))); got != 2 {
		t.Errorf("LOG.md grew to %d lines after duplicate append", got)
	}
}

// TestAppendLogLifecycle simulates a small epic lifecycle: three issues land in
// sequence; LOG.md ends with exactly three lines in order.
func TestAppendLogLifecycle(t *testing.T) {
	repo := t.TempDir()
	for _, e := range []LogEntry{
		{Issue: 1, Summary: "scaffold", Where: "cmd"},
		{Issue: 2, Summary: "core types", Where: "internal/core"},
		{Issue: 3, Summary: "gh client", Where: "internal/gh"},
	} {
		if _, err := AppendLog(repo, e); err != nil {
			t.Fatalf("append #%d: %v", e.Issue, err)
		}
	}
	lines := nonEmptyLines(readContext(t, repo, logFile))
	if len(lines) != 3 {
		t.Fatalf("want 3 lines, got %d", len(lines))
	}
	for i, want := range []string{"- #1 ", "- #2 ", "- #3 "} {
		if !strings.HasPrefix(lines[i], want) {
			t.Errorf("line %d = %q, want prefix %q", i, lines[i], want)
		}
	}
}

// TestAppendPattern appends a note under a heading and dedupes exact repeats.
func TestAppendPattern(t *testing.T) {
	repo := t.TempDir()
	wrote, err := AppendPattern(repo, "Error handling", "Wrap errors with %w and match with errors.Is.")
	if err != nil || !wrote {
		t.Fatalf("append pattern: wrote=%v err=%v", wrote, err)
	}
	content := readContext(t, repo, patternsFile)
	if !strings.Contains(content, "## Error handling") || !strings.Contains(content, "errors.Is") {
		t.Errorf("PATTERNS.md missing content:\n%s", content)
	}
	// Exact duplicate is a no-op.
	wrote2, _ := AppendPattern(repo, "Error handling", "Wrap errors with %w and match with errors.Is.")
	if wrote2 {
		t.Error("exact-duplicate pattern append should be a no-op")
	}
	// A different note appends.
	wrote3, _ := AppendPattern(repo, "Testing", "Table-driven tests with fakes.")
	if !wrote3 {
		t.Error("distinct pattern should append")
	}
}

// TestMapNeedsRefresh: missing MAP.md always needs refresh; an existing MAP.md
// needs refresh only for areas it does not mention, and returns the new areas.
func TestMapNeedsRefresh(t *testing.T) {
	repo := t.TempDir()

	// Missing MAP.md → refresh needed, new areas derived from touched globs.
	need, areas, err := MapNeedsRefresh(repo, []string{"internal/widget/**", "internal/api/**"})
	if err != nil {
		t.Fatalf("MapNeedsRefresh: %v", err)
	}
	if !need {
		t.Error("missing MAP.md must need refresh")
	}
	if !containsStr(areas, "internal/widget") || !containsStr(areas, "internal/api") {
		t.Errorf("areas = %v, want widget+api", areas)
	}

	// Now write a MAP.md mentioning only internal/widget.
	if err := os.MkdirAll(filepath.Join(repo, contextDir), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, contextDir, mapFile), []byte("# Map\n\ninternal/widget holds widgets.\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// A merge touching only internal/widget → no refresh.
	need, _, _ = MapNeedsRefresh(repo, []string{"internal/widget/**"})
	if need {
		t.Error("MAP.md already mentions internal/widget; no refresh expected")
	}

	// A merge touching a new area internal/api → refresh with that area.
	need, areas, _ = MapNeedsRefresh(repo, []string{"internal/widget/**", "internal/api/**"})
	if !need {
		t.Error("new area internal/api should trigger refresh")
	}
	if len(areas) != 1 || areas[0] != "internal/api" {
		t.Errorf("new areas = %v, want [internal/api]", areas)
	}

	// A bare wildcard yields no meaningful area and does not, by itself, force a
	// refresh when MAP.md exists.
	need, areas, _ = MapNeedsRefresh(repo, []string{"**"})
	if need || len(areas) != 0 {
		t.Errorf("bare wildcard: need=%v areas=%v, want false/empty", need, areas)
	}
}

func nonEmptyLines(s string) []string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		if strings.TrimSpace(l) != "" {
			out = append(out, l)
		}
	}
	return out
}

func containsStr(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}
