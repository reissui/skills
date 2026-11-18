# reissui/skills

Reiss's personal agent skills. One directory per skill at `skills/<name>/SKILL.md`, flat, Markdown only apart from small dependency-free scripts.

- **Frontmatter:** every `SKILL.md` needs `name` (equal to its directory) and `description`. Keep the description free of a bare `: ` (colon then space). The installer's YAML parser drops the skill silently if it finds one.
- **Rules are decisions.** Write each rule with a concrete value and, where it helps, the reason. "Weight 500 is the default" is a rule. "Use good typography" is not.
- **Taste comes from evidence.** A rule goes into `design-engineer` because Reiss asked for it, corrected an agent for it, or ships it in his own code. Unsettled questions go in that skill's "Open defaults" table with a fallback. Do not fill gaps with generic design advice.
- **Client brand values stay out.** Colours and fonts that belong to a client project are not personal defaults.
- **The skills follow their own rules.** No em dashes, sentence case headings, no filler.
- **New slop pattern:** add an entry to `skills/remove-slop/CATALOG.md` (signal, fix, source), a row in the `SKILL.md` table, and a rule in `scan.mjs` if it can be detected mechanically.
- **Check before finishing:** `node skills/remove-slop/scan.mjs skills README.md` should report only lines that quote a pattern as an example.
