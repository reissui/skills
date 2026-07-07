package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// writeGlobalConfig writes a valid runnable config to the env's home so the
// role-resolution checks pass; doctor tests that don't care about config use it
// to isolate the check under test.
func writeGlobalConfig(t *testing.T, e *env) {
	t.Helper()
	if err := writeConfigScaffold(e.globalConfigPath(), "tok", 42, nil); err != nil {
		t.Fatalf("seed config: %v", err)
	}
}

func TestDoctorAllHealthy(t *testing.T) {
	e := newTestEnv(t)
	writeGlobalConfig(t, e)
	// All deps healthy (default), fine-grained token (no scopes), protected branch.
	fgh := e.newGH.mustFake(t)
	fgh.scopes = nil
	fgh.protected = true

	code := run(e, []string{"doctor"})
	if code != exitOK {
		t.Fatalf("doctor exit = %d, want 0\nstdout:\n%s", code, outBuf(e))
	}
	out := outBuf(e).String()
	if !strings.Contains(out, "all checks passed") {
		t.Fatalf("expected healthy summary; got:\n%s", out)
	}
}

func TestDoctorBrokenAuthExits2(t *testing.T) {
	e := newTestEnv(t)
	writeGlobalConfig(t, e)
	// gh present but not authenticated → problem.
	e.probe = newFakeProbe(map[string]depResult{
		"claude": {Found: true, Authed: true, Version: "claude 1.2.3"},
		"codex":  {Found: true, Authed: true, Version: "codex 0.9.0"},
		"gh":     {Found: true, Authed: false, Detail: "not logged in"},
		"ollama": {Found: true, Authed: true},
	})

	code := run(e, []string{"doctor"})
	if code != exitProblem {
		t.Fatalf("doctor exit = %d, want 2 (broken auth)\nstdout:\n%s", code, outBuf(e))
	}
	out := outBuf(e).String()
	if !strings.Contains(out, "fix:") || !strings.Contains(out, "gh auth login") {
		t.Fatalf("expected an actionable gh fix line; got:\n%s", out)
	}
}

func TestDoctorMissingRequiredBinaryExits2(t *testing.T) {
	e := newTestEnv(t)
	writeGlobalConfig(t, e)
	e.probe = newFakeProbe(map[string]depResult{
		// claude missing entirely.
		"codex":  {Found: true, Authed: true},
		"gh":     {Found: true, Authed: true},
		"ollama": {Found: true, Authed: true},
	})
	code := run(e, []string{"doctor"})
	if code != exitProblem {
		t.Fatalf("doctor exit = %d, want 2 (missing claude)", code)
	}
	if !strings.Contains(outBuf(e).String(), "claude") {
		t.Fatalf("expected claude ✗ line")
	}
}

func TestDoctorOverScopedTokenWarns(t *testing.T) {
	e := newTestEnv(t)
	writeGlobalConfig(t, e)
	// A user-supplied env PAT is where the fine-grained-PAT advice is actionable,
	// so the over-scope warning applies only when GITHUB_TOKEN/GH_TOKEN is set.
	e.getenv = func(k string) string {
		if k == "GITHUB_TOKEN" {
			return "ghp_classicpat"
		}
		return ""
	}
	fgh := e.newGH.mustFake(t)
	fgh.scopes = []string{"repo", "workflow"} // classic full-repo token
	fgh.protected = true

	code := run(e, []string{"doctor"})
	// A warning does not change the exit code from 0.
	if code != exitOK {
		t.Fatalf("doctor exit = %d, want 0 (warn only)\n%s", code, outBuf(e))
	}
	out := outBuf(e).String()
	if !strings.Contains(out, "over-scoped") || !strings.Contains(out, "fine-grained") {
		t.Fatalf("expected over-scoped token warning with fix; got:\n%s", out)
	}
}

// TestDoctorGHManagedTokenPassesClean is the issue #40 fix-2 case: an
// authenticated gh CLI is the supported happy path. With no GITHUB_TOKEN/GH_TOKEN
// set, even a token that reports classic `repo` scope must pass clean (gh's oauth
// scopes aren't user-narrowable), not warn.
func TestDoctorGHManagedTokenPassesClean(t *testing.T) {
	e := newTestEnv(t)
	writeGlobalConfig(t, e)
	// Default env has no GITHUB_TOKEN/GH_TOKEN → gh-managed.
	fgh := e.newGH.mustFake(t)
	fgh.scopes = []string{"admin:public_key", "gist", "read:org", "repo"} // gh's default set
	fgh.protected = true

	code := run(e, []string{"doctor"})
	if code != exitOK {
		t.Fatalf("doctor exit = %d, want 0\n%s", code, outBuf(e))
	}
	out := outBuf(e).String()
	if strings.Contains(out, "over-scoped") {
		t.Fatalf("gh-managed token should not trip the over-scope warning; got:\n%s", out)
	}
	if !strings.Contains(out, "managed by gh") {
		t.Fatalf("expected 'managed by gh' clean line; got:\n%s", out)
	}
}

// TestDoctorGHOAuthPrefixPassesClean: a gho_-prefixed token is gh-managed even if
// an env var happens to be set (the prefix is the strong positive signal).
func TestDoctorGHOAuthPrefixPassesClean(t *testing.T) {
	e := newTestEnv(t)
	writeGlobalConfig(t, e)
	e.ghToken = func(context.Context) (string, error) { return "gho_oauthtoken", nil }
	e.getenv = func(string) string { return "set" } // even with env set…
	fgh := e.newGH.mustFake(t)
	fgh.scopes = []string{"repo"}
	fgh.protected = true

	code := run(e, []string{"doctor"})
	if code != exitOK {
		t.Fatalf("doctor exit = %d, want 0\n%s", code, outBuf(e))
	}
	if !strings.Contains(outBuf(e).String(), "managed by gh") {
		t.Fatalf("expected gho_ token treated as gh-managed; got:\n%s", outBuf(e))
	}
}

// TestDoctorMissingBranchProtectionIsInfo: branch protection is advisory only
// (issue #40 fix 3). An unprotected branch is reported as a neutral info line
// (glyph ·), keeps exit 0, and the fix text is a suggestion, not a requirement.
func TestDoctorMissingBranchProtectionIsInfo(t *testing.T) {
	e := newTestEnv(t)
	writeGlobalConfig(t, e)
	fgh := e.newGH.mustFake(t)
	fgh.scopes = nil
	fgh.protected = false // main not protected

	code := run(e, []string{"doctor"})
	if code != exitOK {
		t.Fatalf("doctor exit = %d, want 0 (info only)", code)
	}
	out := outBuf(e).String()
	if !strings.Contains(out, "not protected") {
		t.Fatalf("expected branch-protection info line; got:\n%s", out)
	}
	// Rendered as info (·), never as a warning (!).
	if !strings.Contains(out, "· branch-protection") {
		t.Fatalf("expected branch-protection rendered as info (·); got:\n%s", out)
	}
	if !strings.Contains(out, "consider protecting") {
		t.Fatalf("expected a suggestion-worded fix, not a requirement; got:\n%s", out)
	}
	// The healthy summary must not be inflated to a warning by an info line.
	if !strings.Contains(out, "all checks passed") {
		t.Fatalf("info-only run should still summarize as all-passed; got:\n%s", out)
	}
}

// TestDoctorInfoJSONStatus: the info status is surfaced verbatim in JSON so
// machine consumers can distinguish advisory findings from warnings.
func TestDoctorInfoJSONStatus(t *testing.T) {
	e := newTestEnv(t)
	writeGlobalConfig(t, e)
	fgh := e.newGH.mustFake(t)
	fgh.protected = false

	if code := run(e, []string{"doctor", "--json"}); code != exitOK {
		t.Fatalf("doctor --json exit = %d, want 0", code)
	}
	var report doctorReport
	if err := json.Unmarshal(outBuf(e).Bytes(), &report); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	var found bool
	for _, c := range report.Checks {
		if c.Name == "branch-protection" {
			found = true
			if c.Status != statusInfo {
				t.Fatalf("branch-protection status = %q, want %q", c.Status, statusInfo)
			}
		}
	}
	if !found {
		t.Fatal("expected a branch-protection check in the report")
	}
	if !report.OK {
		t.Fatalf("info-only report should be OK; got %+v", report)
	}
}

func TestDoctorJSONIsValid(t *testing.T) {
	e := newTestEnv(t)
	writeGlobalConfig(t, e)
	fgh := e.newGH.mustFake(t)
	fgh.protected = true

	code := run(e, []string{"doctor", "--json"})
	if code != exitOK {
		t.Fatalf("doctor --json exit = %d, want 0", code)
	}
	var report doctorReport
	if err := json.Unmarshal(outBuf(e).Bytes(), &report); err != nil {
		t.Fatalf("doctor --json emitted invalid JSON: %v\n%s", err, outBuf(e))
	}
	if len(report.Checks) == 0 {
		t.Fatalf("expected checks in JSON report")
	}
	if !report.OK {
		t.Fatalf("expected OK=true in healthy JSON report; got %+v", report)
	}
}

func TestDoctorRoleResolutionProblem(t *testing.T) {
	e := newTestEnv(t)
	// No config written → role resolution has no models, but the config-missing
	// path is a warning; write a config with a role pointing nowhere to force a
	// role problem instead.
	badTOML := `
telegram_token = "x"
[providers.claude]
kind = "claude-cli"
[models.opus]
provider = "claude"
billing = "subscription"
[tiers]
default = ["opus"]
[routing.plan]
tier = "default"
[routing.build]
tier = "nonexistent"
[routing.review]
tier = "default"
[routing.lint]
tier = "default"
[routing.bot]
tier = "default"
`
	if err := writeFile(e.globalConfigPath(), badTOML); err != nil {
		t.Fatal(err)
	}
	fgh := e.newGH.mustFake(t)
	fgh.protected = true

	code := run(e, []string{"doctor"})
	if code != exitProblem {
		t.Fatalf("doctor exit = %d, want 2 (role build resolves to nothing)\n%s", code, outBuf(e))
	}
	if !strings.Contains(outBuf(e).String(), "role:build") {
		t.Fatalf("expected a role:build problem line; got:\n%s", outBuf(e))
	}
}
