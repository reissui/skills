package pipeline

import (
	"context"
	"fmt"
	"strings"

	"github.com/reissui/clex/internal/core"
	"github.com/reissui/clex/internal/gh"
)

// planSkills are the skills injected for the planning run (spec: Skills layer —
// clex-plan enforces the dumb-issue contract; to-prd/to-issues shape output).
var planSkills = []string{"clex-plan", "to-prd", "to-issues"}

// lintSkills are the skills injected for the issue-lint run.
var lintSkills = []string{"clex-issue-lint"}

// lintPassToken is the sentinel a passing clex-issue-lint run emits. A run whose
// output does not contain it (case-insensitive) is treated as a lint failure and
// its text becomes the failure detail.
const lintPassToken = "LINT: PASS"

// PlanResult is the typed outcome of the plan stage.
type PlanResult struct {
	// EpicNumber is the created (or pre-existing) epic issue number.
	EpicNumber int
	// IssueNumbers are the created child issue numbers, in plan order.
	IssueNumbers []int
	// Questions are the batched plan-gate questions to render (#18).
	Questions []Question
	// LintFailures records any child that still fails lint after the single
	// bounce; these are surfaced to the human rather than silently accepted.
	LintFailures []LintFailure
	// Bounced reports whether the one automatic re-plan bounce was used.
	Bounced bool
}

// LintFailure is a child issue that failed clex-issue-lint, surfaced after the
// single allowed bounce.
type LintFailure struct {
	Issue  int
	Detail string
}

// PlanInputs carries the per-run material for Plan that is not on the idea issue
// itself.
type PlanInputs struct {
	// ImageRefs are images queued for the idea.
	ImageRefs []string
	// Knowledge is the repo knowledge-file context.
	Knowledge KnowledgeExcerpts
}

// Plan runs the planning stage for an idea issue: it assembles the planner
// prompt, runs a top-tier model, parses the PRD + child issues, creates the epic
// and children on GitHub, lints each child with a mid-tier model, bounces a
// lint-failing plan back to the planner EXACTLY ONCE, and returns the batched
// questions plus any residual lint failures.
//
// Idempotency: Plan is safe to re-run after a crash. If an epic already exists
// for this idea (an open issue carrying clex:epic that links this idea), the
// existing epic and its children are returned instead of creating duplicates.
// Detection is by title convention ("Epic: <idea title>") among issues the
// caller supplies via existingEpicNumber; when existingEpicNumber is non-zero it
// is trusted and no new epic/children are created.
func (p *Pipeline) Plan(ctx context.Context, ideaIssue *gh.Issue, in PlanInputs, existingEpicNumber int) (PlanResult, error) {
	// Idempotent short-circuit: a prior run already created the epic.
	if existingEpicNumber != 0 {
		return p.resumePlan(ctx, existingEpicNumber)
	}

	// 1. Resolve a top-tier planner model.
	planOpt, _, err := p.pickModel(core.RolePlan)
	if err != nil {
		return PlanResult{}, err
	}
	planRunner, err := p.deps.Runners.RunnerFor(planOpt.Model)
	if err != nil {
		return PlanResult{}, fmt.Errorf("plan: runner for %s: %w", planOpt.Model.ID, err)
	}

	// 2. Assemble + run the planner.
	prompt := buildPlanPrompt(PlanInput{
		Idea:      ideaIssue.Body,
		ImageRefs: in.ImageRefs,
		Knowledge: in.Knowledge,
		Skills:    planSkills,
	})
	plan, err := p.runPlanner(ctx, planRunner, planOpt, ideaIssue, prompt)
	if err != nil {
		return PlanResult{}, err
	}

	// 3. Create the epic + children on GitHub.
	res, err := p.createPlan(ctx, plan, ideaIssue.Number)
	if err != nil {
		return PlanResult{}, err
	}

	// 4. Lint each child; bounce the whole plan back to the planner ONCE if any
	//    child fails, then surface residual failures.
	failures, err := p.lintChildren(ctx, res.IssueNumbers)
	if err != nil {
		return res, err
	}
	if len(failures) > 0 {
		res.Bounced = true
		bounced, berr := p.bouncePlan(ctx, planRunner, planOpt, ideaIssue, prompt, plan, failures)
		if berr != nil {
			return res, berr
		}
		// Re-lint only the issues the bounce rewrote.
		res.LintFailures, err = p.lintChildren(ctx, res.IssueNumbers)
		if err != nil {
			return res, err
		}
		_ = bounced
	}
	res.Questions = plan.Questions
	return res, nil
}

// runPlanner runs the planner and parses its output.
func (p *Pipeline) runPlanner(ctx context.Context, r Runner, opt runOption, idea *gh.Issue, prompt string) (PlanOutput, error) {
	task := core.Task{
		Repo:   p.cfg.Repo.String(),
		Prompt: prompt,
		Issue:  idea.Number,
		Skills: planSkills,
		Effort: opt.Effort,
		Fast:   opt.Fast,
	}
	rr, err := runToCompletion(ctx, r, task, p.cfg.RepoDir)
	if err != nil {
		return PlanOutput{}, fmt.Errorf("plan: planner run: %w", err)
	}
	plan, err := parsePlanOutput(rr.Text)
	if err != nil {
		return PlanOutput{}, fmt.Errorf("plan: parse planner output: %w", err)
	}
	return plan, nil
}

// PlannedFromMarker returns the provenance line createPlan appends to the epic
// body. The daemon's crash recovery scans open epics for it to find the epic a
// half-finished plan already created, so a re-run resumes instead of
// duplicating (Plan's existingEpicNumber short-circuit).
func PlannedFromMarker(ideaNumber int) string {
	return fmt.Sprintf("_Planned from #%d by clex._", ideaNumber)
}

// createPlan creates the epic issue and each child, wiring dependency numbers
// and appending the metadata block. Children are created in plan order so their
// GitHub numbers are known before later children reference them.
//
// Children are labelled clex:planned — NOT approved: nothing builds until the
// owner passes the plan gate (/build <epic#> or clex build). Every child lists
// the epic among its DependsOn numbers: that link is how the daemon resolves a
// child's epic (integration branch) and enumerates an epic's children; the
// scheduler ignores it (epics are not dispatchable units, so an unknown dep is
// not a blocker).
func (p *Pipeline) createPlan(ctx context.Context, plan PlanOutput, ideaNumber int) (PlanResult, error) {
	var res PlanResult
	epicBody := plan.EpicBody
	if ideaNumber > 0 {
		epicBody = strings.TrimRight(epicBody, "\n") + "\n\n" + PlannedFromMarker(ideaNumber)
	}
	epicTitle := plan.EpicTitle
	if epicTitle == "" {
		epicTitle = "Epic"
	}
	epic, err := p.deps.GH.CreateIssue(ctx, p.cfg.Repo, epicTitle, epicBody, []string{string(core.StateEpic)})
	if err != nil {
		return res, fmt.Errorf("plan: create epic: %w", err)
	}
	res.EpicNumber = epic.Number

	// ordinalToNumber maps a 1-based plan ordinal to the created issue number.
	ordinalToNumber := make(map[int]int, len(plan.Issues))
	for i, ci := range plan.Issues {
		ordinal := i + 1
		deps := []int{epic.Number}
		for _, o := range ci.DependsOnOrdinals {
			if n, ok := ordinalToNumber[o]; ok {
				deps = append(deps, n)
			}
		}
		body := composeIssueBody(ci.Body, deps, ci)
		labels := []string{string(core.StatePlanned)}
		created, cerr := p.deps.GH.CreateIssue(ctx, p.cfg.Repo, ci.Title, body, labels)
		if cerr != nil {
			return res, fmt.Errorf("plan: create child %q: %w", ci.Title, cerr)
		}
		ordinalToNumber[ordinal] = created.Number
		res.IssueNumbers = append(res.IssueNumbers, created.Number)
	}
	return res, nil
}

// lintChildren runs clex-issue-lint (mid-tier) over each child issue and returns
// the failures. A lint run reads the issue body and scores it against the
// checklist; a run whose output lacks lintPassToken is a failure.
func (p *Pipeline) lintChildren(ctx context.Context, issues []int) ([]LintFailure, error) {
	lintOpt, _, err := p.pickModel(core.RoleLint)
	if err != nil {
		return nil, err
	}
	lintRunner, err := p.deps.Runners.RunnerFor(lintOpt.Model)
	if err != nil {
		return nil, fmt.Errorf("plan: runner for lint model %s: %w", lintOpt.Model.ID, err)
	}
	var failures []LintFailure
	for _, num := range issues {
		iss, gerr := p.deps.GH.GetIssue(ctx, p.cfg.Repo, num)
		if gerr != nil {
			return nil, fmt.Errorf("plan: get child #%d for lint: %w", num, gerr)
		}
		detail, ok, lerr := p.lintOne(ctx, lintRunner, lintOpt, iss)
		if lerr != nil {
			return nil, lerr
		}
		if !ok {
			failures = append(failures, LintFailure{Issue: num, Detail: detail})
		}
	}
	return failures, nil
}

// lintOne lints a single issue, returning the detail text and whether it passed.
func (p *Pipeline) lintOne(ctx context.Context, r Runner, opt runOption, iss *gh.Issue) (string, bool, error) {
	var prompt strings.Builder
	prompt.WriteString(BuilderPreamble)
	prompt.WriteString("\n\n")
	fmt.Fprintf(&prompt, "TASK: Lint issue #%d against the dumb-issue checklist. ", iss.Number)
	fmt.Fprintf(&prompt, "Emit %q on its own line if it passes; otherwise list what is missing.\n\n", lintPassToken)
	prompt.WriteString("## Issue\n")
	fmt.Fprintf(&prompt, "# %s\n\n", iss.Title)
	prompt.WriteString(strings.TrimSpace(iss.Body))
	prompt.WriteString("\n")

	task := core.Task{
		Repo:   p.cfg.Repo.String(),
		Prompt: prompt.String(),
		Issue:  iss.Number,
		Skills: lintSkills,
		Effort: opt.Effort,
		Fast:   opt.Fast,
	}
	rr, err := runToCompletion(ctx, r, task, p.cfg.RepoDir)
	if err != nil {
		return "", false, fmt.Errorf("plan: lint #%d: %w", iss.Number, err)
	}
	if strings.Contains(strings.ToUpper(rr.Text), lintPassToken) {
		return rr.Text, true, nil
	}
	return strings.TrimSpace(rr.Text), false, nil
}

// bouncePlan is the single automatic re-plan bounce: the planner is re-invoked
// with the original prompt plus the concrete lint failures, and its rewritten
// issue bodies replace the failing issues' bodies. Only one bounce ever happens
// per Plan call (enforced by the caller).
func (p *Pipeline) bouncePlan(ctx context.Context, r Runner, opt runOption, idea *gh.Issue, origPrompt string, prev PlanOutput, failures []LintFailure) (PlanOutput, error) {
	var b strings.Builder
	b.WriteString(origPrompt)
	b.WriteString("\n\n## Lint feedback — fix these issues and re-emit the FULL plan\n")
	for _, f := range failures {
		fmt.Fprintf(&b, "- #%d: %s\n", f.Issue, oneLine(f.Detail))
	}

	task := core.Task{
		Repo:     p.cfg.Repo.String(),
		Prompt:   b.String(),
		Issue:    idea.Number,
		Skills:   planSkills,
		Effort:   opt.Effort,
		Fast:     opt.Fast,
		ResumeID: "", // a bounce is a fresh planner turn scoped by the feedback
	}
	rr, err := runToCompletion(ctx, r, task, p.cfg.RepoDir)
	if err != nil {
		return PlanOutput{}, fmt.Errorf("plan: bounce run: %w", err)
	}
	revised, err := parsePlanOutput(rr.Text)
	if err != nil {
		// A bounce that produces no parseable plan leaves the originals in place;
		// surface via residual lint failures instead of failing hard.
		return prev, nil
	}

	// Apply revised bodies to the failing issues by matching plan order. The
	// bounce re-emits the full plan, so we update each already-created child's
	// body from the corresponding revised issue.
	if err := p.applyRevisedBodies(ctx, prev, revised, failures); err != nil {
		return revised, err
	}
	return revised, nil
}

// applyRevisedBodies updates the GitHub bodies of the failing issues from the
// revised plan. The bounce re-emits the full plan, so each failing issue is
// matched to its revised counterpart by title (a stable key that survives
// re-emission). The issue's already-encoded dependency numbers are preserved;
// only the body content and metadata block are refreshed. A revised plan that
// dropped or renamed a failing issue leaves that issue's original body in place.
func (p *Pipeline) applyRevisedBodies(ctx context.Context, prev, revised PlanOutput, failures []LintFailure) error {
	// Map revised issues by title for a stable lookup.
	revisedByTitle := make(map[string]ChildIssue, len(revised.Issues))
	for _, ci := range revised.Issues {
		revisedByTitle[strings.TrimSpace(ci.Title)] = ci
	}
	for _, f := range failures {
		iss, err := p.deps.GH.GetIssue(ctx, p.cfg.Repo, f.Issue)
		if err != nil {
			return fmt.Errorf("plan: get #%d to revise: %w", f.Issue, err)
		}
		ci, ok := revisedByTitle[strings.TrimSpace(iss.Title)]
		if !ok {
			continue // revision dropped/renamed it; leave the original in place
		}
		// Preserve the existing dependency numbers already encoded on the issue.
		body := composeIssueBody(ci.Body, iss.Meta.DependsOn, ci)
		if _, err := p.deps.GH.UpdateIssue(ctx, p.cfg.Repo, f.Issue, nil, strPtr(body)); err != nil {
			return fmt.Errorf("plan: update #%d body after bounce: %w", f.Issue, err)
		}
	}
	return nil
}

// resumePlan reconstructs a PlanResult for an already-created epic (crash
// recovery). It only confirms the epic exists and returns its number — no
// re-creation, no re-lint, no child enumeration (children keep whatever state
// the crashed run left; the plan-gate summary for a resumed plan names just
// the epic and the owner reviews it on GitHub).
func (p *Pipeline) resumePlan(ctx context.Context, epicNumber int) (PlanResult, error) {
	epic, err := p.deps.GH.GetIssue(ctx, p.cfg.Repo, epicNumber)
	if err != nil {
		return PlanResult{}, fmt.Errorf("plan: resume get epic #%d: %w", epicNumber, err)
	}
	res := PlanResult{EpicNumber: epic.Number}
	return res, nil
}

// oneLine collapses whitespace/newlines to a single line for compact logging.
func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// strPtr returns a pointer to s.
func strPtr(s string) *string { return &s }
