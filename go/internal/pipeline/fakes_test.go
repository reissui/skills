package pipeline

import (
	"context"
	"fmt"
	"sync"

	"github.com/reissui/clex/internal/core"
	"github.com/reissui/clex/internal/gh"
	"github.com/reissui/clex/internal/registry"
	"github.com/reissui/clex/internal/skills"
)

// ---- fake GitHub ----------------------------------------------------------

// fakeGH is an in-memory GitHub double. It records every mutating call so tests
// can assert on effects (issues created, states set, PRs opened/merged,
// comments/reviews posted) and can script failures per method.
type fakeGH struct {
	mu sync.Mutex

	issues    map[int]*gh.Issue
	prs       map[int]*gh.PullRequest
	nextIssue int
	nextPR    int

	created   []*gh.Issue
	comments  map[int][]string
	reviews   map[int][]string // prNumber -> events
	setStates []stateSet
	openedPRs []*gh.PullRequest
	mergedPRs []int
	updates   map[int]string // issue -> new body

	// error injection: method name -> error to return (once semantics via
	// failOnce, or persistent via failAlways).
	failOnce   map[string]error
	failAlways map[string]error
}

type stateSet struct {
	Issue int
	To    core.State
}

func newFakeGH() *fakeGH {
	return &fakeGH{
		issues:     map[int]*gh.Issue{},
		prs:        map[int]*gh.PullRequest{},
		comments:   map[int][]string{},
		reviews:    map[int][]string{},
		updates:    map[int]string{},
		failOnce:   map[string]error{},
		failAlways: map[string]error{},
		nextIssue:  100,
		nextPR:     500,
	}
}

func (f *fakeGH) fail(method string) error {
	if err, ok := f.failAlways[method]; ok {
		return err
	}
	if err, ok := f.failOnce[method]; ok {
		delete(f.failOnce, method)
		return err
	}
	return nil
}

// seedIssue inserts an issue directly (for stages that read pre-existing state).
func (f *fakeGH) seedIssue(iss *gh.Issue) {
	f.issues[iss.Number] = iss
}

func (f *fakeGH) seedPR(pr *gh.PullRequest) {
	f.prs[pr.Number] = pr
}

func (f *fakeGH) GetIssue(ctx context.Context, repo gh.Repo, number int) (*gh.Issue, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.fail("GetIssue"); err != nil {
		return nil, err
	}
	iss, ok := f.issues[number]
	if !ok {
		return nil, fmt.Errorf("fakeGH: no issue #%d", number)
	}
	// Return a copy so callers cannot mutate our store by reference.
	cp := *iss
	return &cp, nil
}

func (f *fakeGH) CreateIssue(ctx context.Context, repo gh.Repo, title, body string, labels []string) (*gh.Issue, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.fail("CreateIssue"); err != nil {
		return nil, err
	}
	f.nextIssue++
	meta := gh.ParseMetadata(body)
	iss := &gh.Issue{
		Number: f.nextIssue,
		Title:  title,
		Body:   body,
		Labels: labels,
		Meta:   meta,
	}
	for _, l := range labels {
		st := core.State(l)
		if core.IsPipelineState(st) {
			iss.State = st
		}
		if st == core.StateEpic {
			iss.IsEpic = true
		}
	}
	f.issues[iss.Number] = iss
	cp := *iss
	f.created = append(f.created, &cp)
	return iss, nil
}

func (f *fakeGH) UpdateIssue(ctx context.Context, repo gh.Repo, number int, title, body *string) (*gh.Issue, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.fail("UpdateIssue"); err != nil {
		return nil, err
	}
	iss, ok := f.issues[number]
	if !ok {
		return nil, fmt.Errorf("fakeGH: update missing #%d", number)
	}
	if title != nil {
		iss.Title = *title
	}
	if body != nil {
		iss.Body = *body
		iss.Meta = gh.ParseMetadata(*body)
		f.updates[number] = *body
	}
	cp := *iss
	return &cp, nil
}

func (f *fakeGH) Comment(ctx context.Context, repo gh.Repo, number int, body string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.fail("Comment"); err != nil {
		return err
	}
	f.comments[number] = append(f.comments[number], body)
	return nil
}

func (f *fakeGH) SetState(ctx context.Context, repo gh.Repo, number int, to core.State) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.fail("SetState"); err != nil {
		return err
	}
	if iss, ok := f.issues[number]; ok {
		iss.State = to
	}
	f.setStates = append(f.setStates, stateSet{Issue: number, To: to})
	return nil
}

func (f *fakeGH) OpenPR(ctx context.Context, repo gh.Repo, title, head, base, body string) (*gh.PullRequest, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.fail("OpenPR"); err != nil {
		return nil, err
	}
	f.nextPR++
	pr := &gh.PullRequest{Number: f.nextPR, Title: title, Head: head, Base: base, State: "open"}
	f.prs[pr.Number] = pr
	cp := *pr
	f.openedPRs = append(f.openedPRs, &cp)
	return pr, nil
}

func (f *fakeGH) GetPR(ctx context.Context, repo gh.Repo, number int) (*gh.PullRequest, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.fail("GetPR"); err != nil {
		return nil, err
	}
	pr, ok := f.prs[number]
	if !ok {
		return nil, fmt.Errorf("fakeGH: no PR #%d", number)
	}
	cp := *pr
	return &cp, nil
}

func (f *fakeGH) ReviewPR(ctx context.Context, repo gh.Repo, number int, event, body string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.fail("ReviewPR"); err != nil {
		return err
	}
	f.reviews[number] = append(f.reviews[number], event)
	return nil
}

func (f *fakeGH) MergePR(ctx context.Context, repo gh.Repo, number int, method, commitMessage string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.fail("MergePR"); err != nil {
		return "", err
	}
	if pr, ok := f.prs[number]; ok {
		pr.Merged = true
		pr.State = "closed"
	}
	f.mergedPRs = append(f.mergedPRs, number)
	return fmt.Sprintf("sha-%d", number), nil
}

// ---- fake Workspace -------------------------------------------------------

type fakeWS struct {
	mu sync.Mutex

	root string

	epicBranches    []int
	worktrees       []wtRec
	rebasedOntoEpic []string
	rebasedEpicMain []int
	cleaned         []string

	failOnce   map[string]error
	failAlways map[string]error
}

type wtRec struct {
	Epic, Issue int
	Slug        string
	Path        string
}

func newFakeWS(root string) *fakeWS {
	return &fakeWS{root: root, failOnce: map[string]error{}, failAlways: map[string]error{}}
}

func (f *fakeWS) fail(method string) error {
	if err, ok := f.failAlways[method]; ok {
		return err
	}
	if err, ok := f.failOnce[method]; ok {
		delete(f.failOnce, method)
		return err
	}
	return nil
}

func (f *fakeWS) CreateEpicBranch(ctx context.Context, repoDir string, epicNum int) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.fail("CreateEpicBranch"); err != nil {
		return "", err
	}
	f.epicBranches = append(f.epicBranches, epicNum)
	return fmt.Sprintf("clex/epic-%d", epicNum), nil
}

func (f *fakeWS) CreateWorktree(ctx context.Context, repoDir string, epicNum, issueNum int, slug string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.fail("CreateWorktree"); err != nil {
		return "", err
	}
	path := f.WorktreePath(repoDir, issueNum, slug)
	f.worktrees = append(f.worktrees, wtRec{Epic: epicNum, Issue: issueNum, Slug: slug, Path: path})
	return path, nil
}

func (f *fakeWS) RebaseOntoEpic(ctx context.Context, worktreeDir string, epicNum int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.fail("RebaseOntoEpic"); err != nil {
		return err
	}
	f.rebasedOntoEpic = append(f.rebasedOntoEpic, worktreeDir)
	return nil
}

func (f *fakeWS) RebaseEpicOntoMain(ctx context.Context, repoDir string, epicNum int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.fail("RebaseEpicOntoMain"); err != nil {
		return err
	}
	f.rebasedEpicMain = append(f.rebasedEpicMain, epicNum)
	return nil
}

func (f *fakeWS) Cleanup(ctx context.Context, worktreeDir string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.fail("Cleanup"); err != nil {
		return err
	}
	f.cleaned = append(f.cleaned, worktreeDir)
	return nil
}

func (f *fakeWS) WorktreePath(repo string, issueNum int, slug string) string {
	return fmt.Sprintf("%s/worktrees/%d-%s", f.root, issueNum, slug)
}

// ---- fake Router ----------------------------------------------------------

// fakeRouter serves scripted options per role plus a scripted build decision and
// escalation.
type fakeRouter struct {
	available map[core.Role][]registry.RunOption
	warnings  map[core.Role][]registry.Warning
	build     registry.BuildDecision
	escalate  func(core.Model) (core.Model, bool)
}

func newFakeRouter() *fakeRouter {
	return &fakeRouter{
		available: map[core.Role][]registry.RunOption{},
		warnings:  map[core.Role][]registry.Warning{},
	}
}

func (f *fakeRouter) Available(role core.Role) ([]registry.RunOption, []registry.Warning) {
	return f.available[role], f.warnings[role]
}

func (f *fakeRouter) Build(difficulty core.Difficulty, opts registry.BuildOptions) registry.BuildDecision {
	return f.build
}

func (f *fakeRouter) Escalate(current core.Model) (core.Model, bool) {
	if f.escalate != nil {
		return f.escalate(current)
	}
	return core.Model{}, false
}

// ---- fake SkillResolver ---------------------------------------------------

type fakeSkills struct {
	resolved    []string
	symlinked   int
	renderedMD  int
	failSymlink error
	failRender  error
}

func (f *fakeSkills) Resolve(names []string, repoDir, userRoot string) []skills.SkillDir {
	f.resolved = append(f.resolved, names...)
	dirs := make([]skills.SkillDir, 0, len(names))
	for _, n := range names {
		dirs = append(dirs, skills.SkillDir{Name: n, Path: "/fake/skills/" + n})
	}
	return dirs
}

func (f *fakeSkills) SymlinkInto(worktree string, dirs []skills.SkillDir) error {
	if f.failSymlink != nil {
		return f.failSymlink
	}
	f.symlinked++
	return nil
}

func (f *fakeSkills) RenderAgentsMD(worktree string, dirs []skills.SkillDir) error {
	if f.failRender != nil {
		return f.failRender
	}
	f.renderedMD++
	return nil
}

// ---- fake Runner + factory ------------------------------------------------

// scriptedRunner emits a fixed sequence of events per Run call, cycling through
// scripts so a stage that runs the same model twice (e.g. plan then bounce) gets
// distinct outputs. A script is a list of events; the last should be a result or
// error to terminate cleanly.
type scriptedRunner struct {
	mu      sync.Mutex
	scripts [][]core.Event
	calls   int
	// runErr, if set, makes Run itself return an error (transport failure).
	runErr error
}

func (r *scriptedRunner) Run(ctx context.Context, task core.Task, dir string) (<-chan core.Event, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.runErr != nil {
		return nil, r.runErr
	}
	idx := r.calls
	if idx >= len(r.scripts) {
		idx = len(r.scripts) - 1
	}
	r.calls++
	script := r.scripts[idx]
	ch := make(chan core.Event, len(script)+1)
	for _, ev := range script {
		ch <- ev
	}
	close(ch)
	return ch, nil
}

func (r *scriptedRunner) Probe(ctx context.Context) (core.Availability, error) {
	return core.Availability{Healthy: true}, nil
}

// textThenResult builds a script: one text event carrying body, then a result.
func textThenResult(body string) []core.Event {
	return []core.Event{
		{Type: core.EventText, Text: body},
		{Type: core.EventResult, SessionID: "sess-1"},
	}
}

// errorScript builds a script terminating in an error event.
func errorScript(msg string) []core.Event {
	return []core.Event{{Type: core.EventError, Err: msg}}
}

// fakeFactory returns a runner per model. It can key runners by model id or fall
// back to a default; an unknown model yields an error to exercise routing gaps.
type fakeFactory struct {
	byModel map[string]Runner
	def     Runner
	err     error
}

func newFakeFactory(def Runner) *fakeFactory {
	return &fakeFactory{byModel: map[string]Runner{}, def: def}
}

func (f *fakeFactory) RunnerFor(model core.Model) (Runner, error) {
	if f.err != nil {
		return nil, f.err
	}
	if r, ok := f.byModel[model.ID]; ok {
		return r, nil
	}
	if f.def != nil {
		return f.def, nil
	}
	return nil, fmt.Errorf("fakeFactory: no runner for %s", model.ID)
}

// ---- shared test helpers --------------------------------------------------

func opt(id, provider, tier string) registry.RunOption {
	return registry.RunOption{
		Model: core.Model{ID: id, Provider: provider, Billing: core.BillingSubscription},
		Tier:  tier,
	}
}

func testRepo() gh.Repo { return gh.Repo{Owner: "o", Name: "r"} }

// buildDecisionFor returns a winning BuildDecision for a single model, as the
// router would produce for a build.
func buildDecisionFor(id, provider string) registry.BuildDecision {
	m := core.Model{ID: id, Provider: provider, Billing: core.BillingFree}
	cand := registry.Candidate{Option: registry.RunOption{Model: m, Tier: "local"}}
	return registry.BuildDecision{Winner: cand, Ranked: []registry.Candidate{cand}, Ok: true}
}

// bg is a background context for tests.
func bg() context.Context { return context.Background() }

// newTestPipeline wires a Pipeline with the given fakes and a standard config.
func newTestPipeline(deps Deps, cfg Config) *Pipeline {
	return New(deps, cfg)
}
