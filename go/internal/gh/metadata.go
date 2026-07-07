package gh

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/reissui/clex/internal/core"
)

// Metadata is the clex metadata block parsed out of an issue body. Planners emit
// this block so the scheduler can order work and route builders; see spec
// sections "Source of truth: GitHub" and "Scheduler".
//
// The block is a set of "Key: value" lines, conventionally fenced in a
// ```clex ... ``` code block at the end of the body, e.g.:
//
//	```clex
//	Depends-on: #3, #4
//	Touches: internal/gh/**, go.mod
//	Difficulty: standard
//	Verify: go test ./internal/gh/...
//	```
//
// Parsing is deliberately lenient: unknown or malformed lines never panic — they
// are recorded in Warnings so the caller can surface them without failing the
// pipeline (spec: "unknown/hand-edited states are re-read, never assumed").
type Metadata struct {
	// DependsOn is the set of issue numbers this issue is blocked by, parsed
	// from a "Depends-on:" line (values may be "#3" or "3", comma/space
	// separated).
	DependsOn []int
	// Touches is the list of file globs this issue may modify, from a
	// "Touches:" line. When the line is absent, Touches is the single wildcard
	// "**" and TouchesWildcard is true — meaning "touches everything", which the
	// scheduler treats as serialized against all other work.
	Touches []string
	// TouchesWildcard reports that no explicit Touches line was present, so the
	// issue is assumed to touch everything.
	TouchesWildcard bool
	// Difficulty is the planner's difficulty estimate, from a "Difficulty:"
	// line. Zero value ("") if absent or unrecognized (an unrecognized value
	// also yields a warning).
	Difficulty core.Difficulty
	// Verify is the verification command declared for this issue, from a
	// "Verify:" (or "Verification:") line. Empty if absent. Security: callers
	// must only honor this when the issue body is owner-/clex-authored; other
	// authors fall back to the repo default (spec: Security model).
	Verify string
	// Warnings collects human-readable notes about malformed or unrecognized
	// lines encountered while parsing. Never nil after ParseMetadata; empty when
	// the block was clean.
	Warnings []string
}

// wildcardTouches is the glob used when an issue declares no Touches line.
const wildcardTouches = "**"

// fenceRe matches an opening ```clex code fence (optionally with surrounding
// whitespace). The metadata block is conventionally fenced; we extract its
// contents when present but also parse bare "Key: value" lines anywhere in the
// body so a planner that forgets the fence still works.
var fenceRe = regexp.MustCompile("(?s)```+\\s*clex\\s*\r?\n(.*?)\r?\n\\s*```+")

// metaLineRe matches a single "Key: value" metadata line, capturing the key and
// the trailing value. Leading whitespace and common list markers (-, *) are
// tolerated.
var metaLineRe = regexp.MustCompile(`^\s*[-*]?\s*([A-Za-z][A-Za-z -]*?)\s*:\s*(.*)$`)

// issueRefRe extracts issue numbers from a dependency value, matching either
// "#123" or a bare "123".
var issueRefRe = regexp.MustCompile(`#?(\d+)`)

// ParseMetadata extracts the clex metadata block from an issue body. It never
// returns an error: malformed input degrades to warnings so a single bad line
// cannot break the pipeline (acceptance criterion for issue #6). A missing
// Touches line yields the wildcard "**" with TouchesWildcard set.
func ParseMetadata(body string) Metadata {
	m := Metadata{Warnings: []string{}}

	// Prefer the fenced ```clex block if one exists; otherwise scan the whole
	// body for Key: value lines.
	scanSource := body
	sawTouches := false
	if loc := fenceRe.FindStringSubmatch(body); loc != nil {
		scanSource = loc[1]
	}

	for _, raw := range strings.Split(scanSource, "\n") {
		line := strings.TrimRight(raw, "\r")
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		mm := metaLineRe.FindStringSubmatch(line)
		if mm == nil {
			// Not a Key: value line. Inside a fenced clex block this is
			// noteworthy (malformed); outside it is almost always ordinary
			// prose, so we stay silent unless we were scanning a fence.
			if scanSource != body {
				m.Warnings = append(m.Warnings,
					fmt.Sprintf("ignored malformed metadata line: %q", trimmed))
			}
			continue
		}
		key := canonicalKey(mm[1])
		val := strings.TrimSpace(mm[2])
		switch key {
		case "depends-on", "depends", "blocked-by":
			m.DependsOn = append(m.DependsOn, parseIssueRefs(val, &m)...)
		case "touches", "touch":
			sawTouches = true
			m.Touches = append(m.Touches, parseList(val)...)
		case "difficulty":
			d := core.Difficulty(strings.ToLower(val))
			if d.Valid() {
				m.Difficulty = d
			} else if val != "" {
				m.Warnings = append(m.Warnings,
					fmt.Sprintf("unrecognized difficulty %q", val))
			}
		case "verify", "verification", "verification-command", "verify-command":
			m.Verify = val
		default:
			// Only warn about unknown keys when scanning a fenced block; a bare
			// body legitimately contains many colon lines ("Note: ...").
			if scanSource != body {
				m.Warnings = append(m.Warnings,
					fmt.Sprintf("unknown metadata key %q", strings.TrimSpace(mm[1])))
			}
		}
	}

	// Deduplicate dependencies and touches while preserving first-seen order.
	m.DependsOn = dedupeInts(m.DependsOn)
	m.Touches = dedupeStrings(m.Touches)

	if !sawTouches || len(m.Touches) == 0 {
		m.Touches = []string{wildcardTouches}
		m.TouchesWildcard = true
	}
	return m
}

// canonicalKey lower-cases a metadata key and collapses internal whitespace to a
// single hyphen so "Depends on", "Depends-on", and "DEPENDS  ON" all match.
func canonicalKey(k string) string {
	k = strings.ToLower(strings.TrimSpace(k))
	k = strings.Join(strings.Fields(k), "-")
	return k
}

// parseList splits a comma- or whitespace-separated value into trimmed,
// non-empty items.
func parseList(v string) []string {
	fields := strings.FieldsFunc(v, func(r rune) bool {
		return r == ',' || r == ' ' || r == '\t'
	})
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		f = strings.TrimSpace(f)
		if f != "" {
			out = append(out, f)
		}
	}
	return out
}

// parseIssueRefs extracts issue numbers from a dependency value. Non-numeric
// tokens are recorded as warnings rather than dropped silently.
func parseIssueRefs(v string, m *Metadata) []int {
	var out []int
	for _, tok := range parseList(v) {
		sub := issueRefRe.FindStringSubmatch(tok)
		if sub == nil {
			m.Warnings = append(m.Warnings,
				fmt.Sprintf("ignored non-numeric dependency %q", tok))
			continue
		}
		n, err := strconv.Atoi(sub[1])
		if err != nil || n <= 0 {
			m.Warnings = append(m.Warnings,
				fmt.Sprintf("ignored invalid dependency %q", tok))
			continue
		}
		out = append(out, n)
	}
	return out
}

func dedupeInts(in []int) []int {
	if len(in) == 0 {
		return in
	}
	seen := make(map[int]bool, len(in))
	out := in[:0]
	for _, n := range in {
		if !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	return out
}

func dedupeStrings(in []string) []string {
	if len(in) == 0 {
		return in
	}
	seen := make(map[string]bool, len(in))
	out := in[:0]
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}
