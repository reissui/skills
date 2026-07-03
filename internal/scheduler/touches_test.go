package scheduler

import "testing"

func TestOverlaps(t *testing.T) {
	cases := []struct {
		name string
		a, b []string
		want bool
	}{
		{"identical dir globs", []string{"internal/gh/**"}, []string{"internal/gh/**"}, true},
		{"parent and child", []string{"internal/gh/**"}, []string{"internal/gh/client.go"}, true},
		{"ancestor dir", []string{"internal/**"}, []string{"internal/store/x.go"}, true},
		{"disjoint dirs", []string{"internal/gh/**"}, []string{"internal/store/**"}, false},
		{"disjoint files", []string{"cmd/clex/main.go"}, []string{"cmd/clexd/main.go"}, false},
		{"empty overlaps all", nil, []string{"internal/store/**"}, true},
		{"both empty", nil, nil, true},
		{"leading wildcard overlaps", []string{"**/x.go"}, []string{"internal/store/**"}, true},
		{"sibling packages disjoint", []string{"internal/runner/claude/**"}, []string{"internal/runner/codex/**"}, false},
		{"same top different sub", []string{"internal/runner/**"}, []string{"internal/runner/codex/**"}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := overlaps(c.a, c.b); got != c.want {
				t.Errorf("overlaps(%v, %v) = %v, want %v", c.a, c.b, got, c.want)
			}
			// Symmetry.
			if got := overlaps(c.b, c.a); got != c.want {
				t.Errorf("overlaps(%v, %v) [swapped] = %v, want %v", c.b, c.a, got, c.want)
			}
		})
	}
}

func TestLiteralPrefix(t *testing.T) {
	cases := map[string]string{
		"internal/gh/**":              "internal/gh",
		"internal/gh/client.go":       "internal/gh/client.go",
		"internal/runner/*/x.go":      "internal/runner",
		"**/foo":                      "",
		"*.go":                        "",
		"internal/runner/cod*":        "internal/runner",
		"cmd/clex/main.go":            "cmd/clex/main.go",
		"internal/store/**/*_test.go": "internal/store",
	}
	for glob, want := range cases {
		if got := literalPrefix(glob); got != want {
			t.Errorf("literalPrefix(%q) = %q, want %q", glob, got, want)
		}
	}
}
