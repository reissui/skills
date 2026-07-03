package codex

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/reissui/clex/internal/core"
)

func readAgents(t *testing.T, dir string) string {
	t.Helper()
	b, err := os.ReadFile(agentsFile(dir))
	if err != nil {
		t.Fatalf("read AGENTS.md: %v", err)
	}
	return string(b)
}

// Injecting into a worktree with no AGENTS.md creates one containing exactly one
// marker and a section per skill.
func TestInjectSkills_CreatesFile(t *testing.T) {
	dir := t.TempDir()
	if err := InjectSkills(dir, []string{"clex-plan", "to-issues"}); err != nil {
		t.Fatalf("InjectSkills: %v", err)
	}
	got := readAgents(t, dir)

	if strings.Count(got, skillsMarker) != 1 {
		t.Errorf("want exactly one marker, got %d\n%s", strings.Count(got, skillsMarker), got)
	}
	if !strings.Contains(got, "## clex-plan") || !strings.Contains(got, "## to-issues") {
		t.Errorf("missing a skill section:\n%s", got)
	}
}

// Running injection twice with the same skills yields exactly one skills block
// (spec acceptance: AGENTS.md injection is idempotent).
func TestInjectSkills_IdempotentOnRerun(t *testing.T) {
	dir := t.TempDir()
	skills := []string{"clex-plan", "grill-me"}

	if err := InjectSkills(dir, skills); err != nil {
		t.Fatalf("first inject: %v", err)
	}
	first := readAgents(t, dir)

	if err := InjectSkills(dir, skills); err != nil {
		t.Fatalf("second inject: %v", err)
	}
	second := readAgents(t, dir)

	if second != first {
		t.Errorf("second injection changed the file:\nfirst:\n%s\nsecond:\n%s", first, second)
	}
	if n := strings.Count(second, skillsMarker); n != 1 {
		t.Errorf("want exactly one marker after re-run, got %d", n)
	}
	if n := strings.Count(second, "## clex-plan"); n != 1 {
		t.Errorf("skill section duplicated: %d occurrences of clex-plan", n)
	}
}

// A repo's own AGENTS.md content above the marker is preserved; only the clex
// block is regenerated.
func TestInjectSkills_PreservesPreamble(t *testing.T) {
	dir := t.TempDir()
	preamble := "# Project agents\n\nBuild with make.\n"
	if err := os.WriteFile(agentsFile(dir), []byte(preamble), 0o644); err != nil {
		t.Fatalf("seed AGENTS.md: %v", err)
	}

	if err := InjectSkills(dir, []string{"clex-plan"}); err != nil {
		t.Fatalf("InjectSkills: %v", err)
	}
	got := readAgents(t, dir)

	if !strings.HasPrefix(got, "# Project agents") {
		t.Errorf("preamble not preserved:\n%s", got)
	}
	if !strings.Contains(got, "Build with make.") {
		t.Errorf("preamble body lost:\n%s", got)
	}
	if strings.Count(got, skillsMarker) != 1 {
		t.Errorf("want one marker, got %d", strings.Count(got, skillsMarker))
	}

	// Re-running still preserves the preamble and does not duplicate it.
	if err := InjectSkills(dir, []string{"clex-plan"}); err != nil {
		t.Fatalf("second InjectSkills: %v", err)
	}
	got2 := readAgents(t, dir)
	if strings.Count(got2, "# Project agents") != 1 {
		t.Errorf("preamble duplicated on re-run:\n%s", got2)
	}
	if strings.Count(got2, skillsMarker) != 1 {
		t.Errorf("marker duplicated on re-run: %d", strings.Count(got2, skillsMarker))
	}
}

// Changing the skill set replaces the previous block rather than appending.
func TestInjectSkills_ReplacesOnChangedSkills(t *testing.T) {
	dir := t.TempDir()
	if err := InjectSkills(dir, []string{"old-skill"}); err != nil {
		t.Fatalf("first: %v", err)
	}
	if err := InjectSkills(dir, []string{"new-skill"}); err != nil {
		t.Fatalf("second: %v", err)
	}
	got := readAgents(t, dir)

	if strings.Contains(got, "## old-skill") {
		t.Errorf("stale skill section should be gone:\n%s", got)
	}
	if !strings.Contains(got, "## new-skill") {
		t.Errorf("new skill section missing:\n%s", got)
	}
	if strings.Count(got, skillsMarker) != 1 {
		t.Errorf("want one marker, got %d", strings.Count(got, skillsMarker))
	}
}

// Skill names are de-duplicated and sorted for deterministic, cache-friendly
// output.
func TestRenderSkills_DedupeAndSort(t *testing.T) {
	out := renderSkills([]string{"b-skill", "a-skill", "b-skill", "  ", "a-skill"})
	if strings.Count(out, "## a-skill") != 1 || strings.Count(out, "## b-skill") != 1 {
		t.Errorf("skills not deduped:\n%s", out)
	}
	ai := strings.Index(out, "## a-skill")
	bi := strings.Index(out, "## b-skill")
	if ai < 0 || bi < 0 || ai > bi {
		t.Errorf("skills not sorted (a before b):\n%s", out)
	}
	if strings.Contains(out, "## \n") {
		t.Errorf("blank skill name should be dropped:\n%s", out)
	}
}

// InjectSkills is invoked by Run; verify Run writes AGENTS.md when Task.Skills is
// set, using the fake CLI so no real codex is needed.
func TestRun_InjectsSkills(t *testing.T) {
	fake := fakeCodexPath(t)
	dir := t.TempDir()
	env := []string{
		"PATH=" + os.Getenv("PATH"),
		"FAKE_CODEX_STREAM=" + mustAbsTestdata(t) + "/run_basic.jsonl",
	}
	a := New("gpt-5-5", WithBinary(fake), WithEnv(env))

	ch, err := a.Run(context.Background(), core.Task{Prompt: "x", Skills: []string{"clex-plan"}}, dir)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	drain(ch)

	got := readAgents(t, dir)
	if !strings.Contains(got, "## clex-plan") {
		t.Errorf("Run did not inject skills into AGENTS.md:\n%s", got)
	}
}
