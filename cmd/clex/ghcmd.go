package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/reissui/clex/internal/core"
	"github.com/reissui/clex/internal/gh"
)

// resolveRepo determines the target repository for a gh-backed command: an
// explicit --repo flag wins; otherwise the repo recorded in the global config is
// used. It returns a friendly error when neither is available.
func (e *env) resolveRepo(flagRepo string) (gh.Repo, error) {
	if s := strings.TrimSpace(flagRepo); s != "" {
		return gh.ParseRepo(s)
	}
	if r, ok := e.configuredRepo(); ok {
		return gh.ParseRepo(r)
	}
	return gh.Repo{}, fmt.Errorf("no repository given: pass --repo owner/name (or run 'clex init' to register one)")
}

// ghClientFor builds a gh client from the ambient token. The token comes from
// the injected resolver (real: `gh auth token`); a missing token is a clear
// error pointing at `gh auth login`.
func (e *env) ghClientFor(ctx context.Context) (ghClient, error) {
	token, err := e.ghToken(ctx)
	if err != nil {
		return nil, fmt.Errorf("no GitHub token: run 'gh auth login' (%v)", err)
	}
	return e.newGH(token)
}

// cmdIdea files a feature idea as a labelled GitHub issue. Per the design it does
// NOT require the daemon: the issue is created with the clex:idea label and the
// running daemon's poller picks it up (spec: idea files an idea without
// Telegram). Direct gh op, --repo optional.
func cmdIdea(e *env, args []string) int {
	fs, jsonOut := newFlagSet(e, "idea", "file a feature idea as a labelled GitHub issue")
	repoFlag := fs.String("repo", "", "target repository as owner/name (defaults to the configured repo)")
	titleFlag := fs.String("title", "", "issue title (defaults to the first line of the idea text)")
	if code, ok := parseFlags(fs, args); !ok {
		return code
	}
	body := strings.TrimSpace(strings.Join(fs.Args(), " "))
	if body == "" {
		return fail(e, *jsonOut, `usage: clex idea "what you want built" [--repo owner/name]`)
	}
	repo, err := e.resolveRepo(*repoFlag)
	if err != nil {
		return fail(e, *jsonOut, "%v", err)
	}
	title := strings.TrimSpace(*titleFlag)
	if title == "" {
		title = firstLine(body)
	}

	ctx, cancel := e.cmdContext()
	defer cancel()
	client, err := e.ghClientFor(ctx)
	if err != nil {
		return fail(e, *jsonOut, "%v", err)
	}
	issue, err := client.CreateIssue(ctx, repo, title, body, []string{string(core.StateIdea)})
	if err != nil {
		return fail(e, *jsonOut, "create idea issue: %v", err)
	}
	if *jsonOut {
		return writeJSON(e.stdout, map[string]any{
			"ok": true, "repo": repo.String(), "issue": issue.Number, "title": issue.Title,
		})
	}
	fmt.Fprintf(e.stdout, "filed idea #%d in %s: %s\n", issue.Number, repo, issue.Title)
	fmt.Fprintf(e.stdout, "the daemon will research and plan it; watch with 'clex status'.\n")
	return exitOK
}

// cmdPlan advances an issue toward the plan gate by labelling it clex:idea so the
// daemon's poller researches and plans it. It is a direct gh label op (no daemon
// required) so it works whether or not clexd is up — a re-plan simply re-sets the
// label. See PR notes: plan/build are implemented as label ops because adding
// daemon control kinds would be a non-additive daemon change (out of scope).
func cmdPlan(e *env, args []string) int {
	fs, jsonOut := newFlagSet(e, "plan", "plan an issue (research → plan gate)")
	repoFlag := fs.String("repo", "", "target repository as owner/name")
	if code, ok := parseFlags(fs, args); !ok {
		return code
	}
	return e.setIssueState(fs.Args(), *repoFlag, *jsonOut, core.StateIdea,
		"queued #%d for planning in %s; the daemon will research and open the plan gate.")
}

// cmdBuild passes the plan gate. For a child issue it swaps the label to
// clex:approved, making it dispatchable by the scheduler. For an epic it
// approves every clex:planned child (same behavior as Telegram's /build).
// Direct gh ops (no daemon required); the running daemon picks approved issues
// up on its next reconcile.
func cmdBuild(e *env, args []string) int {
	fs, jsonOut := newFlagSet(e, "build", "approve an issue or epic for building")
	repoFlag := fs.String("repo", "", "target repository as owner/name")
	if code, ok := parseFlags(fs, args); !ok {
		return code
	}
	rest := fs.Args()
	if len(rest) < 1 {
		return fail(e, *jsonOut, "usage: clex build <issue|epic> [--repo owner/name]")
	}
	number, perr := parseIssueTarget(rest[0])
	if perr != nil || number <= 0 {
		return fail(e, *jsonOut, "an issue number is required")
	}
	repo, err := e.resolveRepo(*repoFlag)
	if err != nil {
		return fail(e, *jsonOut, "%v", err)
	}
	ctx, cancel := e.cmdContext()
	defer cancel()
	client, err := e.ghClientFor(ctx)
	if err != nil {
		return fail(e, *jsonOut, "%v", err)
	}
	iss, err := client.GetIssue(ctx, repo, number)
	if err != nil {
		return fail(e, *jsonOut, "get #%d: %v", number, err)
	}
	if !iss.IsEpic {
		if err := client.SetState(ctx, repo, number, core.StateApproved); err != nil {
			return fail(e, *jsonOut, "set #%d to %s: %v", number, core.StateApproved, err)
		}
		if *jsonOut {
			return writeJSON(e.stdout, map[string]any{
				"ok": true, "repo": repo.String(), "issue": number, "state": string(core.StateApproved),
			})
		}
		fmt.Fprintf(e.stdout, "approved #%d for building in %s; the daemon will dispatch it.\n", number, repo)
		return exitOK
	}

	open, err := client.ListOpenIssues(ctx, repo)
	if err != nil {
		return fail(e, *jsonOut, "list issues: %v", err)
	}
	var approved []int
	for _, child := range open {
		if child.IsEpic || child.State != core.StatePlanned || !dependsOnNumber(child, number) {
			continue
		}
		if err := client.SetState(ctx, repo, child.Number, core.StateApproved); err != nil {
			return fail(e, *jsonOut, "approve child #%d: %v", child.Number, err)
		}
		approved = append(approved, child.Number)
	}
	if len(approved) == 0 {
		return fail(e, *jsonOut, "epic #%d has no planned children to approve", number)
	}
	if *jsonOut {
		return writeJSON(e.stdout, map[string]any{
			"ok": true, "repo": repo.String(), "epic": number, "approved": approved,
		})
	}
	fmt.Fprintf(e.stdout, "approved %d issues of epic #%d in %s; the daemon will build them in dependency order.\n", len(approved), number, repo)
	return exitOK
}

// dependsOnNumber reports whether iss lists n among its DependsOn numbers (the
// child→epic link the planner writes).
func dependsOnNumber(iss *gh.Issue, n int) bool {
	for _, dep := range iss.Meta.DependsOn {
		if dep == n {
			return true
		}
	}
	return false
}

// setIssueState is the shared body of plan/build: parse the issue target, resolve
// the repo and gh client, and set the issue's pipeline label. successFmt takes
// (issueNumber, repo).
func (e *env) setIssueState(rest []string, repoFlag string, jsonMode bool, to core.State, successFmt string) int {
	if len(rest) < 1 {
		return fail(e, jsonMode, "usage: clex %s <issue> [--repo owner/name]", strings.TrimPrefix(string(to), "clex:"))
	}
	issue, perr := parseIssueTarget(rest[0])
	if perr != nil {
		return fail(e, jsonMode, "%v", perr)
	}
	if issue <= 0 {
		return fail(e, jsonMode, "an issue number is required")
	}
	repo, err := e.resolveRepo(repoFlag)
	if err != nil {
		return fail(e, jsonMode, "%v", err)
	}
	ctx, cancel := e.cmdContext()
	defer cancel()
	client, err := e.ghClientFor(ctx)
	if err != nil {
		return fail(e, jsonMode, "%v", err)
	}
	if err := client.SetState(ctx, repo, issue, to); err != nil {
		return fail(e, jsonMode, "set #%d to %s: %v", issue, to, err)
	}
	if jsonMode {
		return writeJSON(e.stdout, map[string]any{
			"ok": true, "repo": repo.String(), "issue": issue, "state": string(to),
		})
	}
	fmt.Fprintf(e.stdout, successFmt+"\n", issue, repo)
	return exitOK
}
