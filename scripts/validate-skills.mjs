#!/usr/bin/env node
// Validate every skills/**/SKILL.md the way the `npx skills` installer does.
//
// The installer parses frontmatter with the strict `yaml` library and silently
// drops any skill whose frontmatter throws or lacks name/description — that is
// how a stray ": " in a description once dropped /ship with no error. This
// script reproduces that contract and FAILS LOUDLY (non-zero exit) instead, so
// CI catches a broken skill before it ships.
//
// Checks, per SKILL.md:
//   1. frontmatter block present (opens with a --- fence)
//   2. frontmatter parses under `yaml.parse` (the installer's parser)
//   3. `name` and `description` present and non-empty strings
//   4. `name` equals the skill's own directory basename (installer/agents rely on this)

import { readdir, readFile, stat } from "node:fs/promises";
import { join, basename, dirname, relative } from "node:path";
import { fileURLToPath } from "node:url";
import { parse as parseYaml } from "yaml";

const repoRoot = join(dirname(fileURLToPath(import.meta.url)), "..");
const skillsRoot = join(repoRoot, "skills");

// Same skip list the installer's directory walk uses, so we validate exactly
// the set it would discover (and never descend into build artifacts).
const SKIP_DIRS = new Set(["node_modules", ".git", "dist", "build", "__pycache__"]);

/** Recursively collect every directory that directly contains a SKILL.md. */
async function findSkillDirs(dir) {
  let entries;
  try {
    entries = await readdir(dir, { withFileTypes: true });
  } catch {
    return [];
  }
  const found = [];
  let hasSkill = false;
  try {
    hasSkill = (await stat(join(dir, "SKILL.md"))).isFile();
  } catch {
    hasSkill = false;
  }
  if (hasSkill) found.push(dir);
  for (const e of entries) {
    if (e.isDirectory() && !SKIP_DIRS.has(e.name)) {
      found.push(...(await findSkillDirs(join(dir, e.name))));
    }
  }
  return found;
}

/** Extract the raw frontmatter block, mirroring the installer's regex. */
function frontmatterOf(raw) {
  const m = raw.match(/^---\r?\n([\s\S]*?)\r?\n---\r?\n?/);
  return m ? m[1] : null;
}

const skillDirs = (await findSkillDirs(skillsRoot)).sort();
const failures = [];

for (const dir of skillDirs) {
  const rel = relative(repoRoot, join(dir, "SKILL.md"));
  const raw = await readFile(join(dir, "SKILL.md"), "utf-8");

  const fm = frontmatterOf(raw);
  if (fm === null) {
    failures.push(`${rel}: no YAML frontmatter block (must start with a --- fence)`);
    continue;
  }

  let data;
  try {
    data = parseYaml(fm) ?? {};
  } catch (err) {
    // This is the exact failure mode that silently dropped /ship.
    failures.push(`${rel}: frontmatter is not valid YAML — ${String(err.message).split("\n")[0]}`);
    continue;
  }

  if (typeof data.name !== "string" || data.name.trim() === "") {
    failures.push(`${rel}: frontmatter 'name' is missing or not a non-empty string`);
  } else if (data.name !== basename(dir)) {
    failures.push(`${rel}: frontmatter name '${data.name}' must equal its directory '${basename(dir)}'`);
  }

  if (typeof data.description !== "string" || data.description.trim() === "") {
    failures.push(`${rel}: frontmatter 'description' is missing or not a non-empty string`);
  }
}

if (skillDirs.length === 0) {
  console.error("validate-skills: no SKILL.md files found under skills/ — nothing to validate");
  process.exit(1);
}

if (failures.length > 0) {
  console.error(`validate-skills: ${failures.length} problem(s) across ${skillDirs.length} skill(s):\n`);
  for (const f of failures) console.error("  ✗ " + f);
  console.error("\nA skill that fails these checks is silently dropped by `npx skills add`.");
  process.exit(1);
}

console.log(`validate-skills: ${skillDirs.length} skill(s) OK`);
for (const dir of skillDirs) console.log("  ✓ " + relative(repoRoot, dir));
