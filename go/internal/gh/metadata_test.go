package gh

import (
	"reflect"
	"testing"

	"github.com/reissui/clex/internal/core"
)

func TestParseMetadataFullBlock(t *testing.T) {
	body := "Some intro prose about the issue.\n\n" +
		"```clex\n" +
		"Depends-on: #3, #4, 7\n" +
		"Touches: internal/gh/**, go.mod\n" +
		"Difficulty: standard\n" +
		"Verify: go test ./internal/gh/...\n" +
		"```\n"
	m := ParseMetadata(body)

	wantDeps := []int{3, 4, 7}
	if !reflect.DeepEqual(m.DependsOn, wantDeps) {
		t.Errorf("DependsOn = %v, want %v", m.DependsOn, wantDeps)
	}
	wantTouches := []string{"internal/gh/**", "go.mod"}
	if !reflect.DeepEqual(m.Touches, wantTouches) {
		t.Errorf("Touches = %v, want %v", m.Touches, wantTouches)
	}
	if m.TouchesWildcard {
		t.Error("TouchesWildcard = true, want false (explicit Touches present)")
	}
	if m.Difficulty != core.DifficultyStandard {
		t.Errorf("Difficulty = %q, want %q", m.Difficulty, core.DifficultyStandard)
	}
	if m.Verify != "go test ./internal/gh/..." {
		t.Errorf("Verify = %q, want the go test command", m.Verify)
	}
	if len(m.Warnings) != 0 {
		t.Errorf("Warnings = %v, want none", m.Warnings)
	}
}

func TestParseMetadataMissingTouchesReturnsWildcard(t *testing.T) {
	body := "```clex\n" +
		"Depends-on: #1\n" +
		"Difficulty: trivial\n" +
		"```\n"
	m := ParseMetadata(body)

	want := []string{wildcardTouches}
	if !reflect.DeepEqual(m.Touches, want) {
		t.Errorf("Touches = %v, want %v (wildcard)", m.Touches, want)
	}
	if !m.TouchesWildcard {
		t.Error("TouchesWildcard = false, want true when Touches is absent")
	}
}

func TestParseMetadataMalformedLinesWarnNotPanic(t *testing.T) {
	// A deliberately messy block: a garbage line, a non-numeric dependency, and
	// an unrecognized difficulty. None of these may panic; each must warn.
	body := "```clex\n" +
		"Depends-on: #2, banana\n" +
		"this line has no colon and is nonsense\n" +
		"Touches: internal/gh/**\n" +
		"Difficulty: herculean\n" +
		"```\n"

	m := ParseMetadata(body) // must not panic

	if want := []int{2}; !reflect.DeepEqual(m.DependsOn, want) {
		t.Errorf("DependsOn = %v, want %v (banana dropped)", m.DependsOn, want)
	}
	if m.Difficulty != "" {
		t.Errorf("Difficulty = %q, want empty (herculean is unrecognized)", m.Difficulty)
	}
	if len(m.Warnings) < 3 {
		t.Errorf("Warnings = %v, want at least 3 (garbage line, banana dep, bad difficulty)", m.Warnings)
	}
}

func TestParseMetadataNoBlockAtAll(t *testing.T) {
	// Ordinary prose with colons must not produce warnings or spurious metadata,
	// and must default Touches to the wildcard.
	body := "This issue does something.\n\nNote: be careful here.\nSee also: the docs.\n"
	m := ParseMetadata(body)

	if len(m.DependsOn) != 0 {
		t.Errorf("DependsOn = %v, want none", m.DependsOn)
	}
	if !m.TouchesWildcard {
		t.Error("TouchesWildcard = false, want true for a body with no clex block")
	}
	if len(m.Warnings) != 0 {
		t.Errorf("Warnings = %v, want none for ordinary prose", m.Warnings)
	}
}

func TestParseMetadataBareLinesNoFence(t *testing.T) {
	// Some planners may omit the fence; bare Key: value lines still parse.
	body := "Touches: cmd/clex/**\nDifficulty: complex\nVerification: make test\n"
	m := ParseMetadata(body)

	if want := []string{"cmd/clex/**"}; !reflect.DeepEqual(m.Touches, want) {
		t.Errorf("Touches = %v, want %v", m.Touches, want)
	}
	if m.Difficulty != core.DifficultyComplex {
		t.Errorf("Difficulty = %q, want %q", m.Difficulty, core.DifficultyComplex)
	}
	if m.Verify != "make test" {
		t.Errorf("Verify = %q, want %q", m.Verify, "make test")
	}
}

func TestParseMetadataDeduplicates(t *testing.T) {
	body := "```clex\n" +
		"Depends-on: #3, #3, 4\n" +
		"Touches: a/**, a/**, b/**\n" +
		"```\n"
	m := ParseMetadata(body)

	if want := []int{3, 4}; !reflect.DeepEqual(m.DependsOn, want) {
		t.Errorf("DependsOn = %v, want %v (deduped)", m.DependsOn, want)
	}
	if want := []string{"a/**", "b/**"}; !reflect.DeepEqual(m.Touches, want) {
		t.Errorf("Touches = %v, want %v (deduped)", m.Touches, want)
	}
}
