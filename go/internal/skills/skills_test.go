package skills

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// repoSkillsDir returns the absolute path to the repo skills/plan-build/
// directory (three levels up from go/internal/skills, then into plan-build),
// where the authoritative clex-authored skills live.
func repoSkillsDir(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(wd, "..", "..", "..", "skills", "plan-build")
}

// TestAuthoredSkillsExistWithContract verifies both clex-authored SKILL.md
// files exist under skills/plan-build/ with YAML frontmatter (name +
// description) and carry the contract language the spec requires.
func TestAuthoredSkillsExistWithContract(t *testing.T) {
	root := repoSkillsDir(t)
	cases := []struct {
		dir         string
		wantName    string
		mustContain []string
	}{
		{
			dir:      "clex-plan",
			wantName: "name: clex-plan",
			mustContain: []string{
				// The executability test, verbatim.
				"could a modest local model complete this without asking a single question?",
				// The metadata block shapes #6's parser reads.
				"Depends-on:",
				"Touches:",
				"Difficulty:",
				// Batched open questions each carrying a proposed answer.
				"Proposed:",
			},
		},
		{
			dir:      "clex-issue-lint",
			wantName: "name: clex-issue-lint",
			mustContain: []string{
				// Emits the exact JSON contract.
				`"pass"`,
				`"failures"`,
				`"criterion"`,
				`"detail"`,
				// Applies the same executability test.
				"could a modest local model complete this without asking a single question?",
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.dir, func(t *testing.T) {
			path := filepath.Join(root, tc.dir, "SKILL.md")
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read %s: %v", path, err)
			}
			body := string(data)
			// Frontmatter: must open with a YAML fence and declare name + description.
			if !strings.HasPrefix(body, "---\n") {
				t.Errorf("%s: missing opening YAML frontmatter fence", tc.dir)
			}
			if !strings.Contains(body, tc.wantName) {
				t.Errorf("%s: frontmatter missing %q", tc.dir, tc.wantName)
			}
			if !strings.Contains(body, "description:") {
				t.Errorf("%s: frontmatter missing description", tc.dir)
			}
			for _, want := range tc.mustContain {
				if !strings.Contains(body, want) {
					t.Errorf("%s: missing required contract language %q", tc.dir, want)
				}
			}
		})
	}
}

// TestBundledMatchesRepo guards against drift between the authoritative
// repo-root skills/ and the embedded copies the installer ships.
func TestBundledMatchesRepo(t *testing.T) {
	root := repoSkillsDir(t)
	for _, name := range BundledNames() {
		repoPath := filepath.Join(root, name, "SKILL.md")
		repoData, err := os.ReadFile(repoPath)
		if err != nil {
			t.Fatalf("read repo skill %s: %v", name, err)
		}
		sub, err := BundledFS()
		if err != nil {
			t.Fatal(err)
		}
		embData, err := fs.ReadFile(sub, name+"/SKILL.md")
		if err != nil {
			t.Fatalf("read embedded skill %s: %v", name, err)
		}
		if string(repoData) != string(embData) {
			t.Errorf("bundled %s/SKILL.md differs from repo copy; re-copy skills/%s into internal/skills/bundled/%s", name, name, name)
		}
	}
	// Both clex-authored skills must be embedded.
	names := strings.Join(BundledNames(), ",")
	for _, want := range []string{"clex-plan", "clex-issue-lint"} {
		if !strings.Contains(names, want) {
			t.Errorf("bundled skills missing %q (have %q)", want, names)
		}
	}
}
