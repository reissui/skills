# Telegram chat + /plan + /build: wiring the conversation layer end-to-end

Date: 2026-07-06. Status: approved for build (autonomous session; owner brief 2026-07-06).
Builds on: `2026-07-03-clex-design.md` (v1 epic, all 19 issues merged).

## Problem

The v1 epic shipped every layer but never connected the conversation seam. Concretely,
verified against main (6861989):

1. **Free text is dropped.** `internal/telegram/update.go` ignores any non-command,
   non-image message (`handleMessage`, "not the transport's concern"). The transport has
   no text hook at all — only `Handle` (slash commands) and `OnImages`. The operator can
   only run the seven registered commands.
2. **botflows is dead code.** Nothing outside `internal/botflows` imports it. Its
   `Daemon` interface (Idea/Research/ProceedGate/Ask/…) has no production implementation.
3. **Plan never runs.** The daemon loop only dispatches builds for `clex:approved`
   issues (`reconcile` → `scheduler.Next` → `dispatchBuild`). Nothing invokes
   `pipeline.Plan` — not the daemon, not the CLI (`clex plan` and `clex idea` only set
   labels and print "the daemon will research and plan it", which is false).
4. **No plan gate.** `createPlan` labels children `clex:approved` directly, so if Plan
   ever ran, builds would start with no human review of the plan.
5. **No chat model, no history.** `[routing.bot]` exists in config but nothing uses it.
6. **No merge confirm.** `Assemble` opens the final PR; merge happens only via the
   `AutoMergeFinalPR` config flag. There is no Telegram confirm.

The owner's target UX (2026-07-06 brief): talk to clex in Telegram in natural language
(Claude-powered chat, configurable model); `/plan` turns intent into a PRD epic +
sub-issues on GitHub; `/build <epic#>` executes the whole epic subagent-style into one
integration branch and opens a single PR against main; the owner merges manually or
confirms and clex merges. Planner = Claude, builder = GPT (codex) by default. Builder
prompts are goal-driven: complete as much as possible with no human in the loop. The
process mirrors reissui/middle-app's PRD-to-main workflow (Matt Pocock-style), whose
conventions were extracted verbatim on 2026-07-06 (PRD sections, dumb-issue contract,
integration-branch mode, review verdicts).

## Design

Everything below is additive to the daemon/transport seams; the pipeline's stage
contracts are unchanged except one label (gap 4).

### 1. Transport: free-text hook

`Transport.OnText(fn func(ctx context.Context, text string, replyToMsgID int))`,
symmetric with `OnImages`. `handleMessage` calls it for non-command, non-alter,
non-image text. No behavior change when unregistered. The existing alter-reply
precedence is kept (a pending ask still consumes the line first).

### 2. Chat: free text → conversational bot role

New `internal/daemon/chat.go`. Free text becomes a chat turn:

- Model: first option from `registry.Available(core.RoleBot)` — configurable via
  `[routing.bot]`; init defaults it to Claude (see §6). `/model` shows the current chat
  model + options; `/model <id>` re-points chat at any declared model for this daemon run.
- History: the runner adapters already support session resume (`core.Task.ResumeID`).
  The daemon keeps one chat session (`chatState{model, sessionID}`); each turn resumes
  the previous one, giving real back-and-forth. `/model` resets the session (a session
  belongs to one CLI/provider).
- Execution: chat turns run off-loop in a serialized goroutine (one at a time, a
  busy-turn message if one is already running), in the repo checkout (read context,
  never a worktree), with the bot-role effort/fast flags. The reply is sent verbatim
  as one message. Errors send one terse failure line.
- The chat preamble tells the model what it is (clex's conversational front-end for
  repo X), to answer concisely for Telegram, and that /plan and /build exist — so
  "chat about it, then /plan" is a natural handoff.

Q&A heuristics (`botflows.MaybeAnswer`) are superseded: chat handles questions and
statements alike. botflows stays as-is (unwired) rather than growing a second half-wired
path; its progress renderers may be adopted later.

### 3. Plan flow: /plan → PRD epic + sub-issues → gate

- `/plan <idea text>` files a `clex:idea` issue via the GitHub port (same as `clex idea`),
  then planning starts on the next reconcile. Bare `/plan` with an active chat session
  first asks the chat session to distill the conversation into an idea brief (title +
  body), then files that. Bare `/plan` with no session → usage line.
- **Daemon plan dispatch (fixes gap 3):** `reconcile` now also picks up non-epic
  `clex:idea` issues: label → `clex:researching`, run `Stages.Plan` in a goroutine
  (tracked in the running set, stage "plan", so /status shows it and /stop cancels it),
  completion posts back onto the loop (`evPlanDone`). On success: idea issue is labeled
  `clex:planned` and commented `planned as epic #E`; Telegram gets the gate summary —
  epic number/title, child list with dependency notes, lint failures if any, the PRD's
  open questions — and the instruction `/build <epic#> to start`. On failure: idea
  reverts to `clex:idea` with a comment; Telegram gets one failure line.
- **Plan gate (fixes gap 4):** `createPlan` labels children `clex:planned` (not
  approved). Nothing builds until the owner acts. Crash recovery: `clex:researching`
  issues found at startup revert to `clex:idea` (Plan is idempotent enough to re-run;
  a duplicate epic is prevented by the existing `existingEpicNumber` short-circuit when
  the recovery scan finds an open epic whose body carries `Planned from #<idea>` — the
  epic body gains that marker line).

### 4. Build + merge: /build <epic#>, single integration branch, confirm-to-merge

- `/build <n>`: if n is an epic → flip all its open `clex:planned` children to
  `clex:approved` and reconcile; the scheduler dispatches them (parallel where Touches
  allow, serialized by DependsOn) — each lands a PR into the epic integration branch
  (existing behavior). If n is a child issue → approve just it. Reply states what was
  approved.
- When the last child lands, `Assemble` opens the single epic→main PR (existing).
  New: the Telegram notification for the final PR ends with `/merge <pr#> to merge`.
- `/merge <pr#>`: merges that PR via the GitHub port (same "merge" method Assemble's
  auto-merge uses). `AutoMergeFinalPR` keeps working; /merge is the manual confirm path.
  The owner can always merge on GitHub instead.
- CLI parity: `clex plan` / `clex build` keep their label semantics (now truthful,
  since the daemon actually plans); `clex build <epic#>` gains the same
  epic-approves-children behavior.

### 5. Prompts: middle-app conventions, goal-driven builders

- `skills/clex-plan` (planner contract) adopts the middle-app PRD skeleton for the epic
  body — Problem Statement / Solution / User Stories / Implementation Decisions /
  Testing Decisions / Out of Scope / Task index table — and the child-issue skeleton
  (Parent, What to build, behavioral acceptance-criteria checkboxes + named regression
  test + verification command, Depends on). The parseable plan-output markers that
  `parse.go` consumes are unchanged.
- `buildBuildPrompt` gains a goal-driven directive block (mirrors middle-app's
  orchestration prompt): complete the issue end-to-end without asking questions; when
  blocked, make the most reasonable assumption and record it in the PR body; never
  widen scope beyond Touches; acceptance criteria + verification must pass before done.
- Reviewer prompt unchanged (already verdict-driven).

### 6. Routing defaults: Claude plans, GPT builds

`config.Default` keeps its single-provider shape (used when only one CLI is installed).
The init wizard, when BOTH claude and codex probe healthy, writes a dual-provider
config: models `opus` (claude) and `gpt-5-codex` (codex); tiers `top=[opus]`,
`build=[gpt-5-codex]`; routing plan/review/lint/bot → `top`, build → tier `build`.
Existing configs are untouched (config is owner-owned after init).

## Ports touched

`GitHubPort` gains `CreateIssue` and `MergePR` (both already on `gh.Client`).
`TelegramPort` gains `OnText`. Fakes extended accordingly.

## Testing

Package-level, mirroring existing test style (fakes, no live services):
transport OnText routing/precedence; chat turn + resume + /model swap + busy-turn;
plan dispatch (idea→researching→planned, gate summary text, failure revert, recovery);
/plan `/build` /merge handlers against fake GH/TG; createPlan planned-label change
(plan_test.go updated); init dual-provider config; e2e suite updated where the
approved→planned gate changes its script expectations. Full gate: `go build ./... &&
go vet ./... && go test ./...` plus `go test -tags e2e ./e2e/...`.

## Out of scope

Wiring botflows' renderers/AskBatch plan gate (the summary-line gate supersedes it for
now); per-issue chat threads; multi-repo chat; changing review/assemble mechanics;
config hot-reload for `/model` (runtime override only).
