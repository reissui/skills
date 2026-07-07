# reissui/skills — repo instructions

This repo distributes coding-agent skills. The primary artifacts are the
Markdown skills under `skills/engineering/<name>/SKILL.md` (one directory per
skill, grouped by category); the Go code under `go/` is the parked clex
implementation and is not part of the skill runtime.

- **Skills are instruction-only.** They must stay portable across Claude Code and
  Codex: drive subagents in prose, use `gh` and `git` directly, never depend on
  one harness's machine API or on any persisted repo state. GitHub issues are the
  only store.
- **Editing skills:** every `skills/**/SKILL.md` needs YAML frontmatter with
  `name` (equal to its directory) and `description`. The `description` value is
  parsed by a strict YAML library — keep it free of a bare `: ` (colon-space) or
  the installer silently drops the skill. Run `npm run validate` to check; CI
  runs it too.
- **The parked Go code** builds from `go/`: `cd go && go build ./... && go vet
  ./... && go test ./...`. Do not rewrite its module path.
- **Releases** use changesets: `npm run changeset` to record a change; the
  release workflow versions and tags on merge to `main`.
- **Design + plans** live in `docs/superpowers/`.
