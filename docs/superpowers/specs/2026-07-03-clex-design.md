# clex — Design Spec

**Date:** 2026-07-03
**Status:** Approved (design phase)
**Owner:** Reiss

## Purpose

clex is a self-hosted agentic development orchestrator. It automates the workflow of turning a feature idea into a researched PRD, a dependency-aware set of GitHub issues, and parallel agent-built pull requests — controlled from Telegram, running on the owner's own machine against their existing Claude Max and ChatGPT subscriptions plus local models.

It replaces the manual loop of prompting Claude and Codex apps separately, copying output between them, and hand-creating GitHub issues.

### The workflow it encodes

1. **Idea** — a feature idea arrives (Telegram message or CLI).
2. **Research & plan** — a smart model (default: the strongest available Claude model) researches the codebase, reviews how the feature fits, and produces a PRD epic issue plus child issues tagged to it, each with dependency and file-touch metadata.
3. **Build** — on approval, unblocked issues are executed in parallel, each in its own git worktree on its own branch off `main`, ending in a PR back to `main`, cross-reviewed by a different model.

Approval gates between stages happen in Telegram with inline buttons, including model selection informed by live availability.

## Non-goals (v1)

- No web dashboard (CLI + Telegram only).
- No multi-user support; single owner.
- No external-agent adapter (Hermes / other personal agents) — deferred to a post-v1 webhook/API surface.
- No direct Anthropic/OpenAI API calls for subscription accounts — clex only shells out to official CLI binaries (see Compliance).
- Not a general CI system; verification runs the command declared on each issue, nothing more.

## Language & runtime decision

**Go.** The daemon is I/O-bound — its work is supervising child processes and polling two APIs — so raw compute speed is irrelevant; reliability and distribution dominate. Go provides a single static binary (no runtime for OSS users), goroutines as a natural fit for supervising many concurrent runners, mature libraries for the exact needs (go-github, a Telegram bot library, embedded SQLite), and a low contribution barrier. Rust was considered (Vibe Kanban's choice) and rejected for slower iteration with no meaningful reliability gain for this workload. TypeScript was rejected per the owner's performance/reliability preference and runtime-distribution overhead.

## Architecture overview

Two binaries from one module:

- **`clex`** — CLI for setup, manual pipeline control, and diagnostics.
- **`clexd`** — long-running daemon: event loop polling GitHub and Telegram (long-polling; no public endpoint required), a scheduler, and a runner supervisor.

```
Telegram ──long-poll──▶ ┌─────────────┐ ◀──poll── GitHub (issues, labels, PRs)
                        │    clexd    │
                        │  ┌───────┐  │          ┌── worktree #1 ── claude -p
   clex CLI ──local────▶│  │sched- │  │──spawn──▶├── worktree #2 ── codex exec
                        │  │ uler  │  │          └── worktree #3 ── codex --oss
                        │  └───────┘  │
                        │  SQLite     │   (runtime bookkeeping only)
                        └─────────────┘
```

### Source of truth: GitHub

Pipeline state lives on GitHub as labels; clex is crash-safe and resumable because the state machine is externally durable and human-inspectable. Any tool or agent can participate by editing labels.

**Labels (state machine):**

```
clex:idea → clex:researching → clex:planned → clex:approved
          → clex:building → clex:review → (closed via merged PR)
```

Plus markers: `clex:epic` (PRD issue), `clex:failed` (with failure comment), and routing tags `clex:agent/<name>` recording which runner owns/built an issue.

Valid transitions are enforced by the daemon; unknown/hand-edited states are re-read, never assumed. Epic issues carry the PRD as their body; child issues link to the epic.

**SQLite (runtime only):** runner sessions (PIDs, CLI session ids for resume), token/usage tracking per provider, Telegram message ↔ issue mapping, event log. Losing this database must never lose pipeline state — worst case, in-flight runs are re-dispatched.

## Components

### 1. CLI (`clex`)

- `clex init` — in a repo: creates labels, writes `.clex/config.toml` scaffold, registers the repo with the daemon.
- `clex doctor` — checks `claude` and `codex` binaries, auth state, `gh` auth, Ollama presence, Telegram token.
- `clex idea "…" [--repo r]` — file an idea without Telegram.
- `clex plan <issue>` / `clex build <issue|epic>` — manually trigger stages.
- `clex status` — pipeline view across repos; `clex pause` / `clex resume` — global kill switch.
- `clex models` — live model/provider availability and rate-limit headroom.

### 2. Scheduler

- Builds a dependency graph per epic from GitHub native issue relationships (blocked-by/blocking).
- Topological sort; every issue whose dependencies are closed/merged is *eligible*.
- **Conflict avoidance:** each issue declares `touches:` file globs (see metadata). Eligible issues with overlapping globs are serialized; disjoint ones run concurrently. Missing `touches` metadata is treated as touching everything (serialized) — planners are instructed to always emit it.
- Concurrency limits: global `max_parallel`, plus per-provider caps (defaults: claude 2, codex 2, local 4) to protect subscription rate windows.
- On PR merge, dependents are re-evaluated and dispatched.

### 3. Workspace manager

- One git worktree per issue under `~/.clex/worktrees/<repo>/<issue>-<slug>`, branched from latest `main` as `clex/<issue>-<slug>`.
- Rebases onto `main` before opening a PR; PR created via `gh pr create` with body linking the issue and epic.
- Worktrees cleaned up after merge/close; `clex gc` for manual cleanup.
- Runners are only ever given a worktree; nothing runs against the main checkout.

### 4. Runner adapters

Single interface:

```go
type Runner interface {
    // Run executes a task in dir and streams normalized events
    // (assistant text, tool use, cost, result) until completion.
    Run(ctx context.Context, task Task, dir string) (<-chan Event, error)
    Probe(ctx context.Context) (Availability, error) // auth + headroom
}
```

Adapters (all shell out to official binaries, parse their JSON stream):

- **claude** — `claude -p --output-format stream-json --verbose`, permission flags scoped to the worktree; resume via session id.
- **codex** — `codex exec --json`; resume via `codex exec resume`.
- **local** — `codex --oss` against Ollama (probe via `ollama list`); same adapter shape.

The **model registry** aggregates `Probe()` results plus clex's own usage accounting (parsing rate-limit errors and tracking the 5-hour/weekly windows heuristically) so Telegram model pickers always show what is actually available, with headroom hints.

### 5. Skills layer

- Bundled skill pack in the repo, installed to `~/.clex/skills` on setup: `to-prd`, `to-issues`, `grill-me`, `grill-with-docs` (Matt Pocock's; vendored if licensing permits, otherwise fetched by the installer the way `setup-matt-pocock-skills` does), plus **`clex-plan`** — clex's own planning skill that enforces the output contract: agent-ready child issues (files to touch, acceptance criteria, exact verification command) with dependency links and `touches:` globs.
- Discovery order: repo `.clex/skills` → user `~/.clex/skills` → bundled.
- Injection per runner: Claude Code — symlink into the worktree's `.claude/skills`; Codex — rendered into `AGENTS.md` / prompt templates. The adapter owns the mechanism; the pipeline just names required skills per stage.

### 6. Telegram bot

- Long-polling (works behind NAT, no server). Single authorized chat id (enforced).
- **Intake:** free-text idea (optionally `repo:` prefix) → files `clex:idea` issue → replies with inline keyboard: *Research now?* → model picker (from registry).
- **Plan gate:** when PRD + issues land → message with epic link, issue count, parallelism summary ("6 issues, 4 can run in parallel") → *Build?* → auto-route or per-issue model override.
- **Progress:** stage transitions, PR links, failures with [Retry] [Reassign model] [Skip].
- Commands: `/status`, `/pause`, `/resume`, `/models`.

### 7. Routing

Config rubric maps issue labels/shape to a default provider, always overridable at the Telegram gate:

```toml
[routing]
default   = "claude"
rules     = [
  { match = "refactor|ambiguous|architecture", agent = "claude" },
  { match = "mechanical|well-specified|crud",  agent = "codex"  },
  { match = "docs|tests|chore",                agent = "local"  },
]
```

Availability-aware: if a provider is near its cap, the router falls back down a preference list and tells the user it did so.

### 8. Cross-review stage

After a PR opens, clex requests review from a *different* provider than the author (e.g. `@codex review` on Claude-authored PRs, or a codex/claude review runner on the diff). Findings are posted as PR comments; the authoring runner may be re-invoked once to address them. Merge remains manual in v1 (auto-merge on green is a config flag, default off).

## Configuration

- Global: `~/.clex/config.toml` — Telegram token + chat id, provider caps, routing rules, worktree root.
- Per-repo: `.clex/config.toml` — head branch (default `main`), verification defaults, repo-specific routing/skills.
- Secrets via environment or config, never passed into prompts. `ANTHROPIC_API_KEY` is explicitly **unset** in runner environments so subscription auth cannot silently switch to pay-per-token API billing.

## Error handling & safety

- Every stage is idempotent and resumable from labels; daemon restart re-derives work from GitHub.
- Runner failure/timeout → issue reverts to `clex:approved`, failure comment posted, Telegram alert with retry/reassign/skip. Per-stage timeouts; capped automatic retries (1) before requiring a human decision.
- Global kill switch (`clex pause`, `/pause`) stops dispatch and signals running agents to finish/abort.
- Guardrails: runners confined to worktrees; never commit to `main`; branch protection on target repos is assumed and recommended by `clex doctor`.

## Compliance note (why shell-out, not API)

Anthropic permits Max/Pro subscription usage only through the official `claude` binary; routing subscription OAuth through third-party harnesses is prohibited. clex therefore launches official CLIs as child processes and never touches provider APIs with subscription credentials. OpenAI documents ChatGPT-authenticated `codex exec` for automation. This is a hard architectural constraint, not an implementation detail.

## Testing strategy

- **Unit:** scheduler (graph building, topo sort, `touches` overlap serialization, provider caps), label state machine transitions, routing rules, config parsing.
- **Adapter:** parse recorded `claude`/`codex` JSON stream fixtures; probe logic against canned CLI outputs.
- **Pipeline (integration):** a deterministic `fake` runner (scripted binary emitting the event protocol) drives the full idea→PR flow against a scratch GitHub repo; opt-in, runs in CI nightly not on every push.
- **Telegram:** handler tests via the bot library's test harness (no live Telegram in CI).

## v1 scope

Included: daemon + CLI, GitHub-labels state machine, dependency/touches-aware parallel scheduler, claude/codex/local runner adapters, model registry, bundled skill pack + user skills, Telegram intake/gates/notifications, routing rubric, cross-review stage, pause/resume, doctor.

Deferred: external-agent webhook adapter (Hermes/OpenClaw-style), web dashboard, auto-merge-by-default, multi-user, non-GitHub forges.

## Risks

- **Provider policy drift:** Anthropic's paused headless-billing change may resurface; adapter isolation limits blast radius (worst case: claude runner needs API-credit config).
- **CLI output formats change:** adapters pin minimum CLI versions; `clex doctor` verifies; fixtures catch drift.
- **Merge conflicts despite `touches`:** globs are declared by a planner and can be wrong; rebase-before-PR plus serialized overlap keeps this rare, and failures degrade to a human decision, not corruption.
- **Rate-window exhaustion:** registry headroom tracking is heuristic; caps are conservative by default.
