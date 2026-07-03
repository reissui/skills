package main

import (
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

func TestDoctorMissingBranchProtectionWarns(t *testing.T) {
	e := newTestEnv(t)
	writeGlobalConfig(t, e)
	fgh := e.newGH.mustFake(t)
	fgh.scopes = nil
	fgh.protected = false // main not protected

	code := run(e, []string{"doctor"})
	if code != exitOK {
		t.Fatalf("doctor exit = %d, want 0 (warn only)", code)
	}
	out := outBuf(e).String()
	if !strings.Contains(out, "not protected") || !strings.Contains(out, "branch protection") {
		t.Fatalf("expected branch-protection warning; got:\n%s", out)
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
