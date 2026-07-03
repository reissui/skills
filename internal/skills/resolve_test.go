package skills

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeSkill creates dir/<name>/SKILL.md with the given marker body so tests can
// tell which precedence level a resolved skill came from.
func writeSkill(t *testing.T, dir, name, marker string) string {
	t.Helper()
	sdir := filepath.Join(dir, name)
	if err := os.MkdirAll(sdir, 0o700); err != nil {
		t.Fatal(err)
	}
	body := "---\nname: " + name + "\ndescription: test\n---\n" + marker + "\n"
	if err := os.WriteFile(filepath.Join(sdir, "SKILL.md"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return sdir
}

// TestResolvePrecedence covers the discovery order repo .clex/skills → user root
// → bundled: a same-named skill present at all three levels resolves to the repo
// copy (acceptance criterion 2).
func TestResolvePrecedence(t *testing.T) {
	repoDir := t.TempDir()
	userRoot := t.TempDir()

	// Same skill name at repo and user levels; "clex-plan" also exists bundled.
	writeSkill(t, filepath.Join(repoDir, repoSkillsRel), "clex-plan", "REPO")
	writeSkill(t, userRoot, "clex-plan", "USER")

	got := Resolve([]string{"clex-plan"}, repoDir, userRoot)
	if len(got) != 1 {
		t.Fatalf("want 1 resolved skill, got %d: %+v", len(got), got)
	}
	// Repo wins over user and bundled.
	wantPath := filepath.Join(repoDir, repoSkillsRel, "clex-plan")
	if got[0].Path != wantPath {
		t.Errorf("repo should win: got %q want %q", got[0].Path, wantPath)
	}
	data, err := os.ReadFile(filepath.Join(got[0].Path, "SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "REPO") {
		t.Errorf("resolved skill body is not the repo copy: %s", data)
	}
}

// TestResolveFallsThroughLevels checks the two lower fallbacks independently.
func TestResolveFallsThroughLevels(t *testing.T) {
	repoDir := t.TempDir()
	userRoot := t.TempDir()

	// Only at user level.
	writeSkill(t, userRoot, "grill-me", "USER")

	t.Run("user level when repo absent", func(t *testing.T) {
		got := Resolve([]string{"grill-me"}, repoDir, userRoot)
		if len(got) != 1 || got[0].Path != filepath.Join(userRoot, "grill-me") {
			t.Fatalf("want user-level resolution, got %+v", got)
		}
	})

	t.Run("bundled level when repo and user absent", func(t *testing.T) {
		got := Resolve([]string{"clex-issue-lint"}, repoDir, userRoot)
		if len(got) != 1 {
			t.Fatalf("want bundled resolution, got %+v", got)
		}
		if !isBundledPath(got[0].Path) {
			t.Errorf("expected bundled sentinel path, got %q", got[0].Path)
		}
	})

	t.Run("unknown skill is skipped", func(t *testing.T) {
		got := Resolve([]string{"does-not-exist"}, repoDir, userRoot)
		if len(got) != 0 {
			t.Errorf("unknown skill should resolve to nothing, got %+v", got)
		}
	})
}

// TestResolveOrderAndDedup verifies input order is preserved and duplicates and
// empties are dropped.
func TestResolveOrderAndDedup(t *testing.T) {
	got := Resolve([]string{"clex-plan", "", "clex-issue-lint", "clex-plan"}, "", "")
	if len(got) != 2 {
		t.Fatalf("want 2 (deduped, empties dropped), got %d: %+v", len(got), got)
	}
	if got[0].Name != "clex-plan" || got[1].Name != "clex-issue-lint" {
		t.Errorf("order not preserved: %+v", got)
	}
}

// TestSymlinkInto covers injecting on-disk and bundled skills into a worktree's
// .claude/skills, including replacement of an existing entry (acceptance
// criterion 3).
func TestSymlinkInto(t *testing.T) {
	worktree := t.TempDir()
	src := t.TempDir()
	onDisk := writeSkill(t, src, "grill-me", "ONDISK")

	dirs := []SkillDir{
		{Name: "grill-me", Path: onDisk},
		{Name: "clex-plan", Path: bundledVirtualPath("clex-plan")}, // bundled → materialized
	}
	if err := SymlinkInto(worktree, dirs); err != nil {
		t.Fatalf("SymlinkInto: %v", err)
	}

	// On-disk skill becomes a symlink resolving to the source SKILL.md.
	linkPath := filepath.Join(worktree, ".claude", "skills", "grill-me")
	if fi, err := os.Lstat(linkPath); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Errorf("grill-me should be a symlink, err=%v", err)
	}
	body, err := os.ReadFile(filepath.Join(linkPath, "SKILL.md"))
	if err != nil || !strings.Contains(string(body), "ONDISK") {
		t.Errorf("symlinked skill unreadable or wrong: %s err=%v", body, err)
	}

	// Bundled skill is materialized as a real file (not a symlink).
	bundledPath := filepath.Join(worktree, ".claude", "skills", "clex-plan", "SKILL.md")
	bdata, err := os.ReadFile(bundledPath)
	if err != nil {
		t.Fatalf("bundled skill not materialized: %v", err)
	}
	if !strings.Contains(string(bdata), "could a modest local model") {
		t.Errorf("materialized bundled skill missing contract text")
	}

	// Re-injecting replaces cleanly (idempotent, no error on existing entry).
	if err := SymlinkInto(worktree, dirs); err != nil {
		t.Fatalf("second SymlinkInto should replace, got %v", err)
	}
}

// TestRenderAgentsMD covers rendering the managed block, idempotence, block
// replacement, and preservation of surrounding user content (acceptance
// criterion 3).
func TestRenderAgentsMD(t *testing.T) {
	dirs := []SkillDir{
		{Name: "clex-plan", Path: bundledVirtualPath("clex-plan")},
		{Name: "grill-me", Path: "/somewhere/grill-me"},
	}

	t.Run("creates file and lists skills", func(t *testing.T) {
		wt := t.TempDir()
		if err := RenderAgentsMD(wt, dirs); err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(filepath.Join(wt, "AGENTS.md"))
		if err != nil {
			t.Fatal(err)
		}
		s := string(data)
		for _, want := range []string{agentsMarkerBegin, agentsMarkerEnd, "clex-plan", "grill-me", "## clex skills"} {
			if !strings.Contains(s, want) {
				t.Errorf("AGENTS.md missing %q:\n%s", want, s)
			}
		}
	})

	t.Run("idempotent", func(t *testing.T) {
		wt := t.TempDir()
		if err := RenderAgentsMD(wt, dirs); err != nil {
			t.Fatal(err)
		}
		first, _ := os.ReadFile(filepath.Join(wt, "AGENTS.md"))
		if err := RenderAgentsMD(wt, dirs); err != nil {
			t.Fatal(err)
		}
		second, _ := os.ReadFile(filepath.Join(wt, "AGENTS.md"))
		if string(first) != string(second) {
			t.Errorf("render not idempotent:\nfirst:\n%s\nsecond:\n%s", first, second)
		}
		// Order-independence: same skills in a different order render identically.
		reordered := []SkillDir{dirs[1], dirs[0]}
		if err := RenderAgentsMD(wt, reordered); err != nil {
			t.Fatal(err)
		}
		third, _ := os.ReadFile(filepath.Join(wt, "AGENTS.md"))
		if string(second) != string(third) {
			t.Errorf("render depends on caller order:\n%s\nvs\n%s", second, third)
		}
	})

	t.Run("preserves surrounding content and replaces block", func(t *testing.T) {
		wt := t.TempDir()
		path := filepath.Join(wt, "AGENTS.md")
		preamble := "# My Repo\n\nHand-written guidance the user owns.\n"
		if err := os.WriteFile(path, []byte(preamble), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := RenderAgentsMD(wt, dirs); err != nil {
			t.Fatal(err)
		}
		// Now render with a different skill set; user preamble must survive and
		// only the managed block changes.
		if err := RenderAgentsMD(wt, []SkillDir{{Name: "to-prd", Path: "/x/to-prd"}}); err != nil {
			t.Fatal(err)
		}
		data, _ := os.ReadFile(path)
		s := string(data)
		if !strings.Contains(s, "Hand-written guidance the user owns.") {
			t.Errorf("user content lost:\n%s", s)
		}
		if !strings.Contains(s, "to-prd") {
			t.Errorf("new block not rendered:\n%s", s)
		}
		if strings.Contains(s, "grill-me") {
			t.Errorf("stale block content survived replacement:\n%s", s)
		}
		if strings.Count(s, agentsMarkerBegin) != 1 {
			t.Errorf("expected exactly one managed block, got %d:\n%s", strings.Count(s, agentsMarkerBegin), s)
		}
	})

	t.Run("empty skill list renders placeholder", func(t *testing.T) {
		wt := t.TempDir()
		if err := RenderAgentsMD(wt, nil); err != nil {
			t.Fatal(err)
		}
		data, _ := os.ReadFile(filepath.Join(wt, "AGENTS.md"))
		if !strings.Contains(string(data), "No skills required") {
			t.Errorf("empty render should note no skills:\n%s", data)
		}
	})
}
