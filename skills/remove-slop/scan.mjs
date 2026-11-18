#!/usr/bin/env node
// Slop scanner for the remove-slop skill. No dependencies.
//
//   node scan.mjs <file-or-dir> [more paths] [--json]
//
// Prints file:line for every mechanical tell in CATALOG.md and a count per
// rule. Exits 1 when anything is found, 0 when clean. Hits are candidates for
// a human or agent to judge, not verdicts.

import { readdirSync, readFileSync, statSync } from "node:fs";
import { extname, join, relative } from "node:path";

const EXTENSIONS = new Set([
  ".html", ".htm", ".css", ".scss", ".js", ".jsx", ".ts", ".tsx",
  ".astro", ".vue", ".svelte", ".md", ".mdx",
]);
const SKIP_DIRS = new Set([
  "node_modules", ".git", "dist", "build", ".next", ".astro", ".vercel", "out", "coverage",
]);

const SMALL_WORDS = new Set([
  "a", "an", "and", "as", "at", "but", "by", "for", "from", "in", "into", "of",
  "on", "or", "the", "to", "via", "with", "vs",
]);

/** True when a heading of 3+ words capitalises every main word. */
function isTitleCase(text) {
  const words = text.replace(/&[a-z]+;/g, " ").split(/\s+/).filter((w) => /[A-Za-z]/.test(w));
  if (words.length < 3) return false;
  const main = words.slice(1).filter((w) => !SMALL_WORDS.has(w.toLowerCase()));
  if (main.length < 2) return false;
  return /^[A-Z]/.test(words[0]) && main.every((w) => /^[A-Z]/.test(w));
}

const HEADING_HTML = /<h[1-4]\b[^>]*>([^<{]+)<\/h[1-4]>/gi;
const HEADING_MD = /^#{1,4}\s+(.+)$/;

// Pictographs, minus symbols that are ordinary typography.
const EMOJI = /(?![©®™↔↕▪▫◻◼☑✓✔✨★☆])\p{Extended_Pictographic}/u;

/** Each rule: id plus a test(line, ext) that returns true on a hit. */
const RULES = [
  { id: "em-dash", test: (l) => /—|&mdash;|&#8212;|\\u2014/.test(l) },
  {
    id: "eyebrow",
    test: (l) =>
      /\b(eyebrow|kicker|overline|pretitle|pre-title|supertitle)\b/i.test(l) ||
      (/\buppercase\b/.test(l) && /\btracking-(wide|wider|widest|\[)/.test(l) && /\btext-(xs|sm|\[1[0-3]px\])/.test(l)),
  },
  { id: "uppercase", test: (l) => /text-transform:\s*(uppercase|capitalize)/i.test(l) || /["'\s](uppercase|capitalize)["'\s]/.test(l) },
  {
    id: "title-case",
    test: (l, ext) => {
      if (ext === ".md" || ext === ".mdx") {
        const m = l.match(HEADING_MD);
        return m ? isTitleCase(m[1]) : false;
      }
      HEADING_HTML.lastIndex = 0;
      let m;
      while ((m = HEADING_HTML.exec(l))) if (isTitleCase(m[1].trim())) return true;
      return false;
    },
  },
  { id: "numbered-label", test: (l) => />\s*0[1-9]\s*[./:]?\s*</.test(l) || /["'`]0[1-9]\s*[./]?["'`]\s*[,}]/.test(l) },
  { id: "emoji", test: (l) => EMOJI.test(l) },
  { id: "check-bullet", test: (l) => /[✓✔✨★☑]|&check;|&#10003;|&#10004;/.test(l) },
  { id: "arrow", test: (l) => /→|↗|&rarr;|&#8594;|\b(ArrowRight|ArrowUpRight|ChevronRight|MoveRight)\b/.test(l) },
  {
    id: "stock-phrase",
    test: (l) =>
      /\b(seamless(ly)?|effortless(ly)?|elevate[sd]?|unlock(s|ed)?|supercharge[sd]?|empower(s|ed|ing)?|streamline[sd]?|cutting[- ]edge|game[- ]chang\w+|world[- ]class|next[- ]level|revolutioni[sz]e[sd]?|reimagine[sd]?)\b/i.test(l) ||
      /\bto the next level\b|\bwhether you(’|')re\b|\bin today(’|')s\b|\blook no further\b/i.test(l) ||
      /\b(not|isn(’|')t|more than) (just|only) [^.<]{1,50}[,.;] ?(it(’|')s|but|we(’|')re)\b/i.test(l),
  },
  {
    id: "card-stack",
    test: (l) =>
      /\bborder\b/.test(l) && /\brounded(-(md|lg|xl|2xl|3xl|\[))?/.test(l) && /\bshadow(-(sm|md|lg|xl|2xl|\[))?\b/.test(l),
  },
  { id: "accent-stripe", test: (l) => /border-(left|inline-start):\s*([3-9]|\d\d)px/i.test(l) || /\bborder-[ls]-(4|8|\[[3-9])/.test(l) },
  {
    id: "divider",
    test: (l) =>
      /<hr\b|\bdivide-[xy]\b|\b(Separator|Divider)\b/.test(l) ||
      /border-(top|bottom|block-start|block-end):\s*(?!none|0\b)/i.test(l) ||
      /["'\s]border-[tby](-\d)?["'\s]/.test(l),
  },
  { id: "badge", test: (l) => /class(Name)?=["'{`][^"'`]*\b(badge|chip)\b/i.test(l) || /<Badge\b/.test(l) || /^\s*\.(badge|chip|pill-tag)\b/i.test(l) },
  {
    id: "gradient",
    test: (l) =>
      /\b(linear|radial|conic)-gradient\(/i.test(l) || /\bbg-(gradient-to|linear-to|radial|conic)\b/.test(l) ||
      /\bbg-clip-text\b|background-clip:\s*text/i.test(l),
  },
  { id: "glow", test: (l) => /\b(glow|shimmer|grain|noise|aurora|blob)\b/i.test(l) || /\bblur-(2xl|3xl)\b/.test(l) },
  {
    id: "shadow",
    test: (l) =>
      (/box-shadow:/i.test(l) && !/box-shadow:\s*(none|var\()/i.test(l)) ||
      /["'\s:]shadow-(sm|md|lg|xl|2xl|inner)\b|["'\s]shadow["'\s]|\bdrop-shadow\b/.test(l),
  },
  {
    id: "bold",
    test: (l) =>
      /font-weight:\s*(bold|[6-9]00)\b/i.test(l) || /\bfont-(semibold|bold|extrabold|black)\b/.test(l) || /<(b|strong)>/.test(l),
  },
  {
    id: "pure-black",
    test: (l) => /#000(000)?\b(?![0-9a-f])/i.test(l) || /rgba?\(\s*0[\s,]+0[\s,]+0\s*\)/.test(l) || /["'\s:](bg|text)-black\b(?!\/)/.test(l),
  },
  { id: "ai-purple", test: (l) => /\b(indigo|violet|purple)-[1-9]00\b/.test(l) || /#(6366f1|8b5cf6|7c3aed|a855f7|4f46e5)\b/i.test(l) },
  { id: "tiny-text", test: (l) => /font-size:\s*(\d|10)(\.\d+)?px/i.test(l) || /\btext-\[(\d|10)px\]/.test(l) },
  { id: "ease-in", test: (l) => /(^|[\s:,"'`])ease-in(?![-\w])/.test(l) },
  { id: "scale-zero", test: (l) => /scale\(\s*0\s*\)|["'\s]scale-0["'\s]|\bscale:\s*0\s*[,}]/.test(l) },
  { id: "origin-center", test: (l) => /transform-origin:\s*(center|50% 50%)/i.test(l) || /["'\s]origin-center["'\s]/.test(l) },
  { id: "transition-all",test: (l) => /transition(-property)?:\s*all\b/i.test(l) || /\btransition-all\b/.test(l) },
  { id: "loop", test: (l) => /\binfinite\b/.test(l) || /\banimate-(pulse|bounce|ping)\b/.test(l) || /repeat:\s*Infinity/.test(l) },
  { id: "scroll-snap", test: (l) => /scroll-snap-type/i.test(l) || /\bsnap-(x|y|mandatory|proximity)\b/.test(l) },
];

// Rules that need a whole CSS declaration block, because the tell is a
// combination of properties that usually sit on separate lines.
const BLOCK_EXTENSIONS = new Set([".css", ".scss", ".html", ".htm", ".astro", ".vue", ".svelte"]);
const hasBorder = (b) => /(^|[;{\s])border(-width)?:\s*(?!none|0\b)[^;]+/i.test(b);
const hasRadius = (b) => /border-radius:\s*(?!0\b|999|9999|50%|100)[^;]+/i.test(b);
const hasShadow = (b) => /box-shadow:\s*(?!none)[^;]+/i.test(b);
const BLOCK_RULES = [
  { id: "eyebrow", test: (b) => /text-transform:\s*uppercase/i.test(b) && /letter-spacing:\s*0?\.\d/i.test(b) },
  { id: "card-stack", test: (b) => hasRadius(b) && (hasBorder(b) || hasShadow(b)) },
];

function collect(path, out) {
  let s;
  try {
    s = statSync(path);
  } catch {
    console.error(`scan: cannot read ${path}`);
    process.exitCode = 2;
    return;
  }
  if (s.isDirectory()) {
    for (const name of readdirSync(path)) {
      if (SKIP_DIRS.has(name) || name.startsWith(".")) continue;
      collect(join(path, name), out);
    }
  } else if (EXTENSIONS.has(extname(path).toLowerCase())) {
    out.push(path);
  }
}

const args = process.argv.slice(2);
const asJson = args.includes("--json");
const paths = args.filter((a) => !a.startsWith("--"));
if (paths.length === 0) {
  console.error("usage: node scan.mjs <file-or-dir> [more paths] [--json]");
  process.exit(2);
}

const files = [];
for (const p of paths) collect(p, files);

const hits = [];
for (const file of files) {
  const ext = extname(file).toLowerCase();
  const source = readFileSync(file, "utf8");
  const lines = source.split(/\r?\n/);
  if (BLOCK_EXTENSIONS.has(ext)) {
    const blocks = /([^{};]+)\{([^{}]*)\}/g;
    let m;
    while ((m = blocks.exec(source))) {
      const selector = m[1].trim();
      const line = source.slice(0, m.index + m[1].length - m[1].trimStart().length).split("\n").length;
      for (const rule of BLOCK_RULES) {
        if (rule.test(m[2])) {
          hits.push({ rule: rule.id, file: relative(process.cwd(), file) || file, line, text: selector.slice(0, 100) });
        }
      }
    }
  }
  lines.forEach((line, i) => {
    if (line.length > 2000) return; // minified or embedded data
    for (const rule of RULES) {
      if (rule.test(line, ext)) {
        hits.push({ rule: rule.id, file: relative(process.cwd(), file) || file, line: i + 1, text: line.trim().slice(0, 100) });
      }
    }
  });
}

const counts = {};
for (const h of hits) counts[h.rule] = (counts[h.rule] || 0) + 1;

if (asJson) {
  console.log(JSON.stringify({ files: files.length, total: hits.length, counts, hits }, null, 2));
} else {
  for (const rule of RULES) {
    const group = hits.filter((h) => h.rule === rule.id);
    if (group.length === 0) continue;
    console.log(`\n${rule.id} (${group.length})`);
    for (const h of group) console.log(`  ${h.file}:${h.line}  ${h.text}`);
  }
  console.log(`\n${hits.length} hits in ${files.length} files`);
  for (const [id, n] of Object.entries(counts).sort((a, b) => b[1] - a[1])) console.log(`  ${String(n).padStart(4)}  ${id}`);
}

if (hits.length > 0 && process.exitCode !== 2) process.exitCode = 1;
