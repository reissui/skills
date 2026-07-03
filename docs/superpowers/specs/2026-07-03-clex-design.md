# clex — Design Spec

**Date:** 2026-07-03
**Status:** Approved (design phase)
**Owner:** Reiss

## Purpose

clex is a self-hosted agentic development orchestrator. It automates the workflow of turning a feature idea into a researched PRD, a dependency-aware set of GitHub issues, and parallel agent-built pull requests — controlled from Telegram, running on the owner's own machine against their existing Claude Max and ChatGPT subscriptions plus local models.

It replaces the manual loop of prompting Claude and Codex apps separately, copying output between them, and hand-creating GitHub issues.

**Economic principle:** premium models (Fable 5, Opus 4.8+, GPT-5.5+) are spent on *thinking* — research, planning, issue-writing, and review. Execution defaults to the cheapest model predicted to succeed, including free/local models. The system's job is to make that safe: issues are decomposed until they are simple enough for a modest model to complete stably, and anything built below the top tier is always reviewed by a top-tier model before merge.

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

**Providers are pluggable and disposable.** No provider is hardwired to any role: clex must run fully with only Claude registered, only Codex, or neither subscription (local models only, degraded but functional). Providers and models are declared in config; dropping a subscription means deleting a config block, nothing else. When multiple providers are registered, all of them are used — spread across tiers, cross-review, and fallback — alongside local models.

Each model declares a **billing mode**: `subscription` (consumes rate windows, costs no marginal money), `metered` (pay-per-token — e.g. Fable 5 from 2026-07-07), or `free` (local). Billing mode drives cost gates (see Routing) and restriction policies — a model like Fable 5 can be kept registered but fenced to explicitly-confirmed uses.

The **model registry** aggregates `Probe()` results plus clex's own usage accounting (parsing rate-limit errors and tracking the 5-hour/weekly windows heuristically) so Telegram model pickers always show what is actually available, with headroom hints. **Ollama is auto-detected**: if the binary/daemon is present, its installed models are discovered via `ollama list` and offered for tier assignment automatically (sensible defaults, overridable) — pulling a new local model makes it available without touching config.

### 5. Skills layer

- Bundled skill pack in the repo, installed to `~/.clex/skills` on setup: `to-prd`, `to-issues`, `grill-me`, `grill-with-docs` (Matt Pocock's; vendored if licensing permits, otherwise fetched by the installer the way `setup-matt-pocock-skills` does), plus **`clex-plan`** — clex's own planning skill that enforces the output contract: agent-ready child issues (files to touch, acceptance criteria, exact verification command) with dependency links and `touches:` globs.
- **The "dumb issue" contract.** `clex-plan` must decompose until every child issue passes an executability checklist: one concern per issue; files enumerated; acceptance criteria exact and testable; verification command included; zero design decisions left open; no knowledge required beyond the issue body plus the repo knowledge files (see Context & token economy). The test the planner applies: *could a modest local model complete this without asking a single question?* If not, split further or resolve the ambiguity at the plan gate.
- **Issue lint.** Before the plan gate, a cheap model runs `clex-issue-lint` over every child issue and scores it against the checklist. Failing issues bounce back to the planner (one automatic pass) before the human ever sees the plan — the goal is that the plan gate needs zero follow-up questions.
- Discovery order: repo `.clex/skills` → user `~/.clex/skills` → bundled.
- Injection per runner: Claude Code — symlink into the worktree's `.claude/skills`; Codex — rendered into `AGENTS.md` / prompt templates. The adapter owns the mechanism; the pipeline just names required skills per stage.

### 6. Telegram bot

Long-polling (works behind NAT, no server). Single authorized chat id (enforced).

**Interaction principles — the bot is a tool, not a chat:**

- **Progress messages are one line**, edited in place where the API allows rather than stacked ("`#42 building (codex-mini) — 3/5 checks passing`"). No greetings, no filler, no prose.
- **Every question ships with a proposed answer.** The recommended answer is always the first inline button — the default path is a single tap. `[✓ auth via magic link] [alter…] [skip]`. Tapping *alter* prompts for a one-line reply. The bot never asks an open question it could propose an answer to.
- **Questions are batched at the plan gate.** During research/planning the planner accumulates its open questions and proposed answers; the bot presents them as one numbered message, confirmable with a single *Confirm all* tap or altered per item. Mid-build questions are an escalation of last resort (a builder hitting one is usually an issue-lint failure to learn from).
- **Silence is the default.** Between gates, only state changes worth acting on are sent (PR opened, failure, cap reached). Everything else is available on demand via `/status`.
- **It answers when asked.** Terse-by-default doesn't mean mute: a direct question ("why did #42 escalate?", "what's left on the epic?") gets a concise answer from a cheap model with access to pipeline state and `LOG.md`. Answering never blocks or touches the running pipeline.
- **Images queue, they don't interrupt.** Photos/screenshots (single or albums) sent mid-process attach to the active idea — or to whichever issue/epic the message replies to — and are queued as context for that item's next stage. Nothing running is disturbed; the bot acknowledges with one line (`2 images queued for #42`).
- **Anything can be interrupted.** Every progress line carries a `[stop]` action, and `/stop <issue>` works from anywhere. Stopping cancels the runner, reverts the issue to its previous label, and preserves the worktree so a later retry can resume rather than restart. `/pause` remains the global switch.

**Surfaces:**

- **Intake:** free-text idea (optionally `repo:` prefix) → files `clex:idea` issue → one reply: *Research?* `[✓ fable-5] [pick model] [later]`.
- **Plan gate:** epic link, issue count, parallelism and cost summary ("6 issues · 4 parallel · est. 5 local + 1 codex"), the batched questions block, then `[✓ Build all] [adjust] [hold]`.
- **Progress:** stage transitions and PR links as single edited lines; failures with `[retry] [escalate model] [skip]`.
- Commands: `/status`, `/pause`, `/resume`, `/models`, `/costs`.

### 7. Routing: model tiers and the escalation ladder

Models are declared in config as **tiers**, not just providers. Tiers are pure configuration: any model from any provider can occupy any tier, and every role must keep working when only one provider exists. `clex doctor` validates that each role (`plan`, `build`, `review`, `lint`) resolves to at least one healthy model and warns on gaps.

```toml
[providers.claude]
kind = "claude-cli"                    # delete this block = Claude gone, nothing breaks

[providers.codex]
kind = "codex-cli"

[providers.ollama]
kind       = "ollama"
autodetect = true                      # discovered models join the local tier

[models]
fable-5     = { provider = "claude", billing = "metered" }      # metered from 2026-07-07
opus-4-8    = { provider = "claude", billing = "subscription" }
gpt-5-5     = { provider = "codex",  billing = "subscription" }
sonnet-5    = { provider = "claude", billing = "subscription" }
qwen3-coder = { provider = "ollama", billing = "free" }

[tiers]
top     = ["opus-4-8", "gpt-5-5", "fable-5"]       # plan, review, hard escalations
mid     = ["sonnet-5", "codex-mini"]               # moderate builds, issue lint
local   = ["qwen3-coder"]                          # default builders (free)

[routing]
plan     = "top"          # research/PRD/issues always use a top model
build    = "cheapest"     # cheapest tier whose Probe() is healthy, per issue
review   = "top"          # see review policy
lint     = "mid"
```

- **Build routing:** the planner stamps each issue with a difficulty estimate (`trivial | standard | complex`); the router maps trivial/standard → local, complex → mid, and only routes build work to top on explicit human override. Availability-aware: near-cap providers are skipped and the substitution is noted in the one-line status.
- **Escalation ladder:** a builder that fails its verification command twice is stopped; the issue escalates one tier, and the failed attempt's diff plus failure notes are handed to the next model (no restart from zero). Escalations surface in Telegram as `[retry] [escalate model] [skip]`.
- **Token accounting:** the registry records per-provider usage and estimated spend per issue/epic; `/costs` and `clex status` report it, and the plan gate shows the estimated model mix before approval.

**Cost gates (metered models).** Before dispatching any stage to a `metered` model, clex estimates its cost (issue size, stage type, and historical per-stage averages kept in SQLite). Below the configured threshold it proceeds silently and logs the estimate; above it, the stage holds and Telegram asks once: `#42 plan on fable-5 · est. $6.20 — [✓ proceed] [swap model] [hold]`.

```toml
[budget]
confirm_over_usd  = 2.00    # metered estimates above this require confirmation
max_usd_per_epic  = 25.00   # optional hard cap; reaching it pauses the epic
```

`subscription` and `free` models bypass cost gates entirely (they cost windows, not money) — headroom warnings cover them instead. Estimates are heuristic and improve as SQLite accumulates real per-stage history; actuals are always recorded against estimates so drift is visible in `/costs`.

### 8. Review policy

Review is where premium tokens buy safety:

- **Mandatory top-tier review** for any PR authored below the top tier (i.e. anything not Opus 4.8+/GPT-5.5+/Fable 5). This is non-negotiable in config (`review.required_below_tier = true` by default).
- Top-tier-authored PRs get **cross-review by a different top provider** (e.g. `@codex review` on Claude-authored PRs) — different model, different blind spots. If only one top-tier provider is registered, this degrades gracefully to a fresh-context, review-only session on the same provider.
- Reviews run on the **diff plus the issue's acceptance criteria**, not the whole repo. Findings post as PR comments; the authoring runner is re-invoked once to address them (escalating one tier if it was a local model that can't). Merge remains manual in v1 (auto-merge on green is a config flag, default off).

## Context & token economy

Nothing is researched twice; no model reads more than its task needs.

- **Repo knowledge files** in `.clex/context/`, committed to the repo:
  - `MAP.md` — architecture/codebase map, generated once by a top model at `clex init`, refreshed incrementally after merges touch new areas.
  - `PATTERNS.md` — conventions and "how we do X here", appended by planners when they resolve a question that will recur.
  - `LOG.md` — one line per merged clex PR (issue, what changed, where). Planners and builders read this instead of re-exploring history; it is the "have we done this before?" index.
- **Stage handoffs are files, not transcripts.** Research writes `PRD.md` (becomes the epic body); the planner emits issues; a builder receives only its issue body, the `touches` files, and excerpts of the knowledge files. No stage inherits another stage's full conversation.
- **Scoped builder context.** Builders are told to read the issue, `MAP.md`'s relevant section, and their `touches` globs — and nothing else. The dumb-issue contract is what makes this sufficient.
- **Resume, don't restart.** Retries and review-fix rounds resume the CLI session (`--resume` / `codex exec resume`) instead of paying for a fresh context; escalations carry the failed diff and notes forward.
- **Stable prompts.** Skill preambles and system prompts are deterministic and ordered stable so provider-side prompt caching (where the CLIs support it) actually hits.
- **Diff-scoped review.** Reviewers get the diff + acceptance criteria, never the repo.

## Configuration

- Global: `~/.clex/config.toml` — Telegram token + chat id, providers/models (with billing modes), tiers, routing, budget thresholds, provider caps, worktree root.
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

- **Unit:** scheduler (graph building, topo sort, `touches` overlap serialization, provider caps), label state machine transitions, tier routing + escalation ladder, issue-lint scoring against checklist fixtures (good/bad issue examples), config parsing.
- **Adapter:** parse recorded `claude`/`codex` JSON stream fixtures; probe logic against canned CLI outputs.
- **Pipeline (integration):** a deterministic `fake` runner (scripted binary emitting the event protocol) drives the full idea→PR flow against a scratch GitHub repo; opt-in, runs in CI nightly not on every push.
- **Telegram:** handler tests via the bot library's test harness (no live Telegram in CI).

## v1 scope

Included: daemon + CLI, GitHub-labels state machine, dependency/touches-aware parallel scheduler, claude/codex/local runner adapters with hot-swappable provider config + billing modes + cost gates, Ollama autodetect, model registry with tier routing + escalation ladder + token accounting, bundled skill pack (incl. clex-plan dumb-issue contract and clex-issue-lint), repo knowledge files (MAP/PATTERNS/LOG), Telegram intake/gates/notifications with confirm-or-alter UX + on-demand Q&A + image queueing + per-task stop, mandatory below-tier review + top-tier cross-review (single-provider fallback), pause/resume, doctor.

Deferred: external-agent webhook adapter (Hermes/OpenClaw-style), web dashboard, auto-merge-by-default, multi-user, non-GitHub forges.

## Risks

- **Provider policy drift:** Anthropic's paused headless-billing change may resurface; adapter isolation limits blast radius (worst case: claude runner needs API-credit config).
- **CLI output formats change:** adapters pin minimum CLI versions; `clex doctor` verifies; fixtures catch drift.
- **Merge conflicts despite `touches`:** globs are declared by a planner and can be wrong; rebase-before-PR plus serialized overlap keeps this rare, and failures degrade to a human decision, not corruption.
- **Rate-window exhaustion:** registry headroom tracking is heuristic; caps are conservative by default.
- **Cheap-builder quality:** local models will sometimes produce plausible-but-wrong work. Defenses are layered — the dumb-issue contract + issue lint (prevention), the verification command (detection), the escalation ladder (recovery), and mandatory top-tier review (backstop). If a repo shows a high escalation rate, the planner's difficulty estimates are the knob to tune.
