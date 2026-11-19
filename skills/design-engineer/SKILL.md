---
name: design-engineer
description: Reiss's default taste for building interfaces. Minimal, monochrome first, small Inter type in two weights, icons over labels, tooltips over helper text, accessible colour, origin-aware motion. Use when designing or building any UI, page, component or site from scratch, when a project has no design system yet, or when reviewing UI against these defaults.
---

# Design engineer

These are Reiss's defaults. Apply them on the first pass so the work arrives the way he would have built it, without a round of corrections.

Two files sit beside this one. Read them when the work calls for it:

- [MOTION.md](MOTION.md) before writing any animation or transition.
- [GESTURES.md](GESTURES.md) before building anything that is dragged, swiped, flung or held.
- [REFERENCES.md](REFERENCES.md) when the direction is open. It lists the sites Reiss likes, measured, with what to take from each and where each loses to a rule here.

## Precedence

1. What Reiss asks for in the conversation.
2. The project's own design system, tokens, brand guide or `DESIGN.md`. Read them before writing any UI. Client brands keep their own colours and fonts.
3. This skill. It fills every gap the first two leave.

When a reference site or Figma file is given, take its structure, layout and spacing. Keep the house style for everything else. Do not import the reference's fonts, colours or visual language.

In an existing product, never invent components or patterns for parts that have not been designed yet. Build what was asked, with the tokens and components that exist.

## The stance

- **Minimal.** Show only what the person needs. If a line of text, a label or an element can go without loss, it goes.
- **Monochrome first.** Start in black, white and greys. Colour is earned: a small detail, a state, a hover.
- **Icon before word, tooltip before sentence.** Say it with an icon where an icon is unmistakable. Put extra context in a tooltip, never on the screen.
- **Space separates, not lines.** Group with whitespace and alignment before reaching for a border, a divider or a container.
- **Open but compact.** Generous padding around content, no wasted space inside it. Use the horizontal space the viewport gives you.
- **Consistent.** The same size, gap and radius for the same job, everywhere. Drift is a defect.
- **Quiet.** Nothing glossy, glowing, grainy or shimmering. Nothing moves unless it helps.
- **Accessible.** Every colour pair passes contrast. Every control works by keyboard and has a name.

## Stack

| Job | Default |
| --- | --- |
| Web app | Next.js, React, Tailwind v4, shadcn/ui |
| Content or marketing site | Astro, Tailwind v4 |
| Tailwind setup | CSS first with `@theme`. No `tailwind.config` file. |
| Standard components | shadcn/ui, wired to semantic tokens and restyled. Stock shadcn styling must never show through. |
| Custom component behaviour | UI SDK (`@ui-sdk/react` with `@ui-sdk/core`, from `vercel-labs/ui`) |
| Icons | lucide |
| Motion | CSS transitions first. `motion` when you need springs or gestures. |
| Fonts | Self-hosted (`next/font/local`, Astro local font provider, or `@fontsource-variable`). No font CDN in a real project. A single-file prototype may use a `<link>`. |

**Choosing between shadcn and UI SDK.** If shadcn has the component, use it. When a component does not exist and has to be built (a custom select, a command surface, a sortable list, anything with its own keyboard model), compose it from UI SDK behaviours. Never hand-roll keyboard navigation, focus management, selection, dismissal, popup positioning, drag and drop or ARIA. UI SDK is in beta and its API moves, so read https://ui-sdk.dev/docs before writing against it and pin the version you install.

Hide third-party branding (auth widgets, embeds) wherever the vendor allows it.

## Typography

Small, quiet type in one family. The interface should read as a single texture, with hierarchy carried by colour.

- **Inter Variable for everything.** One mono face for small meta only (timestamps, counts, version labels), never for body copy and never as decoration.
- **Two weights: 400 and 500.** 400 is the default. 500 marks what is primary: titles, names, the selected item, the main action. Nothing at 600 or above, ever.
- **13px is the interface size**, at line-height 1.4 and letter-spacing `-0.003em`. Nav, buttons, labels, lists, table cells and descriptions are all 13px.
- **A page title is 16px at weight 500.** It is the same face, only slightly larger. Titles do not shout.
- **Section headings are 13px**, the same size as the text under them. They differ by colour (`--subtle`), not by size or weight.
- **Hierarchy is colour first.** Primary text in `--fg`, secondary in `--muted`, tertiary in `--subtle`. Reach for a bigger size only after colour and weight have been used.

The whole scale:

| Role | Size | Weight | Line-height | Letter-spacing |
| --- | --- | --- | --- | --- |
| Display (rare, marketing only) | 48px | 500 | 1 | `-0.0175em` |
| Large heading | 32px or 28px | 500 | 1.2 | `-0.0175em` |
| Heading | 20px | 500 | 1.35 | `-0.0175em` |
| Reading text (articles, long prose) | 16px | 400 | 1.5 | `0` |
| Page title | 16px | 500 | 1.4 | `0` |
| Interface (default) | 13px | 400 or 500 | 1.4 | `-0.003em` |
| Small | 12px | 400 or 500 | 1.4 | `0` |
| Meta | 11px | 400 | 1.4 | `0` |

Most screens use three rows of this table: 16px title, 13px interface, 12px small. Start there and add a larger size only when the page is editorial or marketing.

- Text inputs are 16px on touch devices, because iOS zooms the page for anything smaller. They can drop to 13px behind `@media (hover: hover) and (pointer: fine)`.
- Nothing below 11px, and 11px only for meta that is not essential.
- `-webkit-font-smoothing: antialiased` and `font-synthesis: none` on the root.
- `font-variant-numeric: tabular-nums` on any number that changes or sits in a column.
- Sentence case everywhere: headings, buttons, nav, labels. No full caps and no `text-transform: capitalize`.
- No eyebrow above a heading. Give the section its real title and stop.

### Wrapping

- `text-wrap: balance` on headings and on any short centred text. `text-wrap: pretty` on paragraphs, list items and descriptions.
- Measure: paragraphs hold 60 to 75 characters a line (`max-width: 65ch`). Interface descriptions are shorter, 40 to 50 characters.
- A heading never ends with one word alone on its last line. A paragraph never ends with one either. `balance` and `pretty` handle most cases. Where they do not, change the width or the words, not the markup.
- No `<br>` to force a break in running text. It breaks at every other width.
- Keep things that belong together on one line with a non-breaking space: a number and its unit (`45&nbsp;min`), a currency amount, a first and last name in a short label.
- Labels, buttons, tabs and nav items never wrap: `white-space: nowrap`. If a label does not fit, shorten the label.
- Truncate single-line overflow with an ellipsis and put the full value in a tooltip. Long unbroken strings (URLs, IDs) get `overflow-wrap: anywhere`.
- Left aligned by default. Never justified. Centre only short standalone lines.
- No automatic hyphenation in interface text.

## Icons

**If an icon can do the job, use the icon.** Try icon only first, text only second. Icon and text together is the last resort.

- An icon stands alone when its meaning is universal in context: close, search, add, edit, delete, copy, share, download, settings, more, back, forward, play, pause, filter, sort, expand, collapse, external link, theme.
- Where no icon is unmistakable (a domain action such as "Reconcile", "Publish", "Assign"), use text only. Do not invent a metaphor.
- Not both. An icon beside a label that says the same thing is noise. The one exception is a destructive or irreversible primary action, which may keep its word.
- Text nav has no icons. Icon nav has no text. Pick one for the whole nav.
- Every icon-only control has an `aria-label` and a tooltip with its name, plus its keyboard shortcut if it has one.
- lucide, 16px at 1.5 stroke inside 13px interfaces, `currentColor`, coloured like the text tier it sits with. One size per context.
- The hit area is at least 32px with a pointer and 44px on touch, even though the glyph is 16px. Enlarge it with padding or a pseudo-element, not by scaling the icon.
- No icon tiles (an icon in a rounded square), no decorative icons above feature titles, no arrows appended to button labels.

## Text on screen and tooltips

**No extra text unless Reiss asks for it.** That means no helper text, section descriptions, subtitles, captions, hints under inputs, placeholder sentences, empty-state paragraphs or breadcrumbs. The interface shows the thing and its name.

When something might be unclear, the answer is a tooltip on that thing, not more words on the screen.

- Put the tooltip on the element itself. Do not add a separate "i" or "?" icon when there is something to anchor to.
- Tooltip copy is a few words. A name, a shortcut, or one short clause of context. If it needs a paragraph, the design is wrong.
- First tooltip opens after a 400ms delay. Once one is open, neighbours in the same group open instantly with no animation.
- Enter over 125ms from `scale(0.97)` and `opacity: 0`, scaling from the side that touches its trigger.
- Tooltips show on keyboard focus as well as hover, stay open while the pointer is over them, and close on Escape.
- A tooltip never holds something the person must read to complete the task, because touch has no hover. Errors, required formats and consequences of a destructive action go on the screen, in as few words as it takes.
- A marketing page's hero may carry one sentence saying what the product is. That is content, not helper text.

## Colour

- Components use semantic role tokens only, never raw palette values: `--bg`, `--raised`, `--fg`, `--muted`, `--subtle`, `--line`, `--accent` (or the shadcn names when shadcn is in use).
- Neutral ramp with a near-black foreground, never pure `#000`.
- **Accent equals foreground by default.** A coloured accent appears only when the brand has one, and then at most one accent per view, on very small details.
- Text and surface tokens hold full, solid colour values in OKLCH. Do not build a palette out of opacity steps on one colour. Alpha is for two things only: the `--line` hairline and the translucent background behind the sticky nav's blur.
- No gradients, no gradient text, no glow, no grain, no gloss. A filled surface beats a faded one.
- Dark mode is a second set of the same role tokens. It follows the OS setting, and `color-scheme` is set to match. Do not add a theme toggle unless asked.

The default neutral tokens. Every text tier passes AA on both surfaces:

```css
:root {
  color-scheme: light dark;
  --bg: oklch(1 0 0);
  --raised: oklch(0.965 0 0);
  --fg: oklch(0.15 0 0);       /* 19.7:1 on bg */
  --muted: oklch(0.45 0 0);    /* 7.4:1 on bg, 6.7:1 on raised */
  --subtle: oklch(0.52 0 0);   /* 5.5:1 on bg, 5.0:1 on raised */
  --line: oklch(0 0 0 / 8%);
  --accent: var(--fg);
}
@media (prefers-color-scheme: dark) {
  :root {
    --bg: oklch(0.16 0 0);
    --raised: oklch(0.22 0 0);
    --fg: oklch(0.95 0 0);     /* 16.8:1 on bg */
    --muted: oklch(0.75 0 0);  /* 8.7:1 on bg, 7.8:1 on raised */
    --subtle: oklch(0.66 0 0); /* 5.6:1 on raised */
    --line: oklch(1 0 0 / 10%);
  }
}
```

### Accessibility

Every colour is compliant with WCAG 2.2 AA. This is a requirement, not a preference, and it outranks any other colour rule here.

| What | Minimum contrast against its background |
| --- | --- |
| Text under 24px (under 18.66px if weight 700) | 4.5:1 |
| Larger text | 3:1 |
| Icons that carry meaning, input borders, control edges, focus rings | 3:1 |
| Placeholder text | 4.5:1 |
| Disabled controls | Exempt, but keep them legible |

- Check every text and background pair, in light and dark, including text on `--raised` and on any accent. Run `node contrast.mjs <foreground> <background>` from this skill's directory. It takes hex or `oklch()` and exits 1 on a failure.
- A colour with alpha has no contrast ratio until it is flattened onto what is behind it. Check the flattened value. This is one more reason text tokens are solid.
- Small type needs the margin. 13px text in a grey that only just reaches 4.5:1 is hard to read, so the default tiers sit above it.
- Never signal state with colour alone. An error has an icon or a word as well as its red. A selected item has a weight or a marker as well as its colour.
- A coloured accent used for text must pass 4.5:1 itself. If the brand colour fails, use it for fills and non-text details and keep text in `--fg`.
- When the brand supplies a palette that fails, say so and propose the nearest passing value. Do not ship the failure silently.

## Layout and spacing

- Spacing scale: 4, 8, 12, 16, 24, 32, 48, 64, 96, 128. Every gap, margin and padding inside the layout comes from the scale. The two fluid values below (gutter and section spacing) are the only exceptions.
- Padding is equal vertically and horizontally inside a box (a surface, a panel, a cell). Buttons and pills are the exception and take wider inline padding.
- Always set an explicit max width. Reading column 620 to 640px. Page content 1200 to 1440px. Wide layouts up to 1680px.
- Page gutter `clamp(16px, 2.6vw, 40px)`. Section spacing `clamp(96px, 5.3vw + 75px, 152px)`.
- Fill the horizontal space. A heading block stranded in a narrow centred column on a wide screen is a mistake. Put the title left and the supporting content right, or let the grid span.
- Design the mobile layout on purpose. Check it at 375px.

## Shape and surface

- Radius: pills (`999px`) for buttons and tags, 16px for large surfaces, 12px for media, 24 to 28px for the top corners of sheets. Inner radius equals outer radius minus padding.
- **Cards are fill, not outline.** A raised surface is a slightly different background (`--raised`) with no border and no shadow. Never border plus shadow plus radius together.
- Prefer no container at all. A surface is earned only by a separate object the person acts on as a unit: a plan, a product, a media item, a floating layer. A section of text or a feature description sits straight on the page.
- Borders are `1px solid var(--line)` and rare. No full-width rules under the nav, no dividers between stacked blocks, no outlines around sections. An input's edge must still reach 3:1, so give inputs a `--raised` fill or a stronger edge than `--line`.
- Shadows are rare. When one is needed (a floating menu), use a 1px ring with a soft layer: `0 0 0 1px rgb(0 0 0 / 6%), 0 1px 3px rgb(0 0 0 / 4%)`.
- Backdrop blur (8 to 14px) belongs to the sticky nav only.
- Buttons are 32px high with a pointer and 44px on touch layouts.

## Motion

Subtle, smooth and fast. If someone notices the animation, it is too much. The full rules are in [MOTION.md](MOTION.md). These hold everywhere:

```css
--ease-out: cubic-bezier(0.23, 1, 0.32, 1);      /* default: anything entering, exiting or responding */
--ease-in-out: cubic-bezier(0.77, 0, 0.175, 1);  /* things already on screen that move or morph */
--ease-drawer: cubic-bezier(0.32, 0.72, 0, 1);   /* sheets and drawers */
--dur-press: 140ms;
--dur-ui: 220ms;
--dur-move: 420ms;
--dur-reveal: 700ms;
```

- **Every animation is origin aware.** A thing appears from where it came from and leaves back to it. A popover, menu, tooltip or select scales from the edge that touches its trigger. A panel that slid in from the right slides out to the right. A dialog opened by a button grows from that button, and only a dialog with no trigger scales from the centre. `transform-origin: center` on anything anchored is a bug.
- Nothing animates from `scale(0)`. Enter from `scale(0.95)` or above, with opacity.
- Never `ease-in`. Never `transition: all`. Never a keyframe on something the person can interrupt.
- Movement animates `transform` and `opacity` only. A hover or state change may also transition `color` and `background-color`.
- Interface motion stays under 300ms. Exits are faster than entrances.
- Actions triggered from the keyboard do not animate.
- Nothing loops. No scroll snapping. No scroll-triggered fade-in on every section.
- `prefers-reduced-motion`: keep the opacity change, drop all movement.

## Interaction

- Hover styles sit behind `@media (hover: hover) and (pointer: fine)`.
- Press feedback on every clickable thing: `transform: scale(0.97)` on `:active` over 140ms.
- In a list of links or titles, hover dims the siblings (to about 72% opacity) and leaves the target at full strength. Colouring the target is the second choice.
- Focus is `:focus-visible` with `outline: 2px solid currentColor; outline-offset: 3px`. Never the browser's default blue ring, never `outline: none` with nothing in its place.
- Everything reachable by pointer is reachable by keyboard, in a sensible order, with Escape closing whatever is open.
- Style `::selection`. Set `-webkit-tap-highlight-color: transparent`. Use `scrollbar-gutter: stable` where content height changes. Stock scrollbars inside custom panels get styled or hidden.

## Copy

- Concise and plainly written. Say the thing once.
- **No em dashes, ever.** Use a full stop, a comma or brackets.
- Do not explain what the person can already see.
- Never repeat the same selling point in two sections.
- Short direct labels: "Pricing for teams", not "Plans and pricing: teams". One or two words for a button.
- Sentence case. No numbered section titles ("01 Key specifications").

## Process

- **When the direction is open, show several options side by side.** Build 3 to 9 variants with a simple switcher and let Reiss pick. Do not commit to one direction and polish it. When the brief is fixed, or nobody is there to choose, build one.
- Take feedback as a batch, apply every point, then report against each point.
- Check the result at desktop and mobile widths, with reduced motion on, by keyboard alone, and with a production build where the project has one, before saying it is done. If a check could not be run, say which.
- Run `contrast.mjs` on every text and background pair you introduced.
- Finish with the `remove-slop` skill if it is installed. It cleans towards the same standard as this file. Without it, re-read the work against the Icons, Text on screen, Shape and Copy sections above.

## Reviewing against these defaults

Report findings as one table, one row per issue:

| Before | After | Why |
| --- | --- | --- |
| `font-weight: 700` on headings | `font-weight: 500`, `--fg` against `--muted` neighbours | Two weights, hierarchy by colour |
| "Settings" label beside a cog icon | Cog only, `aria-label` and tooltip "Settings" | Icon before word |
| Hint paragraph under an input | Removed, context in a tooltip on the label | No extra text on screen |
| `transform-origin: center` on a dropdown | Origin at the trigger edge | Origin aware |
| `#9a9a9a` text on white (2.8:1) | `var(--muted)` (7.4:1) | AA contrast |

## Open defaults

These are not settled. Until Reiss fills them in, use the fallback given and say that you did. The third column is what a site in [REFERENCES.md](REFERENCES.md) does. It is evidence for Reiss to choose from, not a value to apply.

| Question | Fallback | Seen on a reference |
| --- | --- | --- |
| Mono face for meta | Geist Mono | design.how uses the system mono stack |
| Base radius for shadcn (`--radius`) | `0.75rem` | design.how uses 6px on controls |
| Spelling | British (colour, grey, organise) | |
| Button hover | Opacity to 0.85 over 220ms | design.how steps `background-color` over 100ms `ease-out` |
| Control height with a pointer | 32px | design.how uses 36px |
| Icon stroke | lucide at 1.5 | |
| First tooltip delay | 400ms | |
| Display sizes on marketing pages | The fixed 28, 32 and 48px steps, no fluid `clamp()` | design.how tops out at a fixed 32px, no `clamp()` |
| Interface size | 13px | design.how uses 14px text and 15px controls |
| Content column on a docs or content site | Reading column 620 to 640px, and fill the horizontal space | design.how centres a 576px column |
