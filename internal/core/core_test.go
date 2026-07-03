package core

import (
	"context"
	"encoding/json"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestEventJSONRoundTrip(t *testing.T) {
	cases := []Event{
		{Type: EventText, Text: "hello world"},
		{Type: EventToolUse, Text: "bash: go test ./..."},
		{Type: EventUsage, Tokens: Usage{In: 1200, Out: 340}},
		{Type: EventResult, Tokens: Usage{In: 5000, Out: 900}, SessionID: "sess-abc123"},
		{Type: EventError, Err: "rate limit exceeded"},
	}
	for _, want := range cases {
		b, err := json.Marshal(want)
		if err != nil {
			t.Fatalf("marshal %+v: %v", want, err)
		}
		var got Event
		if err := json.Unmarshal(b, &got); err != nil {
			t.Fatalf("unmarshal %s: %v", b, err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("round-trip mismatch:\n got  %+v\n want %+v\n json %s", got, want, b)
		}
	}
}

func TestTaskJSONRoundTrip(t *testing.T) {
	want := Task{
		Repo:     "reissui/clex",
		Prompt:   "build issue 42",
		Issue:    42,
		Skills:   []string{"clex-plan", "grill-me"},
		Effort:   "max",
		Fast:     true,
		ResumeID: "resume-xyz",
	}
	b, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	var got Task
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("task round-trip mismatch:\n got  %+v\n want %+v", got, want)
	}
}

func TestEnumValidity(t *testing.T) {
	if !BillingSubscription.Valid() || !BillingMetered.Valid() || !BillingFree.Valid() {
		t.Error("all billing modes should be valid")
	}
	if BillingMode("api").Valid() {
		t.Error("unknown billing mode should be invalid")
	}
	if !DifficultyTrivial.Valid() || !DifficultyStandard.Valid() || !DifficultyComplex.Valid() {
		t.Error("all difficulties should be valid")
	}
	if Difficulty("impossible").Valid() {
		t.Error("unknown difficulty should be invalid")
	}
	for _, r := range []Role{RolePlan, RoleBuild, RoleReview, RoleLint, RoleBot} {
		if !r.Valid() {
			t.Errorf("role %q should be valid", r)
		}
	}
	if Role("deploy").Valid() {
		t.Error("unknown role should be invalid")
	}
}

// mockRunner is a compile-time and runtime check that the Runner interface is
// satisfiable with a stdlib-only implementation.
type mockRunner struct{}

func (mockRunner) Run(ctx context.Context, task Task, dir string) (<-chan Event, error) {
	ch := make(chan Event, 1)
	ch <- Event{Type: EventResult, SessionID: "mock"}
	close(ch)
	return ch, nil
}

func (mockRunner) Probe(ctx context.Context) (Availability, error) {
	return Availability{Healthy: true, Detail: "mock", Models: []string{"mock-1"}}, nil
}

var _ Runner = mockRunner{}

func TestRunnerSatisfied(t *testing.T) {
	var r Runner = mockRunner{}
	ch, err := r.Run(context.Background(), Task{Issue: 1}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	var last Event
	for e := range ch {
		last = e
	}
	if last.Type != EventResult {
		t.Errorf("terminal event type = %q, want result", last.Type)
	}
	av, err := r.Probe(context.Background())
	if err != nil || !av.Healthy {
		t.Errorf("probe = %+v, err=%v; want healthy", av, err)
	}
}

func TestTierMapOrdering(t *testing.T) {
	tm := TierMap{"top": {"opus-4-8", "gpt-5-5"}, "local": {"qwen3-coder"}}
	if len(tm["top"]) != 2 || tm["top"][0] != "opus-4-8" {
		t.Errorf("tier order not preserved: %+v", tm["top"])
	}
}

// TestNoProviderNamesInPackage enforces the interface-freeze invariant: the
// core package is provider-agnostic by construction. It scans non-test .go
// source for hardcoded provider names.
func TestNoProviderNamesInPackage(t *testing.T) {
	banned := []string{"claude", "codex", "ollama", "anthropic", "openai", "gpt", "qwen", "sonnet", "opus", "fable"}
	fset := token.NewFileSet()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		src, err := os.ReadFile(filepath.Clean(name))
		if err != nil {
			t.Fatal(err)
		}
		// Parse and scan identifiers + string literals only (doc comments may
		// legitimately mention providers as examples).
		f, err := parser.ParseFile(fset, name, src, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		lower := strings.ToLower(sourceIdentsAndStrings(f))
		for _, b := range banned {
			if strings.Contains(lower, b) {
				t.Errorf("%s contains banned provider token %q in code (must stay provider-agnostic)", name, b)
			}
		}
	}
}
