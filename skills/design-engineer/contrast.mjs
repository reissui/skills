#!/usr/bin/env node
// WCAG 2.2 contrast check for the design-engineer skill. No dependencies.
//
//   node contrast.mjs <foreground> <background> [more pairs...]
//   node contrast.mjs "oklch(0.45 0 0)" "#fff"
//
// Accepts #rgb, #rrggbb and oklch(L C H) with L as 0-1 or a percentage.
// Colours with alpha are rejected: flatten them onto their background first,
// because contrast is only defined for the colour that reaches the eye.
// Exits 1 when any pair fails AA for body text (4.5:1).

function parse(input) {
  const s = input.trim().toLowerCase();
  let m = s.match(/^#([0-9a-f]{3}|[0-9a-f]{6})$/);
  if (m) {
    const h = m[1].length === 3 ? [...m[1]].map((c) => c + c).join("") : m[1];
    return [0, 2, 4].map((i) => parseInt(h.slice(i, i + 2), 16) / 255);
  }
  m = s.match(/^oklch\(\s*([\d.]+)(%?)\s+([\d.]+)(%?)\s+([\d.]+)(?:deg)?\s*\)$/);
  if (m) {
    const L = m[2] ? Number(m[1]) / 100 : Number(m[1]);
    const C = m[4] ? (Number(m[3]) / 100) * 0.4 : Number(m[3]);
    const h = (Number(m[5]) * Math.PI) / 180;
    const a = C * Math.cos(h);
    const b = C * Math.sin(h);
    const l = (L + 0.3963377774 * a + 0.2158037573 * b) ** 3;
    const mm = (L - 0.1055613458 * a - 0.0638541728 * b) ** 3;
    const s2 = (L - 0.0894841775 * a - 1.291485548 * b) ** 3;
    const linear = [
      4.0767416621 * l - 3.3077115913 * mm + 0.2309699292 * s2,
      -1.2684380046 * l + 2.6097574011 * mm - 0.3413193965 * s2,
      -0.0041960863 * l - 0.7034186147 * mm + 1.707614701 * s2,
    ];
    return linear.map((v) => {
      const c = Math.min(1, Math.max(0, v));
      return c <= 0.0031308 ? 12.92 * c : 1.055 * c ** (1 / 2.4) - 0.055;
    });
  }
  throw new Error(`cannot parse "${input}" (use #hex or oklch(L C H) with no alpha)`);
}

function luminance(rgb) {
  const [r, g, b] = rgb.map((c) => (c <= 0.04045 ? c / 12.92 : ((c + 0.055) / 1.055) ** 2.4));
  return 0.2126 * r + 0.7152 * g + 0.0722 * b;
}

function ratio(fg, bg) {
  const [a, b] = [luminance(parse(fg)), luminance(parse(bg))].sort((x, y) => y - x);
  return (a + 0.05) / (b + 0.05);
}

const args = process.argv.slice(2);
if (args.length < 2 || args.length % 2 !== 0) {
  console.error('usage: node contrast.mjs <foreground> <background> [more pairs...]');
  process.exit(2);
}

let failed = false;
for (let i = 0; i < args.length; i += 2) {
  try {
    const r = ratio(args[i], args[i + 1]);
    const verdict = r >= 4.5 ? "pass: text" : r >= 3 ? "FAIL for text (passes only large text, icons and UI edges)" : "FAIL";
    if (r < 4.5) failed = true;
    console.log(`${r.toFixed(2)}:1  ${args[i]} on ${args[i + 1]}  ${verdict}`);
  } catch (e) {
    console.error(e.message);
    process.exit(2);
  }
}
process.exit(failed ? 1 : 0);
