package pipeline

import (
	"context"
	"errors"
	"fmt"

	"github.com/reissui/clex/internal/core"
	"github.com/reissui/clex/internal/gh"
	"github.com/reissui/clex/internal/registry"
	"github.com/reissui/clex/internal/skills"
	"github.com/reissui/clex/internal/workspace"
)

// buildSkills are injected into the build worktree.
var buildSkills = []string{"clex-plan"}

// BuildResult is the typed outcome of the build stage.
type BuildResult struct {
	// WorktreeDir is the worktree the build ran in.
	WorktreeDir string
	// Model is the model chosen by the router for this build.
	Model core.Model
	// PRNumber is the opened (or pre-existing) PR number targeting the epic
	// integration branch. Zero on failure before a PR exists.
	PRNumber int
	// Verification records which command ran and whether it was trusted or
	// substituted (spec: Security model — verification-command trust).
	Verification VerificationPlan
	// SessionID is the runner session id, kept for resume on retry/escalation.
	SessionID string
}

// Build runs the build stage for one child issue under an epic: it ensures the
// epic integration branch and the issue worktree exist, injects skills, runs the
// build model with the scoped context, runs the (trust-resolved) verification
// command in the worktree, and opens a PR targeting the integration branch.
//
// On any failure it reverts the issue to clex:approved, posts a failure comment,
// and returns the error so the daemon can surface retry/escalate/skip (spec:
// Error handling & safety). The escalation DECISION comes from the registry
// ladder and is wired at #16; Build exposes EscalateModel as the hook.
//
// Idempotency: Build re-derives state from GitHub and disk. Ensuring the epic
// branch and worktree tolerates their prior existence (an "already exists" git
// error is treated as success). To avoid opening a duplicate PR after a crash,
// the caller passes existingPRNumber (the open issue-branch PR discovered at a
// higher layer); when non-zero Build reuses it instead of opening another.
func (p *Pipeline) Build(ctx context.Context, epicNum int, issue *gh.Issue, k KnowledgeExcerpts, existingPRNumber int) (BuildResult, error) {
	slug := slugify(issue.Title)

	// 1. Ensure epic integration branch (idempotent in the workspace manager:
	//    creating an existing branch is tolerated there; we tolerate its error
	//    shape defensively).
	if _, err := p.deps.WS.CreateEpicBranch(ctx, p.cfg.RepoDir, epicNum); err != nil && !isAlreadyExists(err) {
		return BuildResult{}, p.failBuild(ctx, issue, fmt.Errorf("build: ensure epic branch: %w", err))
	}

	// 2. Ensure the issue worktree.
	wtPath := p.deps.WS.WorktreePath(p.cfg.RepoDir, issue.Number, slug)
	if _, err := p.deps.WS.CreateWorktree(ctx, p.cfg.RepoDir, epicNum, issue.Number, slug); err != nil && !isAlreadyExists(err) {
		return BuildResult{}, p.failBuild(ctx, issue, fmt.Errorf("build: ensure worktree: %w", err))
	}

	// 3. Inject skills into the worktree.
	dirs := p.deps.Skills.Resolve(buildSkills, p.cfg.RepoDir, p.cfg.UserSkillsRoot)
	if err := p.injectSkills(wtPath, dirs); err != nil {
		return BuildResult{}, p.failBuild(ctx, issue, fmt.Errorf("build: inject skills: %w", err))
	}

	// 4. Resolve the verification command under the trust policy BEFORE running
	//    the model, so the plan (and any substitution) is known and surfaced.
	vplan := p.resolveVerification(issue)

	// 5. Pick the build model via the router (success×speed×cost) and run it.
	dec := p.deps.Router.Build(issue.Meta.Difficulty, registry.BuildOptions{})
	if !dec.Ok {
		return BuildResult{}, p.failBuild(ctx, issue, fmt.Errorf("%w: build role", ErrNoModel))
	}
	model := dec.Winner.Option.Model
	runner, err := p.deps.Runners.RunnerFor(model)
	if err != nil {
		return BuildResult{}, p.failBuild(ctx, issue, fmt.Errorf("build: runner for %s: %w", model.ID, err))
	}

	res := BuildResult{WorktreeDir: wtPath, Model: model, Verification: vplan, PRNumber: existingPRNumber}

	prompt := buildBuildPrompt(BuildContext{Issue: issue, Verify: vplan, Knowledge: k})
	rr, err := runToCompletion(ctx, runner, core.Task{
		Repo:   p.cfg.Repo.String(),
		Prompt: prompt,
		Issue:  issue.Number,
		Skills: buildSkills,
		Effort: dec.Winner.Option.Effort,
		Fast:   dec.Winner.Option.Fast,
	}, wtPath)
	if err != nil {
		return res, p.failBuild(ctx, issue, fmt.Errorf("build: model run: %w", err))
	}
	res.SessionID = rr.SessionID

	// 6. Run the resolved verification command in the worktree.
	if err := wrapVerifyErr(p.verifier().run(ctx, wtPath, vplan.Command)); err != nil {
		return res, p.failBuild(ctx, issue, err)
	}

	// 7. Open a PR targeting the epic integration branch (unless one already
	//    exists for this run).
	if res.PRNumber == 0 {
		head := workspace.IssueBranch(issue.Number, slug)
		base := workspace.EpicBranch(epicNum)
		body := fmt.Sprintf("Automated build for #%d.\n\nVerification: `%s`%s",
			issue.Number, vplan.Command, substitutionNote(vplan))
		pr, err := p.deps.GH.OpenPR(ctx, p.cfg.Repo, issue.Title, head, base, body)
		if err != nil {
			return res, p.failBuild(ctx, issue, fmt.Errorf("build: open PR: %w", err))
		}
		res.PRNumber = pr.Number
	}

	// 8. Move the issue to review.
	if err := p.deps.GH.SetState(ctx, p.cfg.Repo, issue.Number, core.StateReview); err != nil {
		return res, fmt.Errorf("build: set review state: %w", err)
	}
	return res, nil
}

// EscalateModel is the hook the daemon (#16) calls after a build fails twice: it
// asks the registry ladder for the next model up. Build itself never decides to
// escalate — it only exposes the current model's successor so the wiring layer
// can re-dispatch with the failed diff carried forward.
func (p *Pipeline) EscalateModel(current core.Model) (core.Model, bool) {
	return p.deps.Router.Escalate(current)
}

// failBuild performs the common failure path: revert the issue to approved and
// post a failure comment, then return the original error (wrapped). Errors from
// the revert/comment are joined so nothing is lost, but the build error is the
// primary cause.
func (p *Pipeline) failBuild(ctx context.Context, issue *gh.Issue, cause error) error {
	var errs []error
	errs = append(errs, cause)
	// Revert to approved so a retry can re-dispatch (spec: runner failure →
	// clex:approved). Tolerate an already-approved issue.
	if err := p.deps.GH.SetState(ctx, p.cfg.Repo, issue.Number, core.StateApproved); err != nil && !gh.IsTransitionError(err) {
		errs = append(errs, fmt.Errorf("revert to approved: %w", err))
	}
	comment := fmt.Sprintf("clex build failed for #%d: %s", issue.Number, oneLine(cause.Error()))
	if err := p.deps.GH.Comment(ctx, p.cfg.Repo, issue.Number, comment); err != nil {
		errs = append(errs, fmt.Errorf("post failure comment: %w", err))
	}
	return errors.Join(errs...)
}

// injectSkills symlinks and renders the resolved skills into the worktree. Both
// injection mechanisms are attempted so either runner flavor (Claude symlink /
// Codex AGENTS.md) finds them; a nil dirs slice is a no-op.
func (p *Pipeline) injectSkills(worktree string, dirs []skills.SkillDir) error {
	if len(dirs) == 0 {
		return nil
	}
	if err := p.deps.Skills.SymlinkInto(worktree, dirs); err != nil {
		return err
	}
	return p.deps.Skills.RenderAgentsMD(worktree, dirs)
}

// verifier returns the command executor for verification. It is overridable in
// tests via testVerifier; production uses shellVerifier.
func (p *Pipeline) verifier() verifyRunner {
	if p.testVerify != nil {
		return p.testVerify
	}
	return shellVerifier{}
}

// substitutionNote returns a parenthetical note when the verification command was
// substituted, else "".
func substitutionNote(v VerificationPlan) string {
	if v.Substituted {
		return " (" + v.Reason + ")"
	}
	return ""
}

// isAlreadyExists reports whether err indicates a resource the workspace manager
// already created (branch/worktree). The workspace manager returns git errors
// for "already exists"; we match on the wrapped GitError text conservatively so
// a genuinely new failure still propagates.
func isAlreadyExists(err error) bool {
	if err == nil {
		return false
	}
	var ge *workspace.GitError
	if errors.As(err, &ge) {
		s := ge.Stderr
		return containsAny(s, "already exists", "already checked out", "is already used by worktree")
	}
	return false
}
