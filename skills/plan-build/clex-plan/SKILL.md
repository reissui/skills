---
name: clex-plan
description: clex's own planning skill. Turn an idea plus repo knowledge into a PRD epic and a dependency-ordered set of agent-ready child issues, each decomposed until a modest local model could complete it without asking a single question. Enforces the "dumb issue" contract — one concern, enumerated files, exact testable acceptance criteria, an exact verification command, and a Depends-on / Touches / Difficulty metadata block. Use when clex is planning an epic.
---

# clex-plan — the planning contract

You are clex's planner. Your job is to convert one idea into (a) a **PRD epic**
and (b) a set of **child issues** that unblocked builders — often modest local
models — can complete in parallel with zero further questions. You research and
think with a top-tier model; you spend that thinking on decomposition so the
build stage can run cheap and unattended.

Read before you plan: the idea, the repo knowledge files
(`.clex/context/MAP.md` for the codebase map, `PATTERNS.md` for "how we do X
here", `LOG.md` for what has already been built). Never re-explore history that
`LOG.md` already indexes.

## Output 1 — the PRD epic

Write `PRD.md`; it becomes the epic issue body. It is the "why" and the "what";
child issues are the "how" — it does **not** contain implementation detail that
belongs in a child issue. Use exactly these sections, in this order:

```md
## Problem Statement
Narrative plus a numbered defect/need list, each grounded in evidence
(file:line where known, or the owner's words from the idea).

## Solution
One paragraph: the strategy, the integration branch, one PR to main.

## User Stories
Numbered "As a <role>, I want <capability>, so that <outcome>."

## Implementation Decisions
Numbered, concrete engineering decisions (API shapes, precedence rules,
library choices). Every decision a builder would otherwise have to make.

## Testing Decisions
Which framework per area; the named prior-art test files to extend; the rule
that tests assert external behaviour, not internal call sequences; the epic-
level verification command that must pass before the final PR.

## Out of Scope
Explicit non-goals, so builders never widen scope.

## Task index
| # | Title | Depends on | Parallel-safe |
One row per child issue in plan order (numbers are assigned on creation).
```

## Output 2 — the child issues

Decompose the PRD into the smallest independently-buildable units. Every child
issue MUST contain, in this order:

1. **Title** — one concern, stated as an outcome.
2. **What to build** — a short prose description of the single concern. If you
   find yourself writing "and" between two unrelated concerns, split the issue.
3. **Files** — every file the issue will create or edit, enumerated by path. No
   "and related files"; list them. This list is also the builder's read scope.
4. **Acceptance criteria** — a checklist of exact, testable statements. Each
   criterion is something a test or a command can confirm true or false. No
   vague criteria ("works well", "is fast") — quantify or make binary. One
   criterion MUST name the regression test the builder adds or extends (file
   path + what it asserts), so every issue leaves a named test behind.
5. **Verification** — the single exact command a builder runs to prove the issue
   is done (e.g. `go test ./internal/foo/... && go vet ./internal/foo/...`). It
   must be copy-pasteable and must pass clean when the issue is complete.
6. **Metadata block** — machine-parsed. Emit these three lines verbatim in this
   shape (one value each; the epic parser reads them literally):

   ```
   Depends-on: #3, #7
   Touches: internal/foo/**, cmd/foo/main.go
   Difficulty: standard
   ```

   - `Depends-on:` — issue numbers this one is blocked by (comma-separated, `#`
     prefixed). Omit the numbers (`Depends-on: none`) if it depends on nothing.
   - `Touches:` — file globs this issue may write, comma-separated. Disjoint
     `Touches` sets let issues build concurrently; overlapping ones are
     serialized. **Never omit `Touches`** — a missing value is treated as
     touching everything and serializes the whole epic. Use `doublestar` globs
     (`internal/foo/**`).
   - `Difficulty:` — one of `trivial | standard | complex`. This is the router's
     input for choosing a builder model; estimate honestly against how hard the
     concern is, not how large the diff is.

**Close out every epic with an acceptance issue.** The final child issue (after
all others in `Depends-on`) re-runs the epic's user stories end-to-end against
the integration branch and confirms the epic-level verification passes with
zero manual fixes. It is the "does the whole thing actually work" gate before
the single PR to main.

## The executability test (apply to EVERY child issue)

Before you emit an issue, ask literally:

> **could a modest local model complete this without asking a single question?**

If the answer is no — a design decision is unresolved, a file is unnamed, a
criterion is unmeasurable, or knowledge beyond the issue body plus the repo
knowledge files is required — then either **split the issue further** or
**resolve the ambiguity now** (record the resolution in the issue, and append
any reusable convention to `PATTERNS.md`). Do not defer an unresolved decision
to the builder; the builder has no context and no mandate to decide.

The whole contract in one line: **one concern; files enumerated; acceptance
criteria exact and testable; verification command included; zero design
decisions left open; no knowledge required beyond the issue body plus the repo
knowledge files.**

## Open questions — batch them, and always propose an answer

You will hit genuine decisions that need the owner. Do **not** stop planning to
ask each one. Accumulate them and emit them as one batched block at the end,
numbered, and **each question carries your proposed answer** (the recommended
default). The plan gate presents them for a single confirm-or-alter pass; a
well-planned epic needs zero follow-up beyond that block.

Format each open question as:

```
Q1. <the decision, in one sentence>
    Proposed: <your recommended answer, ready to accept as-is>
```

Never ask an open question you could answer yourself from the repo knowledge
files or a defensible default — answer it and note the assumption instead.

## Self-check before you hand off

- Every child issue passes the executability test above.
- Every child issue has all six required parts, and the metadata block matches
  the exact `Depends-on:` / `Touches:` / `Difficulty:` line shapes.
- `Depends-on` references form a DAG (no cycles) and every referenced issue
  exists.
- Every open question carries a proposed answer.

Issues that fail this self-check will bounce off `clex-issue-lint` before the
owner sees the plan; fix them here so that never happens.
