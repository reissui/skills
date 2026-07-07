package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/reissui/clex/internal/core"
	"github.com/reissui/clex/internal/gh"
)

func TestIdeaFilesLabelledIssue(t *testing.T) {
	e := newTestEnv(t)
	fgh := e.newGH.mustFake(t)
	code := run(e, []string{"idea", "add", "a", "health", "endpoint"})
	if code != exitOK {
		t.Fatalf("idea exit = %d, want 0 (stderr: %s)", code, errBuf(e))
	}
	if len(fgh.createdIssues) != 1 {
		t.Fatalf("expected 1 created issue, got %d", len(fgh.createdIssues))
	}
	got := fgh.createdIssues[0]
	if got.repo.String() != "acme/widgets" {
		t.Errorf("idea repo = %s, want acme/widgets (from git origin)", got.repo)
	}
	if got.title != "add a health endpoint" {
		t.Errorf("idea title = %q", got.title)
	}
	if len(got.labels) != 1 || got.labels[0] != string(core.StateIdea) {
		t.Errorf("idea labels = %v, want [%s]", got.labels, core.StateIdea)
	}
}

func TestIdeaExplicitRepoFlagWins(t *testing.T) {
	e := newTestEnv(t)
	fgh := e.newGH.mustFake(t)
	code := run(e, []string{"idea", "--repo", "other/proj", "do a thing"})
	if code != exitOK {
		t.Fatalf("idea exit = %d, want 0", code)
	}
	if fgh.createdIssues[0].repo.String() != "other/proj" {
		t.Fatalf("--repo should win; got %s", fgh.createdIssues[0].repo)
	}
}

func TestIdeaJSON(t *testing.T) {
	e := newTestEnv(t)
	e.newGH.mustFake(t)
	code := run(e, []string{"idea", "--json", "ship it"})
	if code != exitOK {
		t.Fatalf("idea --json exit = %d, want 0", code)
	}
	var got struct {
		OK    bool   `json:"ok"`
		Repo  string `json:"repo"`
		Issue int    `json:"issue"`
	}
	if err := json.Unmarshal(outBuf(e).Bytes(), &got); err != nil {
		t.Fatalf("idea --json invalid: %v\n%s", err, outBuf(e))
	}
	if !got.OK || got.Issue == 0 {
		t.Fatalf("unexpected idea json: %+v", got)
	}
}

func TestIdeaNoRepoNoOriginErrors(t *testing.T) {
	e := newTestEnv(t)
	e.originRemote = func() (string, error) { return "", errNoRemote{} }
	code := run(e, []string{"idea", "something"})
	if code != exitError {
		t.Fatalf("idea with no repo: exit = %d, want 1", code)
	}
	if !strings.Contains(errBuf(e).String(), "--repo") {
		t.Fatalf("expected --repo hint; got: %s", errBuf(e))
	}
}

func TestPlanSetsIdeaState(t *testing.T) {
	e := newTestEnv(t)
	fgh := e.newGH.mustFake(t)
	code := run(e, []string{"plan", "42"})
	if code != exitOK {
		t.Fatalf("plan exit = %d, want 0 (stderr: %s)", code, errBuf(e))
	}
	if len(fgh.setStateCalls) != 1 || fgh.setStateCalls[0].to != core.StateIdea || fgh.setStateCalls[0].number != 42 {
		t.Fatalf("plan should SetState #42 to idea; got %+v", fgh.setStateCalls)
	}
}

func TestBuildApprovesIssue(t *testing.T) {
	e := newTestEnv(t)
	fgh := e.newGH.mustFake(t)
	code := run(e, []string{"build", "#7"})
	if code != exitOK {
		t.Fatalf("build exit = %d, want 0", code)
	}
	if len(fgh.setStateCalls) != 1 || fgh.setStateCalls[0].to != core.StateApproved || fgh.setStateCalls[0].number != 7 {
		t.Fatalf("build should SetState #7 to approved; got %+v", fgh.setStateCalls)
	}
}

// clex build <epic#> passes the plan gate for the whole epic: every planned
// child (linked via Depends-on) is approved; unrelated and already-building
// issues are untouched.
func TestBuildEpicApprovesPlannedChildren(t *testing.T) {
	e := newTestEnv(t)
	fgh := e.newGH.mustFake(t)
	fgh.seedIssue(&gh.Issue{Number: 90, Title: "Epic: widgets", IsEpic: true})
	fgh.seedIssue(&gh.Issue{Number: 91, State: core.StatePlanned, Meta: gh.Metadata{DependsOn: []int{90}}})
	fgh.seedIssue(&gh.Issue{Number: 92, State: core.StatePlanned, Meta: gh.Metadata{DependsOn: []int{90, 91}}})
	fgh.seedIssue(&gh.Issue{Number: 93, State: core.StateBuilding, Meta: gh.Metadata{DependsOn: []int{90}}})
	fgh.seedIssue(&gh.Issue{Number: 99, State: core.StatePlanned}) // no epic link

	code := run(e, []string{"build", "90"})
	if code != exitOK {
		t.Fatalf("build epic exit = %d, want 0\n%s", code, errBuf(e))
	}
	approved := map[int]bool{}
	for _, c := range fgh.setStateCalls {
		if c.to != core.StateApproved {
			t.Fatalf("unexpected transition %+v", c)
		}
		approved[c.number] = true
	}
	if !approved[91] || !approved[92] || len(approved) != 2 {
		t.Fatalf("approved = %v, want exactly {91, 92}", approved)
	}
	if !strings.Contains(outBuf(e).String(), "approved 2 issues of epic #90") {
		t.Fatalf("output = %q", outBuf(e).String())
	}
}

// An epic with nothing planned fails loudly rather than pretending.
func TestBuildEpicWithoutPlannedChildrenFails(t *testing.T) {
	e := newTestEnv(t)
	fgh := e.newGH.mustFake(t)
	fgh.seedIssue(&gh.Issue{Number: 90, Title: "Epic: empty", IsEpic: true})

	if code := run(e, []string{"build", "90"}); code == exitOK {
		t.Fatal("build of childless epic should fail")
	}
	if len(fgh.setStateCalls) != 0 {
		t.Fatalf("no SetState expected; got %+v", fgh.setStateCalls)
	}
}

func TestBuildJSON(t *testing.T) {
	e := newTestEnv(t)
	e.newGH.mustFake(t)
	code := run(e, []string{"build", "--json", "7"})
	if code != exitOK {
		t.Fatalf("build --json exit = %d, want 0", code)
	}
	var got struct {
		OK    bool   `json:"ok"`
		State string `json:"state"`
		Issue int    `json:"issue"`
	}
	if err := json.Unmarshal(outBuf(e).Bytes(), &got); err != nil {
		t.Fatalf("build --json invalid: %v\n%s", err, outBuf(e))
	}
	if !got.OK || got.State != string(core.StateApproved) || got.Issue != 7 {
		t.Fatalf("unexpected build json: %+v", got)
	}
}

func TestPlanRequiresIssue(t *testing.T) {
	e := newTestEnv(t)
	e.newGH.mustFake(t)
	code := run(e, []string{"plan"})
	if code != exitError {
		t.Fatalf("plan without issue: exit = %d, want 1", code)
	}
}

// errNoRemote is a sentinel error the git-origin fake returns when there is no
// remote, so tests exercise the "no repo" path deterministically.
type errNoRemote struct{}

func (errNoRemote) Error() string { return "no origin remote" }
