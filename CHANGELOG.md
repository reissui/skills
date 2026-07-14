# reissui-skills

## 0.4.0

### Minor Changes

- 87cc186: Make `plan-prd` and `ship` goal-aware, add resume-safe wave checkpoints, and use task-scoped follow-up loops only for external CI and review waits.

## 0.3.0

### Minor Changes

- 414e732: Rename the `/plan` skill to `/plan-prd`.

  **Breaking:** the command is now invoked as `/plan-prd`; `/plan` no longer
  exists. The skill directory moved from `skills/engineering/plan/` to
  `skills/engineering/plan-prd/`, its frontmatter `name` is now `plan-prd`, and
  all cross-references (README, `/setup-reissui-skills`, the plugin marketplace
  manifest) point at the new name. Behavior is otherwise unchanged.

## 0.2.0

### Minor Changes

- f74536b: Reorganize skills under `skills/engineering/` (`plan`, `ship`), add a `grill`
  skill for stress-testing a plan before building, and drop the redundant
  `clex-plan` / `clex-issue-lint` skills (their contract is already baked into
  `plan`). Add repo tooling: a `SKILL.md` frontmatter validator wired into CI
  (catches the strict-YAML failure that can silently drop a skill), changesets,
  and a release workflow.
