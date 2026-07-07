# reissui/skills

Portable, AFK development orchestration for coding agents. The headline pair —
**`/plan`** and **`/ship`** — take a feature idea to a merged PR, working
identically in **Claude Code** and **Codex**, with no daemon and no build step.
GitHub issues are the only state store, so you can plan an idea in one AI and
build it in another.

```
/plan  <idea>   → a GitHub epic (PRD) + dependency-ordered "dumb" child issues
/ship  <epic#>  → parallel isolated builds → one PR that closes every issue
/grill <plan>   → get relentlessly interviewed until the plan is watertight
```

## Install

```sh
npx skills@latest add reissui/skills
```

Pick the skills you want and the agent(s) to install them on (Claude Code and/or
Codex). Run **`/setup-reissui-skills`** once per repo first — it confirms the
GitHub remote + `gh` auth and agrees the label set `/plan` and `/ship` use.

## `/plan <idea>`

Explores the repo live, then writes a PRD epic plus child issues so unambiguous
that any capable model can build each without asking a question — one concern,
every file enumerated, exact testable acceptance criteria, one verification
command, and `Depends-on` / `Touches` / `Difficulty` metadata. It self-lints,
shows you the plan once (skip with `--yolo`), then creates the issues on GitHub.
Nothing is written to your repo.

## `/ship <epic#>` (or a list of issue numbers)

Reads the issues back, schedules them into dependency waves, and builds each
parallel-safe issue in its own git worktree (serial fallback where worktrees
aren't available). It runs **fully AFK** — a stuck issue is parked (`blocked`
label + reason) and skipped, never blocking the run. It integrates onto one
branch and opens a **single PR** that summarises the work and carries
`Closes #` for the epic and every built issue, so merging closes them all. Pass
`--merge` to merge automatically (and close any squash-merge stragglers).

## `/grill <plan or design>`

Interviews you relentlessly about a plan before you build it — one question at a
time (max 10), walking each branch of the decision tree and resolving
dependencies between decisions. It looks up *facts* in the codebase itself and
only puts *decisions* to you, each with a recommended answer. It won't enact the
plan until you confirm you've reached a shared understanding. Pairs naturally in
front of `/plan`.

## `/setup-reissui-skills`

Run once per repo before the others: confirms the GitHub remote and `gh` auth,
agrees the label set (`blocked`, optional `epic`), and notes where design docs
live. Verifies what it can; asks only what it can't infer.

## Contributing

Skills live under `skills/engineering/<name>/SKILL.md`. Every `SKILL.md` needs
YAML frontmatter with `name` (matching its directory) and `description`. Run
`npm run validate` before pushing — CI runs it too, and it catches the
strict-YAML frontmatter error that would otherwise make the installer silently
drop a skill. Record notable changes with `npm run changeset`.

## The parked clex implementation

This repo grew out of [clex](./go), a daemon-backed Go orchestrator that proved
this model. That implementation is retained under [`go/`](./go) as a working
reference; the skills above are its portable, daemonless distillation.

## License

See [LICENSE](./LICENSE).
