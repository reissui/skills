# reissui/skills — repo instructions

This repo distributes coding-agent skills. The primary artifacts are the
Markdown skills directly under `skills/` (one directory per skill — the
`npx skills` installer discovers them as immediate children of `skills/`, so do
not nest them in a category subdirectory); the Go code under `go/` is the parked
clex implementation and is not part of the skill runtime.

- **Skills are instruction-only.** They must stay portable across Claude Code and
  Codex: drive subagents in prose, use `gh` and `git` directly, never depend on
  one harness's machine API or on any persisted repo state. GitHub issues are the
  only store.
- **Editing skills:** every `skills/**/SKILL.md` needs YAML frontmatter with
  `name` (equal to its directory) and `description`.
- **The parked Go code** builds from `go/`: `cd go && go build ./... && go vet
  ./... && go test ./...`. Do not rewrite its module path.
- **Design + plans** live in `docs/superpowers/`.
