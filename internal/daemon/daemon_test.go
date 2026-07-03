package daemon

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/reissui/clex/internal/config"
	"github.com/reissui/clex/internal/core"
	"github.com/reissui/clex/internal/gh"
	"github.com/reissui/clex/internal/registry"
	"github.com/reissui/clex/internal/store"
)

// testRepo is the fixed repo used across daemon tests.
var testRepo = gh.Repo{Owner: "acme", Name: "widget"}

// harness bundles a Daemon wired to fakes plus a real store and registry for a
// test. The gh and telegram ports are fakes; Stages is the supplied double (a
// fakeStages for mechanics tests). No network, no git, no live services.
type harness struct {
	d   *Daemon
	gh  *fakeGH
	tg  *fakeTG
	st  *store.Store
	rf  *fakeFactory
	cfg *config.Config
}

// newHarness builds a daemon over fakes. stages is the Stages double to use.
func newHarness(t *testing.T, stages Stages) *harness {
	t.Helper()
	home := t.TempDir()
	if _, err := EnsureHome(home); err != nil {
		t.Fatalf("EnsureHome: %v", err)
	}
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	cfg := buildTestConfig()
	runner := &fakeRunner{}
	rf := &fakeFactory{runner: runner}
	reg := registry.New(cfg, map[string]core.Runner{"fake": runner})

	fg := newFakeGH(testRepo)
	ftg := newFakeTG()
	red := NewRedactor("token-xyz")

	d, err := New(Deps{
		GH:            fg,
		TG:            ftg,
		Stages:        stages,
		Registry:      reg,
		Store:         st,
		RunnerFactory: rf,
	}, Config{
		Repo:          testRepo,
		Home:          home,
		Owner:         "acme",
		SelfLogin:     "clex-bot",
		PollInterval:  20 * time.Millisecond,
		MaxParallel:   4,
		DefaultVerify: "go test ./...",
	}, slog.New(slog.NewTextHandler(&discardWriter{}, nil)), red)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return &harness{d: d, gh: fg, tg: ftg, st: st, rf: rf, cfg: cfg}
}

// runDaemon starts d.Run in the background and returns a cancel func.
func (h *harness) runDaemon(t *testing.T) context.CancelFunc {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		_ = h.d.Run(ctx)
		close(done)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Log("daemon did not stop within 2s")
		}
	})
	return cancel
}

// approvedIssue seeds an approved child issue with the given deps/touches.
func (h *harness) approvedIssue(number int, deps []int, touches []string) {
	h.gh.seed(&gh.Issue{
		Number:      number,
		Title:       "child",
		AuthorLogin: "acme",
		State:       core.StateApproved,
		Meta:        gh.Metadata{DependsOn: deps, Touches: touches, Difficulty: core.DifficultyStandard},
	})
}

type discardWriter struct{}

func (discardWriter) Write(p []byte) (int, error) { return len(p), nil }

// buildTestConfig builds a minimal valid config whose single model sits in the
// build-eligible "mid" tier with subscription billing, so registry.Build
// returns a winner and the cost gate always proceeds (subscription bypasses
// cost gates). This is the config the daemon tests route through.
func buildTestConfig() *config.Config {
	cfg := &config.Config{
		TelegramToken:  "token-xyz",
		TelegramChatID: 12345,
		Verification:   "go test ./...",
		Providers: map[string]config.Provider{
			"fake": {Kind: "claude-cli"},
		},
		Models: map[string]config.Model{
			"fake-model": {Provider: "fake", Billing: core.BillingSubscription},
		},
		Tiers: core.TierMap{
			"mid": {"fake-model"},
			"top": {"fake-model"},
		},
		Routing: map[string]config.Routing{
			string(core.RolePlan):   {Tier: "top"},
			string(core.RoleBuild):  {Policy: "auto"},
			string(core.RoleReview): {Tier: "top"},
			string(core.RoleLint):   {Tier: "mid"},
			string(core.RoleBot):    {Tier: "mid"},
		},
	}
	return cfg
}

// --- Criterion: /stop cancels exactly the target, preserves worktree, reverts label

func TestStopCancelsTargetOnly(t *testing.T) {
	stages := newFakeStages()
	gate14 := stages.holdBuild(14)
	stages.holdBuild(15)
	h := newHarness(t, stages)
	h.approvedIssue(14, nil, []string{"a/**"})
	h.approvedIssue(15, nil, []string{"b/**"})
	h.runDaemon(t)

	// Wait for both builds to be in-flight (running set populated).
	if !waitFor(time.Second, func() bool {
		h.d.mu.Lock()
		defer h.d.mu.Unlock()
		return len(h.d.running) == 2
	}) {
		t.Fatalf("expected 2 running builds, got %d", len(h.d.running))
	}

	// Pause first so the reconcile that follows the stop does not immediately
	// re-dispatch the now-approved #14 (a legitimate but timing-dependent
	// behavior we exclude here to make the target-only assertion deterministic).
	h.d.submitControl(context.Background(), controlAction{kind: ctlPause, reply: make(chan string, 1)})

	// Stop only #14.
	msg := h.d.submitControl(context.Background(), controlAction{kind: ctlStop, issue: 14, reply: make(chan string, 1)})
	if msg != "stopped #14" {
		t.Fatalf("stop reply = %q", msg)
	}

	// #14 reverts to approved; #15 stays building.
	if !waitFor(time.Second, func() bool { return h.gh.stateOf(14) == core.StateApproved }) {
		t.Fatalf("#14 state = %s, want approved", h.gh.stateOf(14))
	}
	if h.gh.stateOf(15) != core.StateBuilding {
		t.Fatalf("#15 state = %s, want building (untouched)", h.gh.stateOf(15))
	}
	// #14 no longer in running set; #15 still there. (Paused, so no re-dispatch.)
	if !waitFor(time.Second, func() bool {
		h.d.mu.Lock()
		defer h.d.mu.Unlock()
		_, has14 := h.d.running[14]
		_, has15 := h.d.running[15]
		return !has14 && has15
	}) {
		t.Fatal("running set not as expected after stop")
	}
	// The daemon never called Cleanup on the worktree (fakeStages has no cleanup;
	// the contract is that stop does not remove the worktree — asserted by the
	// absence of any teardown and the preserved-worktree log line).
	if !h.tg.sentContains("worktree preserved") {
		t.Fatal("expected a 'worktree preserved' notification")
	}
	// release the other gate so the daemon can shut down cleanly
	close(gate14)
}

// --- Criterion: two failed verifications → exactly one escalation re-dispatch

func TestEscalationAfterTwoVerificationFailures(t *testing.T) {
	stages := newFakeStages()
	// Fail attempt 1 and attempt 2 with verification failure; attempt 3 (escalated) succeeds.
	stages.failBuilds(21, pipeline_errVerification(), pipeline_errVerification())
	stages.setEscalation(core.Model{ID: "opus-4-8", Provider: "claude"}, true)
	h := newHarness(t, stages)
	h.approvedIssue(21, nil, []string{"pkg/**"})
	h.runDaemon(t)

	// Expect exactly one escalation call and 3 total builds (2 failed + 1 escalated).
	if !waitFor(2*time.Second, func() bool { return stages.buildCount(21) >= 3 }) {
		t.Fatalf("expected >=3 build attempts, got %d", stages.buildCount(21))
	}
	// Give the loop a beat to settle, then assert exactly one escalation.
	time.Sleep(50 * time.Millisecond)
	if got := stages.escalations(); got != 1 {
		t.Fatalf("escalations = %d, want exactly 1", got)
	}
	if !h.tg.sentContains("escalating to opus-4-8") {
		t.Fatal("expected an escalation notification carrying the new model")
	}
}

// --- Criterion: SIGTERM exits 0 with runners cleaned up

func TestShutdownRevertsRunners(t *testing.T) {
	stages := newFakeStages()
	stages.holdBuild(30) // keep the build active until shutdown cancels it
	h := newHarness(t, stages)
	h.approvedIssue(30, nil, nil)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- h.d.Run(ctx) }()

	// Wait until the build is running.
	if !waitFor(time.Second, func() bool {
		h.d.mu.Lock()
		defer h.d.mu.Unlock()
		return len(h.d.running) == 1
	}) {
		t.Fatal("build never started")
	}

	// SIGTERM equivalent: cancel the context.
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned error on shutdown: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return within 2s of cancel")
	}
	// The runner's label was reverted to approved on shutdown.
	if h.gh.stateOf(30) != core.StateApproved {
		t.Fatalf("#30 state = %s after shutdown, want approved", h.gh.stateOf(30))
	}
}

// --- Criterion: pause stops new dispatches, running work continues

func TestPauseHoldsNewDispatches(t *testing.T) {
	stages := newFakeStages()
	h := newHarness(t, stages)
	h.runDaemon(t)

	// Pause before seeding work.
	h.d.submitControl(context.Background(), controlAction{kind: ctlPause, reply: make(chan string, 1)})
	h.approvedIssue(40, nil, nil)

	// Give the reconcile ticker a few cycles; no build should start.
	time.Sleep(120 * time.Millisecond)
	if n := stages.buildCount(40); n != 0 {
		t.Fatalf("build dispatched while paused: count=%d", n)
	}

	// Resume: the build should now start.
	h.d.submitControl(context.Background(), controlAction{kind: ctlResume, reply: make(chan string, 1)})
	if !waitFor(time.Second, func() bool { return stages.buildCount(40) >= 1 }) {
		t.Fatal("build did not dispatch after resume")
	}
}

// TestEscalationNeverDoublesEvenIfLadderMisbehaves proves the daemon escalates
// AT MOST once even when the registry ladder keeps offering a "stronger" model.
// After the single escalation the escalated build also fails; the daemon must
// hand it to a human, not escalate again — the one-escalation rule holds
// independent of EscalateModel's behavior.
func TestEscalationNeverDoublesEvenIfLadderMisbehaves(t *testing.T) {
	stages := newFakeStages()
	// Fail attempts 1, 2, AND the escalated attempt 3 — all verification failures.
	stages.failBuilds(22, pipeline_errVerification(), pipeline_errVerification(), pipeline_errVerification())
	// A misbehaving ladder that ALWAYS offers a "stronger" model.
	stages.setEscalation(core.Model{ID: "opus-4-8", Provider: "claude"}, true)
	h := newHarness(t, stages)
	h.approvedIssue(22, nil, []string{"pkg2/**"})
	h.runDaemon(t)

	// Wait for the failed escalated build (attempt 3).
	if !waitFor(2*time.Second, func() bool { return stages.buildCount(22) >= 3 }) {
		t.Fatalf("expected >=3 attempts; got %d", stages.buildCount(22))
	}
	// Let the loop settle, then assert escalation happened exactly once despite
	// the ladder still offering a model.
	time.Sleep(100 * time.Millisecond)
	if got := stages.escalations(); got != 1 {
		t.Fatalf("escalations = %d, want exactly 1 (no double-escalation)", got)
	}
	// And the issue should not still be building (handed to human / left approved).
	if !h.tg.sentContains("after escalation") {
		t.Fatal("expected a 'needs a decision after escalation' notification")
	}
}

// pipeline_errVerification returns the pipeline verification-failed sentinel via
// the exported var, kept in a helper so tests read cleanly.
func pipeline_errVerification() error { return errVerificationSentinel }
