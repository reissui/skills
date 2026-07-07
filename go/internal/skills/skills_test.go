package skills

import (
	"io/fs"
	"strings"
	"testing"
)

// The clex-authored skills (clex-plan, clex-issue-lint) live embedded in the
// parked binary under bundled/. They were removed from the repo-root skills/
// directory (which now distributes the standalone plan/ship/grill skills), so
// these tests verify the EMBEDDED copies — the ones the binary actually ships —
// rather than any repo-root file.

// TestAuthoredSkillsExistWithContract verifies both clex-authored SKILL.md
// files are embedded with YAML frontmatter (name + description) and carry the
// contract language the spec requires.
func TestAuthoredSkillsExistWithContract(t *testing.T) {
	sub, err := BundledFS()
	if err != nil {
		t.Fatal(err)
	}
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
				// The metadata block shapes the planner emits.
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
			data, err := fs.ReadFile(sub, tc.dir+"/SKILL.md")
			if err != nil {
				t.Fatalf("read embedded %s: %v", tc.dir, err)
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

// TestBundledSkillsPresent verifies both clex-authored skills are embedded and
// each embedded SKILL.md is non-empty.
func TestBundledSkillsPresent(t *testing.T) {
	sub, err := BundledFS()
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range BundledNames() {
		data, err := fs.ReadFile(sub, name+"/SKILL.md")
		if err != nil {
			t.Fatalf("read embedded skill %s: %v", name, err)
		}
		if len(data) == 0 {
			t.Errorf("embedded %s/SKILL.md is empty", name)
		}
	}
	names := strings.Join(BundledNames(), ",")
	for _, want := range []string{"clex-plan", "clex-issue-lint"} {
		if !strings.Contains(names, want) {
			t.Errorf("bundled skills missing %q (have %q)", want, names)
		}
	}
}
