# Slop catalogue

One entry per tell. **Signal** is how to find it (the `scan.mjs` rule id, and a grep you can run without Node). **Fix** is what to do. **Source** says where the entry comes from: `Reiss` means he has corrected an agent for it, `general` means it is a widely recognised model habit he has not yet confirmed.

## Copy

### Em dash
- **Signal:** `em-dash`. `grep -rn '—\|&mdash;' <path>`
- **Fix:** Rewrite the sentence with a full stop, a comma or brackets. Do not swap in a hyphen or an en dash.
- **Source:** Reiss. "never use em dashes... they are an AI dead giveaway".

### Eyebrow
- **Signal:** `eyebrow`. Class names `eyebrow`, `kicker`, `overline`, `pretitle`, or any rule that combines uppercase with positive letter-spacing. Check what sits above each heading by eye as well, because an eyebrow can hide behind a class called `label`. `grep -rniE 'eyebrow|kicker|overline|uppercase.*tracking-wid' <path>`
- **Fix:** Delete it. If it held the only useful words, they become the heading.
- **Source:** Reiss. "remove the { EYEBROW} and just give them their real titles".

### Full caps
- **Signal:** `uppercase`. `grep -rnE 'text-transform:\s*(uppercase|capitalize)|\b(uppercase|capitalize)\b' <path>`
- **Fix:** Sentence case, written that way in the source text.
- **Source:** Reiss. "no full caps anywhere".

### Title case headings
- **Signal:** `title-case`. A heading of three or more words where every main word is capitalised.
- **Fix:** Sentence case. Proper nouns keep their capitals.
- **Source:** Reiss (written rule in mid-harness).

### Numbered section labels
- **Signal:** `numbered-label`. A bare `01`, `02` beside or above a section title.
- **Fix:** Delete the number.
- **Source:** Reiss. "the 01 key specifications, is not a good look".

### Emoji in interface copy
- **Signal:** `emoji`.
- **Fix:** Delete. If it stood in for an icon the interface needs, use a lucide icon.
- **Source:** general.

### Check and sparkle bullets
- **Signal:** `check-bullet`. `✓ ✔ ✨ ★` or a `Check` icon repeated down a list.
- **Fix:** A plain list, or fold the points into a sentence.
- **Source:** Reiss. "remove the check bullet points".

### Arrow on a button or link
- **Signal:** `arrow`. `→`, `&rarr;`, `ArrowRight`, `ArrowUpRight`, `ChevronRight` inside a button or link label.
- **Fix:** Delete the arrow. The label is enough.
- **Source:** Reiss. "remove the arrows from buttons".

### Stock marketing phrases
- **Signal:** `stock-phrase`. Seamless, effortless, elevate, unlock, supercharge, empower, streamline, cutting-edge, game-changing, world-class, next level, "not just X, it's Y", "whether you're", "in today's".
- **Fix:** State what the thing does in plain words. If nothing concrete is left, delete the sentence.
- **Source:** general.

## Structure

### Card stack
- **Signal:** `card-stack`. A rounded box that also has a border or a shadow (in CSS, the three properties in one rule; in Tailwind, the classes on one element). Pills are ignored. Inputs and floating menus will show up here too and can stay.
- **Fix:** First choice, remove the container and let the content sit on the page. Second choice, a plain `--raised` fill with no border and no shadow.
- **Source:** Reiss. "I would prefer for each area not to be contained in a card or highlighted and bordered area".

### Accent stripe
- **Signal:** `accent-stripe`. `border-left: 3px` or thicker, `border-l-4`, on a callout or card.
- **Fix:** Delete the stripe. Then judge the box as a card stack.
- **Source:** general.

### Dividers
- **Signal:** `divider`. `<hr>`, `divide-y`, `divide-x`, full-width `border-bottom` under the nav or between blocks.
- **Fix:** Delete, then check the spacing still groups the content. A hairline survives only inside dense data (table rows).
- **Source:** Reiss. "remove the dividers between the blocks"; "nav border, and full width line is not needed".

### Decorative badge
- **Signal:** `badge`. Class or component names `badge`, `chip`, `tag` sitting above a heading or on a card with no state to report ("New", "Popular", "Beta").
- **Fix:** Delete. A real status keeps plain text in `--muted`.
- **Source:** Reiss. "remove the badge, title and description so this section is only the content".

## Surface

### Gradient
- **Signal:** `gradient`. `linear-gradient`, `radial-gradient`, `conic-gradient`, `bg-gradient-to-*`, `bg-linear-*`, and gradient text via `background-clip: text`.
- **Fix:** A solid token colour. Gradient text becomes solid foreground.
- **Source:** Reiss. "remove the gradient fade at the bottom"; "filled space rather than gradient".

### Glow, grain, shimmer and blur blobs
- **Signal:** `glow`. Class or keyframe names `glow`, `shimmer`, `grain`, `noise`, `aurora`, `blob`, and `blur-2xl`/`blur-3xl` on decorative layers.
- **Fix:** Delete the layer.
- **Source:** Reiss. "way too much gloss"; "remove the grain"; "take the shimmer off".

### Shadow
- **Signal:** `shadow`. `box-shadow` other than `none`, `shadow-sm` to `shadow-2xl`, `drop-shadow`.
- **Fix:** Delete. A layer that really floats (menu, popover, toast) keeps `0 0 0 1px rgb(0 0 0 / 6%), 0 1px 3px rgb(0 0 0 / 4%)`.
- **Source:** Reiss. "take shadow off tabs".

### Bold weights
- **Signal:** `bold`. `font-weight: 600` and above, `font-semibold`, `font-bold`, `font-extrabold`, `font-black`, `<b>`, `<strong>` used for styling.
- **Fix:** Weight 500 at most, 400 for anything that is not primary. Emphasis comes from foreground colour against muted neighbours.
- **Source:** Reiss (two weights only, 400 and 500).

### Tiny text
- **Signal:** `tiny-text`. A font size of 10px or below.
- **Fix:** 11px for inessential meta, 12 or 13px for anything that has to be read.
- **Source:** Reiss.

### Pure black
- **Signal:** `pure-black`. `#000`, `#000000`, `rgb(0 0 0)` as a text or background colour. Alpha hairlines such as `rgb(0 0 0 / 8%)` are fine.
- **Fix:** The foreground token (near-black).
- **Source:** Reiss (his own code).

### Default indigo and violet
- **Signal:** `ai-purple`. `indigo-*`, `violet-*`, `purple-*` utilities, `#6366f1`, `#8b5cf6`, `#7c3aed`.
- **Fix:** Foreground, or the project's single accent on a small detail.
- **Source:** Reiss for the principle ("I dont want them all blue"), general for these specific values.

## Motion

### `ease-in`
- **Signal:** `ease-in`. The `ease-in` keyword or utility (not `ease-in-out`).
- **Fix:** A strong ease-out curve, `cubic-bezier(0.23, 1, 0.32, 1)`. Starting slowly makes the interface feel late.
- **Source:** Reiss.

### Entering from nothing
- **Signal:** `scale-zero`. `scale(0)`, `scale-0`, `scale: 0` as a starting state.
- **Fix:** Start from `scale(0.95)` with `opacity: 0`.
- **Source:** Reiss.

### Centre origin on an anchored layer
- **Signal:** `origin-center`. `transform-origin: center` or `origin-center`. Also check by eye any popover, menu, tooltip or select with no origin set, since centre is the default.
- **Fix:** Scale from the trigger, using the origin variable the component library exposes. Only a dialog with no trigger keeps the centre.
- **Source:** Reiss. "animations should be origin aware always".

### `transition: all`
- **Signal:** `transition-all`.
- **Fix:** Name the properties, normally `transform` and `opacity`.
- **Source:** Reiss (his own code has none).

### Looping and attention animation
- **Signal:** `loop`. `infinite`, `animate-pulse`, `animate-bounce`, `animate-ping`, `animate-spin` outside a loading state.
- **Fix:** Play once, or delete. A spinner on real loading stays.
- **Source:** Reiss. "dont loop any animations".

### Scroll snapping
- **Signal:** `scroll-snap`. `scroll-snap-type`, `snap-x`, `snap-y`, `snap-mandatory`.
- **Fix:** Delete.
- **Source:** Reiss. "none of the sliders need snapping".
