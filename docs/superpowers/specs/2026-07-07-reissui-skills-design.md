# reissui/skills — portable AFK development orchestrator

**Date:** 2026-07-07
**Status:** Design approved, pending spec review
**Supersedes context:** distills the planning contract from clex (`skills/clex-plan`, `skills/clex-issue-lint`) into standalone, daemonless, cross-harness skills.

## Problem statement

clex proved a model — idea → researched PRD → dependency-aware "dumb" issues →
parallel agent-built PRs → one human-reviewed PR to `main` — but bound it to a
background daemon (`clexd`) and to a single harness. Two limitations follow:

1. **Not portable across AIs.** The planning/build logic lives in Go and in a
   daemon that watches GitHub labels. You cannot run "structure the idea" in
   Claude Code and "build it" in Codex against the same work item.
2. **Heavy footprint.** A daemon, an IPC socket, `~/.clex` state dirs, and a
   compiled binary are required before any value is delivered. This cannot be
   dropped into an arbitrary repo on an arbitrary machine.

The insight from building clex: the hard, valuable part was never the GitHub
mechanics (`gh issue create`, `gh pr create`) — it was the **planning contract**
(decompose an idea into issues so unambiguous that a modest model can build each
without asking a question) and the **AFK build discipline** (parallel isolated
builds, defer-and-continue on blockers, one clean PR). That value can live
entirely in instruction-driven Markdown skills that any capable agent harness
executes with its native tools.

## Solution

Two **global Markdown skills** — `/plan` and `/build` — that work identically in
Claude Code and Codex, with **zero build step** and **zero persisted state in the
user's repo**. GitHub is the entire state store: the PRD *is* the epic issue, the
tasks *are* child issues, the deliverable *is* one PR. Because neither skill
depends on harness-local state, the plan/build seam crosses harnesses: `/plan` in
one AI writes the contract to GitHub; `/build` in another reads it back.

The repository `reissui/clex` is renamed to `reissui/skills` and becomes a
**skills distribution repo** in the style of `mattpocock/skills`, installable via
`npx skills@latest add reissui/skills`. The existing clex Go implementation is
**parked** under a top-level `go/` directory (retained, not deleted — it remains
a working reference and the daemon-backed variant).

### Non-goals (this spec)

- No daemon, no long-running process, no IPC.
- No compiled binary in the skill path; the parked Go code is not invoked by the
  skills.
- No persisted state files in the user's repo (no `.clex/`, no `PRD.md` in-tree,
  no cached context). GitHub is the only store.
- No changesets / publish workflow / `setup-*` skill yet (B2, deferred — see
  "Future").

## Cross-harness reality (the binding constraint)

Both harnesses support the primitives this design needs, but **express them
differently**, so the skills are written as **prose instructions**, never as
calls to a harness-specific API:

| Capability | Claude Code | Codex |
|---|---|---|
| Command discovery | `~/.claude/skills/<name>/SKILL.md` | `~/.codex/prompts/<name>.md` (flat) |
| Parallel subagents | Agent tool (machine API) | prose-triggered ("spawn N agents, wait for all, return summaries") |
| Git worktrees | available | available |

Key facts confirmed from the Codex subagents docs
(`developers.openai.com/codex/concepts/subagents`):

- Codex subagents are **triggered in prose**, not defined in a config file:
  *"direct instructions such as 'spawn two agents,' 'delegate this work in
  parallel,' or 'use one agent per point.'"*
- A good subagent prompt must state **how to divide the work, whether to wait for
  all agents, and what summary to return.**
- **Parallel writers conflict:** *"Be more careful with parallel write-heavy
  workflows, because agents editing code at once can create conflicts."* This is
  why per-issue **git-worktree isolation** is mandatory for parallel builds in
  both harnesses.

Consequently the skills instruct the agent in natural language ("for each
parallel-safe issue, spawn one subagent working in its own git worktree; wait for
all in the wave; then integrate"), which is valid guidance for Claude's Agent
tool *and* Codex's prose-triggered subagents. One authored source; identical
outcome.

## Repository restructure (A1 + B1)

### The rename

Rename the GitHub repo `reissui/clex` → `reissui/skills` (preserves history and
issues). Update `origin`. This is an owner action performed with explicit
confirmation at execution time — not assumed.

### Target layout

```
reissui/skills/
  README.md                     # front door: what it is, one-line install
  CLAUDE.md                     # repo-level agent instructions
  LICENSE
  .gitignore
  .claude-plugin/
    marketplace.json            # Claude plugin/marketplace manifest
  .agents/                      # cross-harness config consumed by `npx skills add`
  skills/
    plan-build/                 # category folder for this skill family
      plan/SKILL.md             # /plan
      build/SKILL.md            # /build
      clex-plan/SKILL.md        # existing planning contract (moved, retained)
      clex-issue-lint/SKILL.md  # existing lint gate (moved, retained)
  docs/
    superpowers/specs/          # this spec travels here under the new root
    ...                         # existing clex design docs retained
  go/                           # PARKED clex Go implementation (unchanged, working)
    cmd/ internal/ e2e/ deploy/ packaging/
    go.mod go.sum Makefile .goreleaser.yaml install.sh bin/
```

### The Go parking

Move — via `git mv` to preserve history — every Go artifact into `go/`:
`cmd/`, `internal/`, `e2e/`, `deploy/`, `packaging/`, `go.mod`, `go.sum`,
`Makefile`, `.goreleaser.yaml`, `install.sh`, `bin/`. After the move:

- The repo root is **no longer a Go module**; `go/` is the module root.
- The parked code must still build from `go/` (`cd go && go build ./... && go vet
  ./... && go test ./...`) — moving it must not break it. Any hardcoded
  module-relative paths in CI or scripts are updated to the `go/` prefix.
- `scratch/` is dropped from the tracked tree (it is untracked working scratch).

### Distribution (B1, lean)

Installable identically to the reference repo:

```sh
npx skills@latest add reissui/skills
```

The installer reads `skills/**/SKILL.md`, `.claude-plugin/marketplace.json`, and
`.agents/` to install the selected skills onto the selected harness(es) (Claude
Code and/or Codex). No changesets, no GitHub Actions publishing, no `setup-*`
skill in v1 — the layout is shaped so those (B2) can be added later without
rework.

## Skill 1 — `/plan <idea>`

Purpose: convert one idea into a **PRD epic** plus **dependency-ordered child
issues** so unambiguous that a modest model can build each without a single
question. Inherits the clex-plan "dumb issue" contract verbatim.

### Flow

1. **Explore the repo live** (A1 knowledge model — nothing persisted): read
   `README`, detect stack + build/test/lint commands, grep for conventions,
   inspect recent commits. Bake findings directly into the PRD and issues.
2. **Draft the PRD epic** — sections: Problem Statement, Solution, User Stories,
   Implementation Decisions, Testing Decisions, Out of Scope, Task index. The
   PRD is the "why/what"; issues are the "how".
3. **Decompose into child issues.** Each child issue MUST contain, in order:
   Title (one concern, as an outcome); What to build; **Files** (every path
   enumerated — also the builder's read scope); **Acceptance criteria** (exact,
   testable, one naming the regression test added); **Verification** (one exact
   copy-pasteable command); **Metadata block** with the exact line shapes:
   ```
   Depends-on: #3, #7      (or: none)
   Touches: internal/foo/**, cmd/foo/main.go
   Difficulty: trivial | standard | complex
   ```
4. **Acceptance issue.** The final child (after all others in `Depends-on`)
   re-runs the epic's user stories end-to-end against the integration branch.
5. **Self-lint** every issue against the executability checklist (the
   clex-issue-lint criteria: one-concern, files-enumerated,
   acceptance-criteria-testable, verification-command, metadata-block,
   no-open-decisions, self-contained). Fix failures in one pass.
6. **Gate (default):** present the plan; the owner approves or alters.
   **Skippable** with a trailing "just create them" / `--yolo` flag for trusted
   ideas.
7. **Create on GitHub** via `gh`: the epic issue, then each child issue. GitHub
   has no native parent/child issue field, so the epic↔child link is expressed
   two ways (as clex did): the **epic body's Task index** lists every child by
   number, and **each child body references the epic** (`Epic: #<epic>`) and
   carries its `Depends-on:` line. This is a convention the skills read back, not
   a GitHub feature. GitHub is now the source of truth; nothing is written to the
   working tree.

### The executability test (applied to every issue)

> **Could a modest local model complete this without asking a single question?**

If no — a decision is unresolved, a file unnamed, a criterion unmeasurable, or
outside knowledge required — **split the issue or resolve the ambiguity now**.
Never defer a decision to the builder. Open questions that genuinely need the
owner are **batched** at the end, each carrying a proposed default answer, and
resolved in the single plan gate.

## Skill 2 — `/build <epic#>` (or a list of issue #s)

Purpose: build the planned work fully AFK and deliver one PR. Never blocks
mid-flight.

### Flow

1. **Read work from GitHub.** Given an epic #, read the epic body + all child
   issues via `gh`. Given a list of issue #s, use those directly.
2. **Create one integration branch** `build/prd-<epic#>` off the default branch.
3. **Schedule into waves** by the `Depends-on` DAG. Within a wave, issues with
   disjoint `Touches` sets are parallel-safe.
4. **Build each wave in parallel, isolated.** For each parallel-safe issue in the
   wave, spawn **one subagent working in its own git worktree** branched off the
   integration branch. Each subagent builds exactly its single issue and
   self-verifies by running the issue's Verification command. Wait for the whole
   wave. **Fallback:** where git worktrees are unavailable, degrade to
   **serial-on-one-branch** in dependency order — same PR outcome, slower.
5. **Integrate.** Merge each finished worktree branch back into the integration
   branch, respecting dependency order between waves.
6. **AFK blocker policy — defer-and-continue (never stop-and-ask).** When a
   subagent cannot resolve an issue (ambiguous requirement, unfixable
   verification failure, real design fork), it **parks** that issue — adds a
   `blocked` label and a comment stating precisely why — **skips it, and the
   build continues** with every other issue. Nothing is built on a guess. In a
   well-planned epic this rarely fires (the plan resolved decisions up front);
   it is the safety net.
7. **Open one PR** from the integration branch → default branch. The PR body:
   - Summarizes what was built (per-issue, one line each).
   - Lists **Blocked — need a human**: each parked issue + its reason.
   - Contains closing keywords: `Closes #<epic>` **and** `Closes #<each built
     child>`. (Parked issues are NOT closed.)
8. **Merge (only if a merge flag was passed).** Merge the PR. Then — because
   squash-merges do not reliably fire GitHub's auto-close for every linked issue
   (observed directly while building clex) — **verify each linked issue actually
   closed and explicitly `gh issue close` any straggler.** Without the flag, the
   PR is left open for the owner (and merging it later in the GitHub UI still
   auto-closes via the keywords).

### AFK guarantee

`/build` runs to completion without human input. The only human-facing outputs
are the finished PR and its "needs a human" list. An overnight run is never
stranded by one ambiguous issue.

## The plan/build seam (the portability payoff)

Because the contract lives in **GitHub issues**, not harness-local state:

- Run `/plan` in Claude Code → epic + children created on GitHub.
- Run `/build <epic#>` in Codex → reads the same issues, builds, opens the PR.
- Or the reverse. Or the same harness for both.

This is the "structure the idea in one AI, build it in another" capability, and
it falls out of B1 (GitHub is the only store) for free.

## Behavior summary (gates)

| Stage | Default | Human gate? |
|---|---|---|
| `/plan` | research → PRD + issues → self-lint → present | **Yes**, one gate (skippable with `--yolo`) |
| create issues on GitHub | on plan approval | — |
| `/build` | waves → parallel isolated subagents → integrate → PR | **No** — fully AFK; defers blockers |
| merge | only on merge flag; then verify-and-close stragglers | **No** if flag; else PR left open |

## Testing / verification decisions

This repo is primarily Markdown skills; "tests" are behavioral and structural:

1. **Parked Go still builds.** `cd go && go build ./... && go vet ./... && go
   test ./...` passes after the move (proves the parking didn't break clex).
2. **Skill structure valid.** Every `skills/**/SKILL.md` has valid frontmatter
   (`name`, `description`) and each new skill's name matches its directory.
3. **Installer smoke.** `npx skills@latest add reissui/skills` lists the four
   skills and installs the selected ones onto a target harness without error.
4. **Cross-harness dry-run (manual acceptance).** In each of Claude Code and
   Codex: `/plan` on a trivial idea produces a lint-clean plan (issues pass the
   executability checklist); `/build` against a hand-made 2-issue epic in a
   throwaway repo produces one integration branch, isolated builds, and one PR
   whose body carries the correct `Closes #` keywords.
5. **Blocker path.** A deliberately ambiguous issue is parked (`blocked` label +
   reason comment), the build continues, and the PR's "needs a human" list names
   it — verified in the dry-run repo.

## Out of scope

- Rewriting or extending the parked Go/daemon implementation.
- Any issue tracker other than GitHub (Linear/local-files variants are future).
- A `setup-reissui-skills` configuration skill, changesets, and a publish
  workflow (deferred to B2).
- Automatic parallelism via OS-process orchestration (spawning background
  `codex`/`claude` processes) — rejected; it reintroduces the daemon complexity
  this design sheds. Parallelism is via each harness's native subagents only.

## Future (B2, documented, not built now)

- `setup-reissui-skills` skill: one-time per-repo config (issue tracker choice,
  triage labels, docs location), mirroring the reference repo's `setup-*` skill.
- Changesets + GitHub Actions publish workflow for versioned skill releases.
- Optional non-GitHub trackers behind the same `/plan` `/build` interface.
