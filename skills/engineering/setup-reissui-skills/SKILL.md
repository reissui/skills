---
name: setup-reissui-skills
description: Configure a repo for the reissui/skills engineering skills — confirm the GitHub remote and gh auth, agree the label set /plan-prd and /ship use (the blocked label and any epic label), and record where design docs live. Run this once per repo before using /plan-prd and /ship. Use when the user says /setup-reissui-skills or asks to set up these skills in a repo.
---

# /setup-reissui-skills — one-time per-repo setup

Run this once in a repo before using `/plan-prd` and `/ship`. It confirms the
preconditions those skills need and agrees a few conventions with the user, so
the later skills never stop to ask. Keep it short — verify what you can, ask
only what you cannot infer.

## Step 1 — Confirm the GitHub precondition

`/plan-prd` and `/ship` use GitHub issues as the only state store. Verify, without
asking:

- The repo has a GitHub remote (`git remote -v`). If not, tell the user these
  skills need a GitHub-backed checkout and stop.
- `gh` is installed and authenticated (`gh auth status`). If not, tell the user
  to run `gh auth login` and stop.
- Report the resolved `owner/repo` and default branch so the user can confirm
  it is the right target.

## Step 2 — Agree the label set

`/ship` parks a blocked issue with a label, and `/plan-prd` may mark an epic. Confirm
the labels to use and create any that are missing (`gh label list`, then
`gh label create` for absent ones). Defaults, offered for one confirm-or-alter:

- `blocked` — applied by `/ship` to an issue it parks (defer-and-continue).
- `epic` — optional, applied by `/plan-prd` to the PRD epic issue.

If the user already uses different names for these, record their choice and use
those instead.

## Step 3 — Record where design docs live

`/plan-prd` writes the PRD to a GitHub issue and nothing to the repo, but if the
user wants a spec or notes committed alongside code, agree the location now
(default: `docs/`). Note the choice; do not create files unless asked.

## Step 4 — Confirm ready

Summarize what was verified and agreed (repo target, label set, docs location)
in a few lines, and confirm `/plan-prd` and `/ship` are ready to use. Persist
nothing beyond what the user explicitly asked to commit — the agreed
conventions live in this summary, not in a config file.
