//go:build e2e

package e2e

import (
	"fmt"
	"strings"

	"github.com/reissui/clex/internal/core"
)

// scriptForTask maps a pipeline task to the fake-runner script that makes the
// stage progress. The stage is inferred from the task's Skills and prompt:
//
//   - plan:   Skills include "to-prd"/"to-issues"  → emit a CLEX-PLAN block.
//   - lint:   prompt asks to emit "LINT: PASS"      → emit LINT: PASS.
//   - build:  Skills == ["clex-plan"] (a build)     → write a file + commit,
//     then emit build text; verification is the issue's "true" command.
//   - review: no skills, prompt is a review          → emit "REVIEW: APPROVE".
//   - steer:  prompt starts with "Steering update"   → benign result.
//
// This keeps one fake binary behaving as every role, selected purely from what
// the real pipeline passes it (the pipeline is unmodified).
func scriptForTask(task core.Task) fakeScript {
	skills := strings.Join(task.Skills, ",")
	prompt := task.Prompt

	switch {
	case containsAny(skills, "to-prd", "to-issues"):
		return planScript()
	case strings.Contains(prompt, lintPassSentinel):
		return lintScript()
	case strings.HasPrefix(strings.TrimSpace(prompt), "Steering update"):
		return simpleResult("steer acknowledged")
	case strings.Contains(prompt, "Review the pull request"):
		return reviewScript()
	case strings.Contains(prompt, "Implement issue"):
		return buildScript(task.Issue)
	default:
		// Any other runner turn (e.g. a bounce) gets a benign terminal result so
		// the stream always completes.
		return simpleResult("clex-fake-runner: unrecognized task; completing.")
	}
}

// lintPassSentinel is the token the pipeline's lint prompt instructs the model
// to emit; its presence in a prompt identifies a lint run. It matches
// pipeline.lintPassToken.
const lintPassSentinel = "LINT: PASS"

// planScript emits a deterministic CLEX-PLAN v1 block describing an epic and
// three child issues, the third depending on the second (a dependency edge the
// scheduler must serialize). Two independent children (1 and 2) touch disjoint
// paths so they may build in parallel; child 3 depends on child 2.
func planScript() fakeScript {
	var b strings.Builder
	b.WriteString("Here is the plan.\n")
	b.WriteString("===CLEX-PLAN v1===\n")
	b.WriteString("===EPIC===\n")
	b.WriteString("TITLE: Epic: add greeting service\n")
	b.WriteString("Build a small greeting service in three slices.\n")
	// Child 1 — independent.
	b.WriteString("===ISSUE===\n")
	b.WriteString("TITLE: add greeting core\n")
	b.WriteString("DIFFICULTY: standard\n")
	b.WriteString("TOUCHES: pkg/core/**\n")
	b.WriteString("VERIFY: true\n")
	b.WriteString("BODY:\n")
	b.WriteString("Implement the greeting core.\n")
	b.WriteString("Acceptance: it greets.\n")
	// Child 2 — independent of child 1 (disjoint touches) → parallel with 1.
	b.WriteString("===ISSUE===\n")
	b.WriteString("TITLE: add greeting api\n")
	b.WriteString("DIFFICULTY: standard\n")
	b.WriteString("TOUCHES: pkg/api/**\n")
	b.WriteString("VERIFY: true\n")
	b.WriteString("BODY:\n")
	b.WriteString("Implement the greeting api.\n")
	b.WriteString("Acceptance: it serves.\n")
	// Child 3 — depends on child 2 (ordinal 2) → built only after #2 lands.
	b.WriteString("===ISSUE===\n")
	b.WriteString("TITLE: add greeting cli\n")
	b.WriteString("DIFFICULTY: standard\n")
	b.WriteString("DEPENDS-ON: 2\n")
	b.WriteString("TOUCHES: pkg/cli/**\n")
	b.WriteString("VERIFY: true\n")
	b.WriteString("BODY:\n")
	b.WriteString("Implement the greeting cli on top of the api.\n")
	b.WriteString("Acceptance: it runs.\n")
	b.WriteString("===END===\n")
	return fakeScript{
		SessionID: "plan-sess",
		Events: []fakeScriptEvt{
			{Type: "text", Text: b.String()},
			{Type: "usage", In: 2000, Out: 300},
			{Type: "result"},
		},
	}
}

// lintScript emits the lint pass token so every child clears the plan gate's
// lint without a bounce.
func lintScript() fakeScript {
	return fakeScript{
		SessionID: "lint-sess",
		Events: []fakeScriptEvt{
			{Type: "text", Text: "LINT: PASS — the issue is a proper dumb issue."},
			{Type: "usage", In: 300, Out: 10},
			{Type: "result"},
		},
	}
}

// buildDelayMS is the per-event delay in a build script. With several events it
// stretches a build to ~1s of wall-clock so two builds dispatched together are
// genuinely in-flight at the same time — their store session windows overlap
// even at the store's one-second timestamp resolution, which is how the test
// proves parallelism.
const buildDelayMS = 350

// buildScript writes a unique file into the worktree and commits it (so the
// issue branch carries real content the pipeline opens a PR from and the merge
// can fast-forward), then emits build output. The verification command for each
// issue is "true", so the build's verification always passes. A per-event delay
// makes the build take long enough for concurrent builds to overlap observably.
func buildScript(issue int) fakeScript {
	path := fmt.Sprintf("clex_built_%d.txt", issue)
	return fakeScript{
		DelayMS:   buildDelayMS,
		SessionID: fmt.Sprintf("build-sess-%d", issue),
		Writes: []fakeScriptFile{
			{Path: path, Content: fmt.Sprintf("built issue %d by clex-fake-runner\n", issue)},
		},
		Commit:        true,
		CommitMessage: fmt.Sprintf("clex build #%d", issue),
		Events: []fakeScriptEvt{
			{Type: "text", Text: fmt.Sprintf("Implemented issue #%d.", issue)},
			{Type: "tool_use", Text: "Edit"},
			{Type: "usage", In: 1500, Out: 200},
			{Type: "result"},
		},
	}
}

// reviewScript approves the PR (the pipeline reads "REVIEW: APPROVE" as approval
// and, with green verification, merges the issue PR into the integration branch).
func reviewScript() fakeScript {
	return fakeScript{
		SessionID: "review-sess",
		Events: []fakeScriptEvt{
			{Type: "text", Text: "REVIEW: APPROVE — meets acceptance criteria; no blocking findings."},
			{Type: "usage", In: 800, Out: 40},
			{Type: "result"},
		},
	}
}

// simpleResult emits one line of text plus a terminal result.
func simpleResult(text string) fakeScript {
	return fakeScript{
		SessionID: "misc-sess",
		Events: []fakeScriptEvt{
			{Type: "text", Text: text},
			{Type: "result"},
		},
	}
}

// containsAny reports whether s contains any of subs.
func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}
