# reissui/skills

Portable, AFK development orchestration for coding agents. Two skills —
**`/plan`** and **`/build`** — that take a feature idea to a merged PR, working
identically in **Claude Code** and **Codex**, with no daemon and no build step.
GitHub issues are the only state store, so you can plan an idea in one AI and
build it in another.

```
/plan  <idea>   → a GitHub epic (PRD) + dependency-ordered "dumb" child issues
/build <epic#>  → parallel isolated builds → one PR that closes every issue
```

## Install

```sh
npx skills@latest add reissui/skills
```

Pick the skills you want and the agent(s) to install them on (Claude Code and/or
Codex).

## `/plan <idea>`

Explores the repo live, then writes a PRD epic plus child issues so unambiguous
that any capable model can build each without asking a question — one concern,
every file enumerated, exact testable acceptance criteria, one verification
command, and `Depends-on` / `Touches` / `Difficulty` metadata. It self-lints,
shows you the plan once (skip with `--yolo`), then creates the issues on GitHub.
Nothing is written to your repo.

## `/build <epic#>` (or a list of issue numbers)

Reads the issues back, schedules them into dependency waves, and builds each
parallel-safe issue in its own git worktree (serial fallback where worktrees
aren't available). It runs **fully AFK** — a stuck issue is parked (`blocked`
label + reason) and skipped, never blocking the run. It integrates onto one
branch and opens a **single PR** that summarises the work and carries
`Closes #` for the epic and every built issue, so merging closes them all. Pass
`--merge` to merge automatically (and close any squash-merge stragglers).

## The parked clex implementation

This repo grew out of [clex](./go), a daemon-backed Go orchestrator that proved
this model. That implementation is retained under [`go/`](./go) as a working
reference; the skills above are its portable, daemonless distillation.

## License

See [LICENSE](./LICENSE).
