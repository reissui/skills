---
"reissui-skills": minor
---

Reorganize skills under `skills/engineering/` (`plan`, `ship`), add a `grill`
skill for stress-testing a plan before building, and drop the redundant
`clex-plan` / `clex-issue-lint` skills (their contract is already baked into
`plan`). Add repo tooling: a `SKILL.md` frontmatter validator wired into CI
(catches the strict-YAML failure that can silently drop a skill), changesets,
and a release workflow.
