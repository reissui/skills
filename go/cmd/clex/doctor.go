package main

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/reissui/clex/internal/config"
	"github.com/reissui/clex/internal/core"
	"github.com/reissui/clex/internal/gh"
)

// checkStatus is one diagnostic outcome level. ok, info, and warn all keep the
// overall exit at 0; problem forces exit 2 (spec: doctor exit codes). info is for
// purely advisory findings that carry no obligation — a neutral FYI, weaker than
// a warning (issue #40: branch protection is not a requirement for clex repos).
type checkStatus string

const (
	statusOK      checkStatus = "ok"
	statusInfo    checkStatus = "info"
	statusWarn    checkStatus = "warn"
	statusProblem checkStatus = "problem"
)

// mark returns the glyph for a status in human output.
func (s checkStatus) mark() string {
	switch s {
	case statusOK:
		return "✓"
	case statusInfo:
		return "·"
	case statusWarn:
		return "!"
	default:
		return "✗"
	}
}

// checkResult is a single doctor line: what was checked, how it came out, a
// human message, and (when not ok) the exact fix command to run.
type checkResult struct {
	Name    string      `json:"name"`
	Status  checkStatus `json:"status"`
	Message string      `json:"message"`
	Fix     string      `json:"fix,omitempty"`
}

// doctorReport is the full aggregated result, JSON-serializable.
type doctorReport struct {
	OK      bool          `json:"ok"`
	Checks  []checkResult `json:"checks"`
	Summary string        `json:"summary"`
}

// worstStatus folds the checks into an exit code: any problem → exit 2, else 0.
func (r doctorReport) exitCode() int {
	for _, c := range r.Checks {
		if c.Status == statusProblem {
			return exitProblem
		}
	}
	return exitOK
}

// cmdDoctor aggregates dependency, auth, GitHub-token, and role-resolution checks
// into a report. All-healthy → exit 0; any problem → exit 2 with actionable fix
// lines. Every outside call goes through the injected probe/newGH so tests drive
// every branch without live tools (issue #17 acceptance criteria).
func cmdDoctor(e *env, args []string) int {
	fs, jsonOut := newFlagSet(e, "doctor", "check binaries, auth, tokens, and role resolution")
	repoFlag := fs.String("repo", "", "repository to check branch protection on (defaults to the git origin)")
	if code, ok := parseFlags(fs, args); !ok {
		return code
	}
	ctx, cancel := e.cmdContext()
	defer cancel()

	report := e.runDoctor(ctx, *repoFlag)
	if *jsonOut {
		writeJSON(e.stdout, report)
		return report.exitCode()
	}
	for _, c := range report.Checks {
		fmt.Fprintf(e.stdout, "%s %-22s %s\n", c.Status.mark(), c.Name, c.Message)
		if c.Fix != "" {
			fmt.Fprintf(e.stdout, "    fix: %s\n", c.Fix)
		}
	}
	fmt.Fprintf(e.stdout, "\n%s\n", report.Summary)
	return report.exitCode()
}

// requiredBinaries lists the external tools doctor probes and the fix command to
// print when one is absent (spec: doctor checks claude/codex/ollama/gh).
var requiredBinaries = []struct {
	name string
	fix  string
}{
	{"claude", "install the Claude CLI: https://docs.anthropic.com/claude/cli (then `claude login`)"},
	{"codex", "install the Codex CLI (then authenticate per its docs)"},
	{"gh", "install GitHub CLI: https://cli.github.com then run `gh auth login`"},
	{"ollama", "install Ollama for local models: https://ollama.com (optional)"},
}

// runDoctor performs every check and assembles the report.
func (e *env) runDoctor(ctx context.Context, repoFlag string) doctorReport {
	var checks []checkResult

	// 1. Binaries + auth.
	for _, b := range requiredBinaries {
		checks = append(checks, e.checkBinary(ctx, b.name, b.fix))
	}

	// 2. GitHub token quality: over-scoped classic token + branch protection.
	checks = append(checks, e.checkGitHub(ctx, repoFlag)...)

	// 3. Config role resolution: each role resolves to >=1 model.
	checks = append(checks, e.checkRoles()...)

	problems, warns := 0, 0
	for _, c := range checks {
		switch c.Status {
		case statusProblem:
			problems++
		case statusWarn:
			warns++
		}
	}
	summary := "all checks passed"
	switch {
	case problems > 0:
		summary = fmt.Sprintf("%d problem(s), %d warning(s) — fix the ✗ lines above", problems, warns)
	case warns > 0:
		summary = fmt.Sprintf("healthy, with %d warning(s)", warns)
	}
	return doctorReport{OK: problems == 0, Checks: checks, Summary: summary}
}

// checkBinary probes one tool. Absent → problem (with fix); present-but-unauthed
// → problem for gh (fix: gh auth login), else ok. ollama absence is a warning,
// not a problem, since local models are optional.
func (e *env) checkBinary(ctx context.Context, name, fix string) checkResult {
	r := e.probe.Probe(ctx, name)
	if !r.Found {
		st := statusProblem
		if name == "ollama" {
			st = statusWarn
		}
		return checkResult{Name: name, Status: st, Message: "not found on PATH", Fix: fix}
	}
	if !r.Authed {
		msg := "found but not authenticated"
		if r.Detail != "" {
			msg = "found: " + r.Detail
		}
		return checkResult{Name: name, Status: statusProblem, Message: msg, Fix: fix}
	}
	msg := "found"
	if r.Version != "" {
		msg = r.Version
	}
	return checkResult{Name: name, Status: statusOK, Message: msg}
}

// checkGitHub inspects the token's scopes and the target repo's branch
// protection. It warns (does not fail) on a classic full-`repo` token and on
// missing protection of the head branch (spec: doctor warns on over-scoped
// tokens and recommends branch protection on main). When no repo or token is
// available these degrade to informational skips, never hard failures.
func (e *env) checkGitHub(ctx context.Context, repoFlag string) []checkResult {
	token, err := e.ghToken(ctx)
	if err != nil {
		return []checkResult{{
			Name: "github-token", Status: statusWarn,
			Message: "no token available; skipping scope/protection checks",
			Fix:     "run `gh auth login` so doctor can inspect the token",
		}}
	}
	client, err := e.newGH(token)
	if err != nil {
		return []checkResult{{
			Name: "github-token", Status: statusProblem,
			Message: fmt.Sprintf("cannot build GitHub client: %v", err),
			Fix:     "check `gh auth status`",
		}}
	}

	var out []checkResult
	// Scope check. A gh-CLI-managed token is the supported happy path: gh's oauth
	// scopes are not user-narrowable and the CLI itself authenticates via `gh auth
	// token`, so warning about them is unactionable and contradicts our own auth
	// strategy (issue #40). Only a user-supplied GITHUB_TOKEN/GH_TOKEN classic PAT
	// gets the over-scope warning, where a fine-grained PAT is genuinely actionable.
	if e.githubTokenIsGHManaged(token) {
		out = append(out, checkResult{
			Name: "github-token", Status: statusOK,
			Message: "using gh CLI auth (managed by gh)",
		})
	} else {
		scopes, serr := client.TokenScopes(ctx)
		switch {
		case serr != nil:
			out = append(out, checkResult{
				Name: "github-token", Status: statusWarn,
				Message: fmt.Sprintf("could not read token scopes: %v", serr),
			})
		case containsFold(scopes, "repo"):
			out = append(out, checkResult{
				Name: "github-token", Status: statusWarn,
				Message: fmt.Sprintf("over-scoped classic token (scopes: %s)", strings.Join(scopes, ", ")),
				Fix:     "GITHUB_TOKEN/GH_TOKEN is a classic full-`repo` PAT; use a fine-grained PAT scoped to the managed repo(s), or unset it to use `gh auth login`",
			})
		default:
			msg := "fine-grained (no classic `repo` scope)"
			if len(scopes) > 0 {
				msg = "scopes: " + strings.Join(scopes, ", ")
			}
			out = append(out, checkResult{Name: "github-token", Status: statusOK, Message: msg})
		}
	}

	// Branch-protection check on the head branch. This is purely informational:
	// nothing in merge/push/pipeline logic depends on protection being enabled, and
	// protecting main is a suggestion, not a clex requirement (issue #40). All
	// outcomes are statusInfo so they never read as an obligation and never affect
	// the exit code.
	repoStr := strings.TrimSpace(repoFlag)
	if repoStr == "" {
		if r, ok := e.configuredRepo(); ok {
			repoStr = r
		}
	}
	if repoStr == "" {
		out = append(out, checkResult{
			Name: "branch-protection", Status: statusInfo,
			Message: "no repository to check; pass --repo owner/name",
		})
		return out
	}
	repo, perr := gh.ParseRepo(repoStr)
	if perr != nil {
		out = append(out, checkResult{Name: "branch-protection", Status: statusInfo, Message: perr.Error()})
		return out
	}
	branch := e.headBranch()
	protected, berr := client.BranchProtected(ctx, repo, branch)
	switch {
	case berr != nil:
		out = append(out, checkResult{
			Name: "branch-protection", Status: statusInfo,
			Message: fmt.Sprintf("could not read protection for %s@%s: %v", repo, branch, berr),
		})
	case !protected:
		out = append(out, checkResult{
			Name: "branch-protection", Status: statusInfo,
			Message: fmt.Sprintf("%s branch %q is not protected", repo, branch),
			Fix:     fmt.Sprintf("consider protecting %q — optional; clex already never pushes the default branch directly", branch),
		})
	default:
		out = append(out, checkResult{
			Name: "branch-protection", Status: statusInfo,
			Message: fmt.Sprintf("%s@%s is protected", repo, branch),
		})
	}
	return out
}

// githubTokenIsGHManaged reports whether the resolved GitHub token is managed by
// the gh CLI rather than supplied by the user via GITHUB_TOKEN/GH_TOKEN. A
// gh-managed token is either an oauth token (gho_ prefix, what `gh auth login`
// mints) or any token resolved when no GITHUB_TOKEN/GH_TOKEN is set in the
// environment (so the source was `gh auth token`'s own stored credentials).
// User-supplied env tokens are not gh-managed — they can be narrowed to a
// fine-grained PAT, so the over-scope warning stays actionable for them.
func (e *env) githubTokenIsGHManaged(token string) bool {
	if strings.HasPrefix(token, "gho_") {
		return true
	}
	getenv := e.getenv
	if getenv == nil {
		getenv = os.Getenv
	}
	return getenv("GITHUB_TOKEN") == "" && getenv("GH_TOKEN") == ""
}

// headBranch is the target branch doctor checks for protection: the configured
// head branch, defaulting to "main".
func (e *env) headBranch() string {
	cfg, _, err := config.LoadGlobal(e.globalConfigPath())
	if err == nil && cfg != nil && strings.TrimSpace(cfg.HeadBranch) != "" {
		return cfg.HeadBranch
	}
	return "main"
}

// checkRoles validates that every routing role resolves to at least one model
// (spec: doctor validates each role resolves to >=1 healthy model). A role that
// resolves to nothing is a problem; a missing config is a warning (init writes
// one). It reads only config resolution, not live model health — the daemon's
// `models` command reports runtime health.
func (e *env) checkRoles() []checkResult {
	cfg, warns, err := config.LoadGlobal(e.globalConfigPath())
	if err != nil {
		return []checkResult{{
			Name: "config", Status: statusWarn,
			Message: fmt.Sprintf("no global config: %v", err),
			Fix:     "run `clex init` to create ~/.clex/config.toml",
		}}
	}
	// Surface config load warnings (dropped models, dangling tiers) as warn lines.
	var out []checkResult
	for _, w := range warns {
		out = append(out, checkResult{Name: "config", Status: statusWarn, Message: w.String()})
	}
	roles := []core.Role{core.RolePlan, core.RoleBuild, core.RoleReview, core.RoleLint, core.RoleBot}
	names := make([]string, 0, len(roles))
	nameOf := map[core.Role]string{
		core.RolePlan: "plan", core.RoleBuild: "build", core.RoleReview: "review",
		core.RoleLint: "lint", core.RoleBot: "bot",
	}
	for _, role := range roles {
		names = append(names, nameOf[role])
	}
	sort.Strings(names)
	for _, role := range roles {
		models := cfg.ModelsForRole(role)
		if len(models) == 0 {
			out = append(out, checkResult{
				Name: "role:" + nameOf[role], Status: statusProblem,
				Message: "resolves to no models",
				Fix:     fmt.Sprintf("point [routing.%s] at a tier/model in ~/.clex/config.toml", nameOf[role]),
			})
			continue
		}
		out = append(out, checkResult{
			Name: "role:" + nameOf[role], Status: statusOK,
			Message: fmt.Sprintf("%d model(s): %s", len(models), modelIDs(models)),
		})
	}
	return out
}

// modelIDs renders a comma-joined list of model ids for a role line.
func modelIDs(models []core.Model) string {
	ids := make([]string, 0, len(models))
	for _, m := range models {
		ids = append(ids, m.ID)
	}
	return strings.Join(ids, ", ")
}

// containsFold reports whether xs contains s, case-insensitively.
func containsFold(xs []string, s string) bool {
	for _, x := range xs {
		if strings.EqualFold(strings.TrimSpace(x), s) {
			return true
		}
	}
	return false
}
