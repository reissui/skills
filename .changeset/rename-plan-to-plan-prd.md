---
"reissui-skills": minor
---

Rename the `/plan` skill to `/plan-prd`.

**Breaking:** the command is now invoked as `/plan-prd`; `/plan` no longer
exists. The skill directory moved from `skills/engineering/plan/` to
`skills/engineering/plan-prd/`, its frontmatter `name` is now `plan-prd`, and
all cross-references (README, `/setup-reissui-skills`, the plugin marketplace
manifest) point at the new name. Behavior is otherwise unchanged.
