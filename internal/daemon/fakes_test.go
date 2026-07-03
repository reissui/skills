package daemon

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/reissui/clex/internal/config"
	"github.com/reissui/clex/internal/core"
	"github.com/reissui/clex/internal/gh"
	"github.com/reissui/clex/internal/pipeline"
	"github.com/reissui/clex/internal/registry"
	"github.com/reissui/clex/internal/skills"
	"github.com/reissui/clex/internal/telegram"
)

// ---- fake GitHub port -------------------------------------------------------

// fakeGH is an in-memory GitHubPort. It is the source of truth in tests, exactly
// as real GitHub labels are in production. All methods are concurrency-safe
// because the daemon reads issues from the loop goroutine while control handlers
// may write concurrently.
type fakeGH struct {
	mu       sync.Mutex
	repo     gh.Repo
	issues   map[int]*gh.Issue
	comments map[int][]string
	changes  chan gh.Change
	// setStateCalls records every SetState transition for assertions.
	setStateCalls []stateChange
}

type stateChange struct {
	issue int
	to    core.State
}

func newFakeGH(repo gh.Repo) *fakeGH {
	return &fakeGH{
		repo:     repo,
		issues:   make(map[int]*gh.Issue),
		comments: make(map[int][]string),
		changes:  make(chan gh.Change, 64),
	}
}

// seed adds or replaces an issue.
func (f *fakeGH) seed(iss *gh.Issue) {
	f.mu.Lock()
	defer f.mu.Unlock()
	cp := *iss
	f.issues[iss.Number] = &cp
}

func (f *fakeGH) Poll(ctx context.Context, _ []gh.Repo, _ time.Duration, _ gh.PollOptions) <-chan gh.Change {
	out := make(chan gh.Change)
	go func() {
		defer close(out)
		for {
			select {
			case <-ctx.Done():
				return
			case ch, ok := <-f.changes:
				if !ok {
					return
				}
				select {
				case out <- ch:
				case <-ctx.Done():
					return
				}
			}
		}
	}()
	return out
}

func (f *fakeGH) emit(ch gh.Change) { f.changes <- ch }

func (f *fakeGH) ListIssues(_ context.Context, _ gh.Repo) ([]*gh.Issue, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []*gh.Issue
	for _, iss := range f.issues {
		cp := *iss
		out = append(out, &cp)
	}
	return out, nil
}

func (f *fakeGH) GetIssue(_ context.Context, _ gh.Repo, number int) (*gh.Issue, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	iss, ok := f.issues[number]
	if !ok {
		return nil, fmt.Errorf("fakeGH: issue #%d not found", number)
	}
	cp := *iss
	return &cp, nil
}

func (f *fakeGH) SetState(_ context.Context, _ gh.Repo, number int, to core.State) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	iss, ok := f.issues[number]
	if !ok {
		return fmt.Errorf("fakeGH: issue #%d not found", number)
	}
	iss.State = to
	f.setStateCalls = append(f.setStateCalls, stateChange{issue: number, to: to})
	return nil
}

func (f *fakeGH) Comment(_ context.Context, _ gh.Repo, number int, body string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.comments[number] = append(f.comments[number], body)
	return nil
}

func (f *fakeGH) UpdateIssue(_ context.Context, _ gh.Repo, number int, title, body *string) (*gh.Issue, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	iss, ok := f.issues[number]
	if !ok {
		return nil, fmt.Errorf("fakeGH: issue #%d not found", number)
	}
	if title != nil {
		iss.Title = *title
	}
	if body != nil {
		iss.Body = *body
	}
	cp := *iss
	return &cp, nil
}

// stateOf returns the current label state of an issue (test helper).
func (f *fakeGH) stateOf(number int) core.State {
	f.mu.Lock()
	defer f.mu.Unlock()
	if iss, ok := f.issues[number]; ok {
		return iss.State
	}
	return ""
}

// commentsOf returns the comments posted to an issue (test helper).
func (f *fakeGH) commentsOf(number int) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.comments[number]...)
}

func (f *fakeGH) transitionsFor(number int) []core.State {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []core.State
	for _, sc := range f.setStateCalls {
		if sc.issue == number {
			out = append(out, sc.to)
		}
	}
	return out
}

// ---- fake Telegram port -----------------------------------------------------

// fakeTG is an in-memory TelegramPort. It records sent lines, exposes registered
// command handlers so tests can invoke a /command, and answers Ask from a
// scripted queue (for cost-gate confirm tests).
type fakeTG struct {
	mu       sync.Mutex
	lines    []string
	handlers map[string]telegram.CommandHandler
	answers  []telegram.Answer
	askCalls int
}

func newFakeTG() *fakeTG {
	return &fakeTG{handlers: make(map[string]telegram.CommandHandler)}
}

func (f *fakeTG) SendLine(_ context.Context, text string) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lines = append(f.lines, text)
	return len(f.lines), nil
}

func (f *fakeTG) Ask(_ context.Context, _ telegram.Question) (telegram.Answer, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.askCalls++
	if len(f.answers) == 0 {
		// Default: accept the proposal.
		return telegram.Answer{Text: "proceed"}, nil
	}
	ans := f.answers[0]
	f.answers = f.answers[1:]
	return ans, nil
}

func (f *fakeTG) Handle(name string, h telegram.CommandHandler) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.handlers[name] = h
}

// sentContains reports whether any sent line contains sub (test helper).
func (f *fakeTG) sentContains(sub string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, l := range f.lines {
		if contains(l, sub) {
			return true
		}
	}
	return false
}

func (f *fakeTG) queueAnswer(a telegram.Answer) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.answers = append(f.answers, a)
}

// ---- fake runner + factory --------------------------------------------------

// fakeRunner is a scripted core.Runner. It records the tasks it was handed (to
// assert steer resume ids and prompts) and emits a scripted terminal event. The
// child-env allowlist is proven separately against the REAL claude adapter in
// TestChildEnvAllowlistDropsCanary, which is where that boundary actually lives.
type fakeRunner struct {
	mu        sync.Mutex
	runs      []core.Task
	sessionID string
	runCount  int
}

func (r *fakeRunner) Run(ctx context.Context, task core.Task, _ string) (<-chan core.Event, error) {
	r.mu.Lock()
	r.runs = append(r.runs, task)
	r.runCount++
	n := r.runCount
	sid := r.sessionID
	if sid == "" {
		sid = fmt.Sprintf("sess-%d", n)
	}
	r.mu.Unlock()

	out := make(chan core.Event, 4)
	go func() {
		defer close(out)
		select {
		case <-ctx.Done():
			out <- core.Event{Type: core.EventError, Err: "cancelled"}
			return
		default:
		}
		out <- core.Event{Type: core.EventText, Text: "working"}
		out <- core.Event{Type: core.EventResult, SessionID: sid, Tokens: core.Usage{In: 10, Out: 5}}
	}()
	return out, nil
}

func (r *fakeRunner) Probe(context.Context) (core.Availability, error) {
	return core.Availability{Healthy: true}, nil
}

func (r *fakeRunner) tasks() []core.Task {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]core.Task(nil), r.runs...)
}

// fakeFactory returns the same fakeRunner for any model.
type fakeFactory struct {
	runner *fakeRunner
}

func (f *fakeFactory) RunnerFor(core.Model) (pipeline.Runner, error) { return f.runner, nil }

var _ pipeline.RunnerFactory = (*fakeFactory)(nil)

// ---- fakes to construct the REAL pipeline (for TestRealPipelineComposes) ----

// registryFor builds a real registry for cfg with runner backing its provider.
func registryFor(cfg *config.Config, runner core.Runner) *registry.Registry {
	runners := make(map[string]core.Runner)
	for _, m := range cfg.Models {
		runners[m.Provider] = runner
	}
	return registry.New(cfg, runners)
}

// fakePipelineGH satisfies pipeline.GitHub with inert no-ops. It is only used to
// construct the real pipeline for the composition smoke test; no stage is run.
type fakePipelineGH struct{}

func (fakePipelineGH) GetIssue(context.Context, gh.Repo, int) (*gh.Issue, error) {
	return &gh.Issue{}, nil
}
func (fakePipelineGH) CreateIssue(context.Context, gh.Repo, string, string, []string) (*gh.Issue, error) {
	return &gh.Issue{}, nil
}
func (fakePipelineGH) UpdateIssue(context.Context, gh.Repo, int, *string, *string) (*gh.Issue, error) {
	return &gh.Issue{}, nil
}
func (fakePipelineGH) Comment(context.Context, gh.Repo, int, string) error      { return nil }
func (fakePipelineGH) SetState(context.Context, gh.Repo, int, core.State) error { return nil }
func (fakePipelineGH) OpenPR(context.Context, gh.Repo, string, string, string, string) (*gh.PullRequest, error) {
	return &gh.PullRequest{}, nil
}
func (fakePipelineGH) GetPR(context.Context, gh.Repo, int) (*gh.PullRequest, error) {
	return &gh.PullRequest{}, nil
}
func (fakePipelineGH) ReviewPR(context.Context, gh.Repo, int, string, string) error { return nil }
func (fakePipelineGH) MergePR(context.Context, gh.Repo, int, string, string) (string, error) {
	return "sha", nil
}

// fakeWorkspace satisfies pipeline.Workspace with inert paths (no git).
type fakeWorkspace struct{}

func (fakeWorkspace) CreateEpicBranch(context.Context, string, int) (string, error) {
	return "clex/epic-1", nil
}
func (fakeWorkspace) CreateWorktree(context.Context, string, int, int, string) (string, error) {
	return "/fake/wt", nil
}
func (fakeWorkspace) RebaseOntoEpic(context.Context, string, int) error     { return nil }
func (fakeWorkspace) RebaseEpicOntoMain(context.Context, string, int) error { return nil }
func (fakeWorkspace) Cleanup(context.Context, string) error                 { return nil }
func (fakeWorkspace) WorktreePath(string, int, string) string               { return "/fake/wt" }

// fakeSkills satisfies pipeline.SkillResolver with no-ops.
type fakeSkills struct{}

func (fakeSkills) Resolve([]string, string, string) []skills.SkillDir { return nil }
func (fakeSkills) SymlinkInto(string, []skills.SkillDir) error        { return nil }
func (fakeSkills) RenderAgentsMD(string, []skills.SkillDir) error     { return nil }

// contains is strings.Contains without importing strings in every test file.
func contains(s, sub string) bool {
	return len(sub) == 0 || (len(s) >= len(sub) && indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
