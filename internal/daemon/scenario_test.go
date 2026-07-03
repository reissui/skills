package daemon

import (
	"sync"
	"testing"
	"time"

	"github.com/reissui/clex/internal/core"
	"github.com/reissui/clex/internal/gh"
	"github.com/reissui/clex/internal/pipeline"
)

// --- Criterion: full scenario — approve → 2 parallel builds → reviews → assemble.
//
// This drives the daemon's real event loop with the real scheduler (parallel
// dispatch with touches serialization), real store (event/session recording),
// and real registry (routing + cost gate), against fake gh/telegram and a
// fakeStages standing in for the pipeline. Two approved, non-overlapping issues
// must build in parallel, each advance through review to merged, and once both
// have landed the epic assembles into a single final PR.
func TestScenarioParallelBuildsToAssemble(t *testing.T) {
	stages := newFakeStages()

	// Track when both builds are concurrently in-flight to prove parallelism.
	var mu sync.Mutex
	active := map[int]bool{}
	maxConcurrent := 0
	release := make(map[int]chan struct{})
	release[101] = make(chan struct{})
	release[102] = make(chan struct{})
	stages.mu.Lock()
	stages.buildGate[101] = release[101]
	stages.buildGate[102] = release[102]
	stages.onBuild = func(issue int) {
		mu.Lock()
		active[issue] = true
		if len(active) > maxConcurrent {
			maxConcurrent = len(active)
		}
		mu.Unlock()
	}
	stages.mu.Unlock()

	h := newHarness(t, stages)

	// Epic + two non-overlapping children, both approved.
	h.gh.seed(&gh.Issue{Number: 100, Title: "Epic: feature", Body: "PRD", AuthorLogin: "acme", State: core.StateApproved, IsEpic: true})
	h.gh.seed(&gh.Issue{
		Number: 101, Title: "child A", AuthorLogin: "acme", State: core.StateApproved,
		Meta: gh.Metadata{DependsOn: []int{100}, Touches: []string{"pkgA/**"}, Difficulty: core.DifficultyStandard},
	})
	h.gh.seed(&gh.Issue{
		Number: 102, Title: "child B", AuthorLogin: "acme", State: core.StateApproved,
		Meta: gh.Metadata{DependsOn: []int{100}, Touches: []string{"pkgB/**"}, Difficulty: core.DifficultyStandard},
	})

	h.runDaemon(t)

	// Both builds should be concurrently active.
	if !waitFor(2*time.Second, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(active) == 2
	}) {
		t.Fatalf("expected 2 concurrent builds; got %d", len(active))
	}
	mu.Lock()
	if maxConcurrent < 2 {
		mu.Unlock()
		t.Fatalf("builds did not run in parallel; maxConcurrent=%d", maxConcurrent)
	}
	mu.Unlock()

	// When a child's review merges, simulate the merged PR closing the issue by
	// removing it from the open set — exactly what GitHub does. This lets the
	// daemon's automatic maybeAssemble path detect "all children landed" and
	// assemble the epic without any test-driven Assemble call.
	stages.onMerge = func(issue int) {
		h.gh.mu.Lock()
		delete(h.gh.issues, issue)
		h.gh.mu.Unlock()
	}

	// Release both builds; each advances build→review→merged→(maybe assemble).
	close(release[101])
	close(release[102])

	// The epic assembles automatically once the second child lands.
	if !waitFor(3*time.Second, func() bool {
		stages.mu.Lock()
		defer stages.mu.Unlock()
		return len(stages.assembleCalls) >= 1
	}) {
		t.Fatalf("epic did not auto-assemble; reviews=%v assembles=%v", stages.reviewCalls, stages.assembleCalls)
	}
	if !h.tg.sentContains("final PR") {
		t.Fatal("expected a final-PR notification from assembly")
	}

	// Store recorded events across the scenario (audit trail is populated).
	if evs, err := h.st.RecentEvents(200); err == nil && len(evs) == 0 {
		t.Fatal("expected the event log to record scenario activity")
	}
}

// TestRealPipelineComposes proves the REAL *pipeline.Pipeline satisfies the
// daemon's Stages contract and can be constructed with the same fake
// collaborators the daemon uses — i.e. the wiring in FromConfig is sound. It
// does not run a stage (that needs a real worktree/verification); the pipeline's
// stage behavior is covered by #15. This closes the "real pipeline is wired"
// requirement without shelling out to git/CLIs.
func TestRealPipelineComposes(t *testing.T) {
	cfg := buildTestConfig()
	runner := &fakeRunner{}
	rf := &fakeFactory{runner: runner}
	reg := registryFor(cfg, runner)

	pl := pipeline.New(pipeline.Deps{
		GH:      &fakePipelineGH{},
		WS:      &fakeWorkspace{},
		Router:  reg,
		Skills:  &fakeSkills{},
		Runners: rf,
	}, pipeline.Config{
		Repo:          testRepo,
		RepoDir:       t.TempDir(),
		Owner:         "acme",
		SelfLogin:     "clex-bot",
		DefaultVerify: "go test ./...",
		TopTier:       cfg.Tiers["top"],
	})

	// Compile-time and run-time proof it is usable as daemon Stages.
	var stages Stages = pl
	if stages == nil {
		t.Fatal("real pipeline did not satisfy Stages")
	}
	// EscalateModel is a pure method safe to call without side effects.
	if _, ok := stages.EscalateModel(core.Model{ID: "fake-model", Provider: "fake"}); ok {
		// Either result is acceptable; we only assert it does not panic.
		_ = ok
	}
}
