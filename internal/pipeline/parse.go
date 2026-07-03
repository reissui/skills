package pipeline

import (
	"bufio"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/reissui/clex/internal/core"
)

// This file defines the plan-stage data structures and the parser that turns a
// planner runner's textual output into a PRD plus child issues. The clex-plan
// skill emits a deterministic, machine-readable block (below) so parsing needs
// no model call of its own and is fully testable with fixtures.
//
// Planner output format (stable, line-oriented):
//
//	===CLEX-PLAN v1===
//	===EPIC===
//	TITLE: <one line>
//	<PRD body, may be multiple lines / markdown>
//	===ISSUE===
//	TITLE: <one line>
//	DIFFICULTY: trivial|standard|complex
//	DEPENDS-ON: 2, 3          (local ordinals referencing earlier ===ISSUE=== blocks, 1-based; optional)
//	TOUCHES: internal/x/**, go.mod
//	VERIFY: go test ./internal/x/...
//	BODY:
//	<issue body / acceptance criteria, multiple lines>
//	===ISSUE===
//	...
//	===QUESTION===          (zero or more; the batched plan-gate questions)
//	TEXT: <question>
//	PROPOSED: <recommended answer>
//	===END===
//
// DEPENDS-ON in planner output references sibling issues by their 1-based
// position in the plan (they have no GitHub numbers yet); the plan stage rewrites
// these to real issue numbers after creating each issue.

// ChildIssue is one planned child issue before it is created on GitHub.
type ChildIssue struct {
	Title      string
	Body       string
	Difficulty core.Difficulty
	// DependsOnOrdinals are 1-based indices into the plan's issue list.
	DependsOnOrdinals []int
	Touches           []string
	Verify            string
}

// Question is one plan-gate question with its proposed answer. The Telegram plan
// gate (#18) renders these; the plan stage only produces them (spec: Telegram
// bot — "Every question ships with a proposed answer", "Questions are batched at
// the plan gate").
type Question struct {
	// Text is the question shown to the owner.
	Text string
	// Proposed is the recommended answer, rendered as the first (✓) button.
	Proposed string
}

// PlanOutput is the parsed result of a planner run: the epic (PRD) and its
// children plus any batched questions.
type PlanOutput struct {
	EpicTitle string
	EpicBody  string
	Issues    []ChildIssue
	Questions []Question
}

// Plan-output sentinel markers.
const (
	planHeader   = "===CLEX-PLAN v1==="
	markEpic     = "===EPIC==="
	markIssue    = "===ISSUE==="
	markQuestion = "===QUESTION==="
	markEnd      = "===END==="
)

// parsePlanOutput parses a planner run's text into a PlanOutput. It is lenient
// about surrounding chatter: it starts at the first planHeader line and stops at
// markEnd (or EOF). It errors only when no epic block is present, since a plan
// with no epic cannot proceed.
func parsePlanOutput(text string) (PlanOutput, error) {
	var out PlanOutput
	sc := bufio.NewScanner(strings.NewReader(text))
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)

	const (
		stNone = iota
		stEpic
		stIssue
		stQuestion
	)
	state := stNone
	started := false

	var epicBody strings.Builder
	var curIssue *ChildIssue
	var curIssueBody strings.Builder
	inIssueBody := false
	var curQ *Question

	flushIssue := func() {
		if curIssue != nil {
			curIssue.Body = strings.TrimRight(curIssueBody.String(), "\n")
			out.Issues = append(out.Issues, *curIssue)
		}
		curIssue = nil
		curIssueBody.Reset()
		inIssueBody = false
	}
	flushQuestion := func() {
		if curQ != nil && strings.TrimSpace(curQ.Text) != "" {
			out.Questions = append(out.Questions, *curQ)
		}
		curQ = nil
	}

	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), "\r")
		trimmed := strings.TrimSpace(line)

		switch trimmed {
		case planHeader:
			started = true
			continue
		case markEnd:
			// terminal
			flushIssue()
			flushQuestion()
			goto done
		case markEpic:
			flushIssue()
			flushQuestion()
			state = stEpic
			continue
		case markIssue:
			flushIssue()
			flushQuestion()
			state = stIssue
			curIssue = &ChildIssue{}
			continue
		case markQuestion:
			flushIssue()
			flushQuestion()
			state = stQuestion
			curQ = &Question{}
			continue
		}

		if !started {
			continue // ignore any preamble before the header
		}

		switch state {
		case stEpic:
			if v, ok := field(line, "TITLE"); ok && out.EpicTitle == "" {
				out.EpicTitle = v
				continue
			}
			epicBody.WriteString(line)
			epicBody.WriteString("\n")
		case stIssue:
			if curIssue == nil {
				continue
			}
			if inIssueBody {
				curIssueBody.WriteString(line)
				curIssueBody.WriteString("\n")
				continue
			}
			switch {
			case matchesField(line, "TITLE"):
				v, _ := field(line, "TITLE")
				curIssue.Title = v
			case matchesField(line, "DIFFICULTY"):
				v, _ := field(line, "DIFFICULTY")
				d := core.Difficulty(strings.ToLower(strings.TrimSpace(v)))
				if d.Valid() {
					curIssue.Difficulty = d
				}
			case matchesField(line, "DEPENDS-ON"), matchesField(line, "DEPENDS"):
				v, _ := field(line, "DEPENDS-ON")
				if v == "" {
					v, _ = field(line, "DEPENDS")
				}
				curIssue.DependsOnOrdinals = parseOrdinals(v)
			case matchesField(line, "TOUCHES"):
				v, _ := field(line, "TOUCHES")
				curIssue.Touches = splitList(v)
			case matchesField(line, "VERIFY"):
				v, _ := field(line, "VERIFY")
				curIssue.Verify = strings.TrimSpace(v)
			case matchesField(line, "BODY"):
				inIssueBody = true
			default:
				// Ignore stray lines between fields before BODY.
			}
		case stQuestion:
			if curQ == nil {
				continue
			}
			switch {
			case matchesField(line, "TEXT"):
				v, _ := field(line, "TEXT")
				curQ.Text = v
			case matchesField(line, "PROPOSED"):
				v, _ := field(line, "PROPOSED")
				curQ.Proposed = v
			}
		}
	}
	flushIssue()
	flushQuestion()

done:
	if err := sc.Err(); err != nil {
		return PlanOutput{}, fmt.Errorf("scanning plan output: %w", err)
	}
	out.EpicBody = strings.TrimSpace(epicBody.String())
	if out.EpicTitle == "" && out.EpicBody == "" {
		return PlanOutput{}, fmt.Errorf("plan output has no epic block")
	}
	return out, nil
}

// field returns the value of a "KEY: value" line if key matches (case-
// insensitive), and whether it matched.
func field(line, key string) (string, bool) {
	idx := strings.IndexByte(line, ':')
	if idx < 0 {
		return "", false
	}
	k := strings.ToUpper(strings.TrimSpace(line[:idx]))
	if k != strings.ToUpper(key) {
		return "", false
	}
	return strings.TrimSpace(line[idx+1:]), true
}

// matchesField reports whether line is a "KEY:" line for key.
func matchesField(line, key string) bool {
	_, ok := field(line, key)
	return ok
}

// splitList splits a comma/whitespace-separated list into trimmed items.
func splitList(v string) []string {
	fields := strings.FieldsFunc(v, func(r rune) bool { return r == ',' })
	var out []string
	for _, f := range fields {
		if s := strings.TrimSpace(f); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// parseOrdinals parses "1, 2 3" into []int{1,2,3}, ignoring non-numeric tokens.
func parseOrdinals(v string) []int {
	fields := strings.FieldsFunc(v, func(r rune) bool { return r == ',' || r == ' ' })
	var out []int
	for _, f := range fields {
		f = strings.TrimPrefix(strings.TrimSpace(f), "#")
		if f == "" {
			continue
		}
		if n, err := strconv.Atoi(f); err == nil && n > 0 {
			out = append(out, n)
		}
	}
	return out
}

// renderMetadataBlock renders a fenced ```clex metadata block for a child issue,
// in the exact format gh.ParseMetadata reads back. dependsOn are already-resolved
// GitHub issue numbers. The block is deterministic (fields in a fixed order) so
// re-runs produce identical bodies.
func renderMetadataBlock(dependsOn []int, touches []string, difficulty core.Difficulty, verify string) string {
	var b strings.Builder
	b.WriteString("```clex\n")
	if len(dependsOn) > 0 {
		nums := append([]int(nil), dependsOn...)
		sort.Ints(nums)
		parts := make([]string, len(nums))
		for i, n := range nums {
			parts[i] = "#" + strconv.Itoa(n)
		}
		fmt.Fprintf(&b, "Depends-on: %s\n", strings.Join(parts, ", "))
	}
	if len(touches) > 0 {
		fmt.Fprintf(&b, "Touches: %s\n", strings.Join(touches, ", "))
	}
	if difficulty != "" {
		fmt.Fprintf(&b, "Difficulty: %s\n", difficulty)
	}
	if strings.TrimSpace(verify) != "" {
		fmt.Fprintf(&b, "Verify: %s\n", verify)
	}
	b.WriteString("```")
	return b.String()
}

// composeIssueBody appends the metadata block to the child issue's body,
// stripping any pre-existing fenced clex block first so re-rendering is
// idempotent.
func composeIssueBody(body string, dependsOn []int, ci ChildIssue) string {
	base := stripClexFence(body)
	block := renderMetadataBlock(dependsOn, ci.Touches, ci.Difficulty, ci.Verify)
	base = strings.TrimRight(base, "\n")
	if base == "" {
		return block
	}
	return base + "\n\n" + block
}

// clexFenceRe matches a fenced ```clex ... ``` block, mirroring gh's parser so
// composeIssueBody can strip a previously-appended block. It is a local copy on
// purpose: the pipeline must not reach into gh's internals, and matching keeps
// re-rendering idempotent.
var clexFenceRe = regexp.MustCompile("(?s)```+\\s*clex\\s*\r?\n.*?\r?\n\\s*```+\\s*$")

// stripClexFence removes a trailing ```clex ... ``` block from body if present,
// so composeIssueBody does not stack duplicate metadata blocks on re-run.
func stripClexFence(body string) string {
	return strings.TrimRight(clexFenceRe.ReplaceAllString(body, ""), "\n")
}
