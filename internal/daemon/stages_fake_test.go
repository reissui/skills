package daemon

import (
	"context"
	"sync"
	"time"

	"github.com/reissui/clex/internal/core"
	"github.com/reissui/clex/internal/gh"
	"github.com/reissui/clex/internal/pipeline"
)

// errVerificationSentinel is the pipeline verification-failed error, used by
// tests to drive the escalation path exactly as production code matches it
// (errors.Is against pipeline.ErrVerificationFailed).
var errVerificationSentinel = pipeline.ErrVerificationFailed

// fakeStages is a scriptable Stages double for daemon loop-mechanics tests
// (stop, steer, escalation bookkeeping, recovery). It lets a test control how
// long a Build blocks and whether it fails, without a real pipeline or git. The
// scenario test uses the REAL *pipeline.Pipeline instead (see scenario_test.go).
type fakeStages struct {
	mu sync.Mutex

	// buildGate, if non-nil for an issue, blocks Build until the test closes it
	// (used to hold a runner "active" for stop/steer tests).
	buildGate map[int]chan struct{}
	// buildErr[n] is the error the nth (1-based) Build call for an issue returns.
	buildErrByAttempt map[int][]error
	buildAttempts     map[int]int

	// escalateTo is the model EscalateModel returns; ok=false when zero.
	escalateTo core.Model
	escalateOK bool

	// records
	buildCalls    []int
	reviewCalls   []int
	assembleCalls []int
	escalateCalls int

	// reviewResult controls what Review returns.
	reviewMerged bool

	// onBuild is an optional hook invoked at the start of each Build.
	onBuild func(issue int)
}

func newFakeStages() *fakeStages {
	return &fakeStages{
		buildGate:         make(map[int]chan struct{}),
		buildErrByAttempt: make(map[int][]error),
		buildAttempts:     make(map[int]int),
		reviewMerged:      true,
	}
}

func (s *fakeStages) Plan(_ context.Context, _ *gh.Issue, _ pipeline.PlanInputs, _ int) (pipeline.PlanResult, error) {
	return pipeline.PlanResult{}, nil
}

func (s *fakeStages) Build(ctx context.Context, _ int, iss *gh.Issue, _ pipeline.KnowledgeExcerpts, _ int) (pipeline.BuildResult, error) {
	s.mu.Lock()
	s.buildCalls = append(s.buildCalls, iss.Number)
	s.buildAttempts[iss.Number]++
	attempt := s.buildAttempts[iss.Number]
	gate := s.buildGate[iss.Number]
	errs := s.buildErrByAttempt[iss.Number]
	hook := s.onBuild
	s.mu.Unlock()

	if hook != nil {
		hook(iss.Number)
	}
	if gate != nil {
		select {
		case <-gate:
		case <-ctx.Done():
			return pipeline.BuildResult{}, ctx.Err()
		}
	}
	// Respect cancellation (stop/shutdown).
	if ctx.Err() != nil {
		return pipeline.BuildResult{}, ctx.Err()
	}
	var err error
	if attempt-1 < len(errs) {
		err = errs[attempt-1]
	}
	res := pipeline.BuildResult{
		WorktreeDir:  "/fake/wt",
		Model:        core.Model{ID: "fake-model", Provider: "fake"},
		PRNumber:     1000 + iss.Number,
		SessionID:    "cli-sess",
		Verification: pipeline.VerificationPlan{Command: "go test ./...", Trusted: true},
	}
	if err != nil {
		return pipeline.BuildResult{PRNumber: 0, SessionID: "cli-sess"}, err
	}
	return res, nil
}

func (s *fakeStages) Review(_ context.Context, _ int, iss *gh.Issue, _ int, _ core.Model, _ string, _ bool) (pipeline.ReviewResult, error) {
	s.mu.Lock()
	s.reviewCalls = append(s.reviewCalls, iss.Number)
	merged := s.reviewMerged
	s.mu.Unlock()
	out := pipeline.ReviewResult{Outcome: pipeline.ReviewApproved, Merged: merged, MergeSHA: "abcdef1234"}
	return out, nil
}

func (s *fakeStages) Assemble(_ context.Context, epicNum int, allLanded bool, _ pipeline.AssembleInput, _ string, _ int) (pipeline.AssembleResult, error) {
	s.mu.Lock()
	s.assembleCalls = append(s.assembleCalls, epicNum)
	s.mu.Unlock()
	if !allLanded {
		return pipeline.AssembleResult{}, pipeline.ErrNotReady
	}
	return pipeline.AssembleResult{PRNumber: 9999}, nil
}

func (s *fakeStages) EscalateModel(_ core.Model) (core.Model, bool) {
	s.mu.Lock()
	s.escalateCalls++
	m, ok := s.escalateTo, s.escalateOK
	s.mu.Unlock()
	return m, ok
}

// --- scripting helpers ---

func (s *fakeStages) holdBuild(issue int) chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	ch := make(chan struct{})
	s.buildGate[issue] = ch
	return ch
}

func (s *fakeStages) failBuilds(issue int, errs ...error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.buildErrByAttempt[issue] = errs
}

func (s *fakeStages) setEscalation(m core.Model, ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.escalateTo, s.escalateOK = m, ok
}

func (s *fakeStages) buildCount(issue int) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, b := range s.buildCalls {
		if b == issue {
			n++
		}
	}
	return n
}

func (s *fakeStages) escalations() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.escalateCalls
}

// waitFor polls cond until true or timeout; returns whether it became true.
func waitFor(timeout time.Duration, cond func() bool) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return cond()
}
