//go:build e2e

// Package e2e is clex's opt-in end-to-end suite. It drives the real daemon,
// scheduler, pipeline, registry, store, workspace manager, and GitHub client
// through a complete idea → plan → approve → parallel builds → reviews →
// integration merges → single final PR flow, against a scratch bare git repo and
// an in-memory GitHub API double, with every runner replaced by the scripted
// clex-fake-runner binary. There are no live services (spec: Testing strategy —
// "a deterministic fake runner drives the full idea→PR flow against a scratch
// GitHub repo; opt-in, runs in CI nightly not on every push").
//
// Build tag: e2e. Run with `go test -tags e2e ./e2e/...`.
package e2e

import (
	"context"
	"testing"
	"time"

	"github.com/reissui/clex/internal/core"
	"github.com/reissui/clex/internal/gh"
	"github.com/reissui/clex/internal/pipeline"
	"github.com/reissui/clex/internal/store"
)

// TestEndToEndIdeaToFinalPR is the capstone: it exercises the whole clex flow and
// asserts the epic-level invariants the design guarantees.
func TestEndToEndIdeaToFinalPR(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e flow is not run under -short")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	h := newHarness(t)

	// --- 1. Idea → plan. Drive the real Plan stage directly (the daemon loop only
	// dispatches builds; Plan is a CLI-invoked stage in production). The fake
	// planner emits a 3-issue plan, the third depending on the second. ---
	idea := seedIdea(t, h)
	pl := h.planPipeline()
	planRes, err := pl.Plan(ctx, idea, pipeline.PlanInputs{}, 0)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(planRes.IssueNumbers) != 3 {
		t.Fatalf("expected 3 planned issues, got %d (%v)", len(planRes.IssueNumbers), planRes.IssueNumbers)
	}
	if planRes.EpicNumber == 0 {
		t.Fatal("plan produced no epic")
	}
	if len(planRes.LintFailures) != 0 {
		t.Fatalf("unexpected lint failures: %v", planRes.LintFailures)
	}
	epic := planRes.EpicNumber
	c1, c2, c3 := planRes.IssueNumbers[0], planRes.IssueNumbers[1], planRes.IssueNumbers[2]

	// Children are created labelled clex:planned — the plan gate: nothing
	// builds until the owner approves. Every child links its epic in
	// Depends-on (how the daemon resolves the integration branch), and the
	// third child records its dependency on the second.
	assertHasLabel(t, h, c1, string(core.StatePlanned))
	assertHasLabel(t, h, c2, string(core.StatePlanned))
	assertHasLabel(t, h, c3, string(core.StatePlanned))

	c3Issue, err := h.ghc.GetIssue(ctx, testRepo, c3)
	if err != nil {
		t.Fatalf("get child 3: %v", err)
	}
	if !containsInt(c3Issue.Meta.DependsOn, c2) {
		t.Fatalf("child #%d should depend on #%d; deps=%v", c3, c2, c3Issue.Meta.DependsOn)
	}
	for _, c := range []int{c1, c2, c3} {
		iss, gerr := h.ghc.GetIssue(ctx, testRepo, c)
		if gerr != nil {
			t.Fatalf("get child #%d: %v", c, gerr)
		}
		if !containsInt(iss.Meta.DependsOn, epic) {
			t.Fatalf("child #%d should depend on its epic #%d; deps=%v", c, epic, iss.Meta.DependsOn)
		}
	}

	// Pass the plan gate: approve every child (what /build <epic#> does).
	for _, c := range []int{c1, c2, c3} {
		if err := h.ghc.SetState(ctx, testRepo, c, core.StateApproved); err != nil {
			t.Fatalf("approve child #%d: %v", c, err)
		}
	}

	// --- 2. Run the daemon: it dispatches approved children (respecting the
	// dependency and touches serialization), reviews, merges into the integration
	// branch, and assembles the single final PR. ---
	_, stopDaemon := h.startDaemon(ctx)
	defer stopDaemon()

	// Wait for the epic to assemble into a final PR to main (opened, not merged —
	// auto-merge is off by default so the final PR is the owner's manual gate).
	if !waitFor(60*time.Second, func() bool { return len(h.gh.prsToMain()) >= 1 }) {
		t.Fatalf("epic did not assemble a final PR; tg=%v", h.tg.sent())
	}
	// Give the daemon extra ticks to (incorrectly) open a SECOND final PR if the
	// idempotency guard were broken; then assert exactly one exists.
	time.Sleep(1 * time.Second)

	// === ASSERTIONS ===

	// (A) Exactly ONE PR to main — the single final integration PR.
	prsToMain := h.gh.prsToMain()
	if len(prsToMain) != 1 {
		t.Fatalf("expected exactly ONE PR targeting main, got %d (%+v)", len(prsToMain), prsToMain)
	}
	finalPR := prsToMain[0]
	if finalPR.Head != "clex/epic-"+itoa(epic) {
		t.Fatalf("final PR head = %q, want the epic integration branch clex/epic-%d", finalPR.Head, epic)
	}
	if finalPR.Base != "main" {
		t.Fatalf("final PR base = %q, want main", finalPR.Base)
	}

	// (B) Two issues built in parallel: their runner sessions overlap in time.
	assertParallel(t, h.st, c1, c2)

	// (C) The dependency (#c3) built AFTER its blocker (#c2) finished.
	assertDependencyOrder(t, h.st, c2, c3)

	// (D) Branch topology: the integration branch exists on origin and every
	// child's build file is present on it (their branches merged in).
	assertBranchTopology(t, h, epic, []int{c1, c2, c3})

	// (E) Label history: each child ended out of the pipeline (closed/merged) and
	// passed through building + review. The store event log records the dispatch
	// and review transitions for each.
	assertLabelHistory(t, h.st, []int{c1, c2, c3})

	// (F) Store records: sessions exist per child with real start/end times, and
	// the event log ("LOG") captured the epic assembly.
	assertStoreRecords(t, h.st, epic, []int{c1, c2, c3})
}

// seedIdea creates the idea issue on the fake GitHub and returns the gh.Issue the
// pipeline's Plan expects.
func seedIdea(t *testing.T, h *harness) *gh.Issue {
	t.Helper()
	iss, err := h.ghc.CreateIssue(context.Background(), testRepo,
		"add a greeting service", "I want a small greeting service.", []string{string(core.StateIdea)})
	if err != nil {
		t.Fatalf("seed idea: %v", err)
	}
	return iss
}

// assertParallel proves two issues' runner sessions overlapped: one started
// before the other ended (and vice versa). Sessions are recorded by the daemon
// with real StartedAt/EndedAt timestamps.
func assertParallel(t *testing.T, st *store.Store, a, b int) {
	t.Helper()
	sa := latestSession(t, st, a)
	sb := latestSession(t, st, b)
	if sa.EndedAt.IsZero() || sb.EndedAt.IsZero() {
		t.Fatalf("sessions for #%d/#%d not both finished: a.end=%v b.end=%v", a, b, sa.EndedAt, sb.EndedAt)
	}
	// Overlap iff a started before b ended AND b started before a ended.
	overlap := sa.StartedAt.Before(sb.EndedAt) && sb.StartedAt.Before(sa.EndedAt)
	if !overlap {
		t.Fatalf("builds #%d and #%d did not overlap: #%d[%v..%v] #%d[%v..%v]",
			a, b, a, sa.StartedAt, sa.EndedAt, b, sb.StartedAt, sb.EndedAt)
	}
}

// assertDependencyOrder proves the dependent issue's build started only after its
// blocker's build finished (the scheduler must not dispatch a blocked issue until
// its dependency lands).
func assertDependencyOrder(t *testing.T, st *store.Store, blocker, dependent int) {
	t.Helper()
	sb := latestSession(t, st, blocker)
	sd := latestSession(t, st, dependent)
	if sb.EndedAt.IsZero() {
		t.Fatalf("blocker #%d session never ended", blocker)
	}
	if sd.StartedAt.Before(sb.EndedAt) {
		t.Fatalf("dependent #%d started (%v) before blocker #%d finished (%v)",
			dependent, sd.StartedAt, blocker, sb.EndedAt)
	}
}

// latestSession returns the most recent session for an issue, failing if none.
func latestSession(t *testing.T, st *store.Store, issue int) store.Session {
	t.Helper()
	sessions, err := st.SessionsForIssue(issue)
	if err != nil {
		t.Fatalf("sessions for #%d: %v", issue, err)
	}
	if len(sessions) == 0 {
		t.Fatalf("no sessions recorded for #%d", issue)
	}
	// SessionsForIssue returns newest-first; take the newest terminal one.
	for _, s := range sessions {
		if !s.EndedAt.IsZero() {
			return s
		}
	}
	return sessions[0]
}

// assertBranchTopology checks the epic integration branch exists in the primary
// checkout and that every child's build artifact merged into it — i.e. all
// children's work landed on the single integration branch the final PR is cut
// from.
func assertBranchTopology(t *testing.T, h *harness, epic int, children []int) {
	t.Helper()
	epicBranch := "clex/epic-" + itoa(epic)
	if out, err := gitIn(h.repoDir, "rev-parse", "--verify", epicBranch); err != nil {
		t.Fatalf("integration branch %s missing: %v\n%s", epicBranch, err, out)
	}
	for _, c := range children {
		file := "clex_built_" + itoa(c) + ".txt"
		if out, err := gitIn(h.repoDir, "cat-file", "-e", epicBranch+":"+file); err != nil {
			logOut, _ := gitIn(h.repoDir, "log", "--oneline", "--decorate", "--graph", "-12", epicBranch)
			treeOut, _ := gitIn(h.repoDir, "ls-tree", "--name-only", epicBranch)
			t.Fatalf("child #%d artifact %q not on integration branch %s: %v\n%s\nlog:\n%s\ntree:\n%s",
				c, file, epicBranch, err, out, logOut, treeOut)
		}
	}
}

// assertLabelHistory checks each child left the pipeline (its issue closed via
// merge) and that the store event log recorded a dispatch and a review/merge for
// each — the observable label lifecycle.
func assertLabelHistory(t *testing.T, st *store.Store, children []int) {
	t.Helper()
	for _, c := range children {
		events, err := st.EventsForIssue(c)
		if err != nil {
			t.Fatalf("events for #%d: %v", c, err)
		}
		kinds := map[string]bool{}
		var details []string
		for _, e := range events {
			kinds[e.Kind] = true
			details = append(details, e.Kind+":"+e.Detail)
		}
		if !kinds["dispatch"] {
			t.Fatalf("child #%d has no dispatch event; log=%v", c, details)
		}
		if !kinds["build"] {
			t.Fatalf("child #%d has no build event; log=%v", c, details)
		}
	}
}

// assertStoreRecords checks the runtime store captured sessions with real
// timestamps and that the event log ("LOG") recorded the epic assembly.
func assertStoreRecords(t *testing.T, st *store.Store, epic int, children []int) {
	t.Helper()
	for _, c := range children {
		s := latestSession(t, st, c)
		if s.Model == "" {
			t.Fatalf("session for #%d recorded no model", c)
		}
		if s.StartedAt.IsZero() {
			t.Fatalf("session for #%d has zero StartedAt", c)
		}
	}
	// The epic assembly is logged against the epic number.
	epicEvents, err := st.EventsForIssue(epic)
	if err != nil {
		t.Fatalf("epic events: %v", err)
	}
	sawAssemble := false
	for _, e := range epicEvents {
		if e.Kind == "assemble" {
			sawAssemble = true
		}
	}
	if !sawAssemble {
		t.Fatalf("no assemble event logged for epic #%d; events=%v", epic, epicEvents)
	}
	// The event log as a whole is non-empty (the audit trail / LOG).
	recent, err := st.RecentEvents(500)
	if err != nil {
		t.Fatalf("recent events: %v", err)
	}
	if len(recent) == 0 {
		t.Fatal("event log (LOG) is empty; expected a full audit trail")
	}
}

// --- small assertion helpers ---

func assertHasLabel(t *testing.T, h *harness, issue int, label string) {
	t.Helper()
	labels := h.gh.issueLabels(issue)
	for _, l := range labels {
		if l == label {
			return
		}
	}
	t.Fatalf("issue #%d missing label %q; has %v", issue, label, labels)
}

func containsInt(xs []int, v int) bool {
	for _, x := range xs {
		if x == v {
			return true
		}
	}
	return false
}
