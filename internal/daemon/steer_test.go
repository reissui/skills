package daemon

import (
	"context"
	"testing"
	"time"

	"github.com/reissui/clex/internal/core"
	"github.com/reissui/clex/internal/gh"
)

// --- Criterion: steer reaches an active fake runner as a resumed turn.
//
// With a build in flight, steering #60 must inject the text into the runner as a
// resumed session turn (ResumeID set), not a fresh run. We assert the fake
// runner received a task carrying the steering text and a resume id.
func TestSteerActiveRunnerResumesSession(t *testing.T) {
	stages := newFakeStages()
	gate := stages.holdBuild(60)
	h := newHarness(t, stages)
	h.approvedIssue(60, nil, []string{"z/**"})
	h.runDaemon(t)

	// Wait for the build to be active and give the runState a session id.
	if !waitFor(time.Second, func() bool {
		h.d.mu.Lock()
		defer h.d.mu.Unlock()
		rs, ok := h.d.running[60]
		if ok {
			rs.sessionID = "resume-123" // simulate a session id learned mid-build
		}
		return ok
	}) {
		t.Fatal("build #60 never became active")
	}

	msg := h.d.submitControl(context.Background(), controlAction{kind: ctlSteer, issue: 60, text: "add a benchmark", reply: make(chan string, 1)})
	if !contains(msg, "active runner") {
		t.Fatalf("steer reply = %q, want active-runner delivery", msg)
	}

	// The fake runner should have received a resumed turn with the steering text.
	if !waitFor(time.Second, func() bool {
		for _, task := range h.rf.runner.tasks() {
			if task.ResumeID == "resume-123" && contains(task.Prompt, "add a benchmark") {
				return true
			}
		}
		return false
	}) {
		t.Fatalf("runner did not receive a resumed steer turn; tasks=%+v", h.rf.runner.tasks())
	}
	close(gate)
}

// --- Criterion: idle steer edits the issue body.
//
// Steering an issue with no active runner appends a Steering note to its body.
func TestSteerIdleIssueEditsBody(t *testing.T) {
	stages := newFakeStages()
	h := newHarness(t, stages)
	h.gh.seed(&gh.Issue{
		Number:      61,
		Title:       "idle",
		Body:        "original body",
		AuthorLogin: "acme",
		State:       core.StateApproved,
		Meta:        gh.Metadata{Touches: []string{"idle/**"}},
	})
	// Do NOT run the daemon loop, so nothing dispatches; the issue stays idle.

	msg := h.d.steer(context.Background(), 61, "prefer smaller functions")
	if !contains(msg, "idle") {
		t.Fatalf("steer reply = %q, want idle handling", msg)
	}
	iss, _ := h.gh.GetIssue(context.Background(), testRepo, 61)
	if !contains(iss.Body, "## Steering") || !contains(iss.Body, "prefer smaller functions") {
		t.Fatalf("idle steer did not append to body; body=%q", iss.Body)
	}
}

// --- Criterion: epic steer updates the PRD body and propagates to children.
func TestSteerEpicPropagates(t *testing.T) {
	stages := newFakeStages()
	h := newHarness(t, stages)
	// Epic issue.
	h.gh.seed(&gh.Issue{
		Number:      70,
		Title:       "Epic: thing",
		Body:        "PRD",
		AuthorLogin: "acme",
		State:       core.StateApproved,
		IsEpic:      true,
	})
	// Unstarted child that depends on the epic.
	h.gh.seed(&gh.Issue{
		Number:      71,
		Title:       "child",
		Body:        "child body",
		AuthorLogin: "acme",
		State:       core.StateApproved,
		Meta:        gh.Metadata{DependsOn: []int{70}, Touches: []string{"c/**"}},
	})

	msg := h.d.steer(context.Background(), 70, "switch to Postgres")
	if !contains(msg, "epic") {
		t.Fatalf("epic steer reply = %q", msg)
	}
	epic, _ := h.gh.GetIssue(context.Background(), testRepo, 70)
	if !contains(epic.Body, "## Steering") || !contains(epic.Body, "switch to Postgres") {
		t.Fatalf("epic body not updated; body=%q", epic.Body)
	}
	child, _ := h.gh.GetIssue(context.Background(), testRepo, 71)
	if !contains(child.Body, "switch to Postgres") {
		t.Fatalf("steer did not propagate to unstarted child; body=%q", child.Body)
	}
}
