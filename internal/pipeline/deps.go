package pipeline

import (
	"context"

	"github.com/reissui/clex/internal/core"
	"github.com/reissui/clex/internal/gh"
	"github.com/reissui/clex/internal/registry"
	"github.com/reissui/clex/internal/skills"
)

// This file declares the narrow interfaces the pipeline stages depend on. Each
// interface captures only the methods a stage actually calls, so the concrete
// packages (gh, workspace, registry, runners) can be faked in tests without any
// live GitHub/git/CLI calls. The concrete types satisfy these interfaces via the
// compile-time assertions at the bottom of the file; see wiring_assert.go.
//
// Design rationale (issue #15): "Dependencies (gh client, workspace, registry,
// skills, runners) are injected as interfaces; tests use fakes — this issue
// writes no live integrations."

// GitHub is the subset of *gh.Client the stages use to read/write issues,
// comments, labels, and pull requests. Every method mirrors the concrete
// signature exactly so *gh.Client satisfies it.
type GitHub interface {
	GetIssue(ctx context.Context, repo gh.Repo, number int) (*gh.Issue, error)
	CreateIssue(ctx context.Context, repo gh.Repo, title, body string, labels []string) (*gh.Issue, error)
	UpdateIssue(ctx context.Context, repo gh.Repo, number int, title, body *string) (*gh.Issue, error)
	Comment(ctx context.Context, repo gh.Repo, number int, body string) error
	SetState(ctx context.Context, repo gh.Repo, number int, to core.State) error
	OpenPR(ctx context.Context, repo gh.Repo, title, head, base, body string) (*gh.PullRequest, error)
	GetPR(ctx context.Context, repo gh.Repo, number int) (*gh.PullRequest, error)
	ReviewPR(ctx context.Context, repo gh.Repo, number int, event, body string) error
	MergePR(ctx context.Context, repo gh.Repo, number int, method, commitMessage string) (string, error)
}

// Workspace is the subset of *workspace.Manager the build and assemble stages
// use to manage the epic integration branch and per-issue worktrees.
type Workspace interface {
	CreateEpicBranch(ctx context.Context, repoDir string, epicNum int) (string, error)
	CreateWorktree(ctx context.Context, repoDir string, epicNum, issueNum int, slug string) (string, error)
	RebaseOntoEpic(ctx context.Context, worktreeDir string, epicNum int) error
	RebaseEpicOntoMain(ctx context.Context, repoDir string, epicNum int) error
	Cleanup(ctx context.Context, worktreeDir string) error
	WorktreePath(repo string, issueNum int, slug string) string
}

// Router is the subset of *registry.Registry the build stage uses to pick a
// build model and escalate on failure. Plan and review resolve their models via
// Available.
type Router interface {
	Available(role core.Role) ([]registry.RunOption, []registry.Warning)
	Build(difficulty core.Difficulty, opts registry.BuildOptions) registry.BuildDecision
	Escalate(current core.Model) (core.Model, bool)
}

// SkillResolver is the subset of the skills package the stages use to resolve
// and inject skills into a worktree. It is an interface (rather than direct
// package-function calls) so tests can stub injection without touching the
// filesystem. The concrete implementation (skillsAdapter) forwards to the
// package functions.
type SkillResolver interface {
	Resolve(names []string, repoDir, userRoot string) []skills.SkillDir
	SymlinkInto(worktree string, dirs []skills.SkillDir) error
	RenderAgentsMD(worktree string, dirs []skills.SkillDir) error
}

// Runner is re-exported from core for convenience; a stage receives runners via
// a RunnerFactory keyed by the chosen model.
type Runner = core.Runner

// RunnerFactory returns the runner that executes a given model. The registry
// owns the provider→runner mapping; the pipeline only needs "give me something
// that can run this model". Tests inject a fake factory that returns a scripted
// runner. It returns an error when no runner is registered for the model's
// provider so a stage can surface a routing gap rather than panic.
type RunnerFactory interface {
	RunnerFor(model core.Model) (Runner, error)
}

// RunnerFactoryFunc adapts a plain function to RunnerFactory.
type RunnerFactoryFunc func(model core.Model) (Runner, error)

// RunnerFor implements RunnerFactory.
func (f RunnerFactoryFunc) RunnerFor(model core.Model) (Runner, error) { return f(model) }

// skillsAdapter forwards SkillResolver calls to the concrete skills package
// functions. It is the production implementation injected into Deps.
type skillsAdapter struct{}

// SkillsAdapter returns the production SkillResolver backed by the skills
// package.
func SkillsAdapter() SkillResolver { return skillsAdapter{} }

func (skillsAdapter) Resolve(names []string, repoDir, userRoot string) []skills.SkillDir {
	return skills.Resolve(names, repoDir, userRoot)
}

func (skillsAdapter) SymlinkInto(worktree string, dirs []skills.SkillDir) error {
	return skills.SymlinkInto(worktree, dirs)
}

func (skillsAdapter) RenderAgentsMD(worktree string, dirs []skills.SkillDir) error {
	return skills.RenderAgentsMD(worktree, dirs)
}
