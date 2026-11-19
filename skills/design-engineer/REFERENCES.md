# References

Sites Reiss has said he likes. A reference calibrates judgement. It does not override a rule in [SKILL.md](SKILL.md).

How to use an entry:

1. **Take** rows are safe to apply. They add a pattern the house rules allow and do not already cover.
2. **Confirms** rows need no action. They show a house rule holding up on a site Reiss likes.
3. **Differs** rows lose to the house rule until Reiss picks the reference's value. Never apply one on your own. When one would change the work in front of you, say so and ask.

Every value below was read from the site's shipped CSS, not from its marketing copy or a screenshot. Re-measure before relying on an entry older than six months.

## design.how

Reiss, 2026-11-19: "i also like this for taste". He gave no reason and named no part of the site, so nothing here carries a why from him.

Measured 2026-11-19 from https://design.how/ and its two stylesheets, with pages rendered at 1440px and 390px. Source code is at https://github.com/arla6ka/design.how (MIT).

What the site is: a docs and gallery site in one narrow centred column, one sans face in two weights, a flat grey button, no borders, no shadows, and almost no words.

### Take

| Pattern | Measured on design.how | Apply with house tokens as |
| --- | --- | --- |
| Named row | A list row is the name on the left in the text colour and one short clause on the right in the muted colour, on one line, with no divider and no icon | Name in `--fg`, clause in `--muted`, 13px, space between. The clause is content the row needs, not helper text. Drop it if the name is enough. |
| One moving nav indicator | The nav has one highlight element (`.nav-pill`) that changes `translate` and `width` to sit behind the current item. Items have no background of their own. | Follow "Shared elements" and "Tabs with a moving highlight" in [MOTION.md](MOTION.md). One indicator, never a background per item. |
| Fixed scale, no fluid type | Every size is a fixed px token. There is no `clamp()` on type anywhere. | Already the fallback in Open defaults. This is one more site for it. |
| Token per job | Sizes are named for the job (`--size-control`, `--size-touch`, `--size-icon`, `--type-caption-size`), not for a step (`--text-sm`) | Name a new token for what it is used for. |

### Confirms

| House rule | design.how |
| --- | --- |
| Two weights, 400 and 500 | 26 `font-weight` declarations. 20 are 500, 6 are 400, none higher. |
| Focus is `2px solid currentColor`, offset 3px | `--focus-width: 2px`, `--focus-offset: 3px`, `--color-focus` equals the text colour |
| Icons are 16px | `--size-icon: 16px` |
| Touch target 44px, touch inputs 16px | `--size-touch: 44px`, `--type-input-touch-size: 16px` |
| Spacing scale | 4, 8, 12, 16, 24, 32, 48, 96 are all tokens. It also has 2, 6 and 40, which the house scale does not. |
| Shadows are rare | One `box-shadow` on the whole site, an inset 1px edge on colour swatches |
| No `transition: all` | Every transition names its properties |
| Near black, never `#000` | Text is `#161514`, dark background is `#0e0e0d` |
| Root and wrapping | `-webkit-font-smoothing: antialiased`, `text-wrap: balance` and `pretty`, `tabular-nums`, `scrollbar-gutter: stable`, styled `::selection`, transparent tap highlight |
| Sentence case, no eyebrows, no extra text | The home page is one heading, one sentence and two buttons |

### Differs

The house rule wins every row until Reiss says otherwise.

| Subject | design.how | House rule | Note |
| --- | --- | --- | --- |
| Interface size | 14px text, 15px controls and nav, 13px captions | 13px for all of it | |
| Page title | 21px, weight 500, `-0.01em` | 16px, weight 500 | |
| Face | Its own face, "mori" | Inter Variable | A reference's font is never imported |
| Button shape | 6px radius, 4px on rows | Pill, `999px` | Its own slop list bans pills on static tags because they read as toggles |
| Button fill | Flat grey (`#f6f6f6`), weight 400, same for both hero actions | Accent equals foreground | |
| Control height | 36px | 32px, open | |
| Hover | `background-color` or `color` step over 100ms `ease-out` | Opacity to 0.85 over 220ms, open. Sibling dimming in lists. | |
| Content column | 576px, centred, with 840px for a hero visual and 1008px for wide pages | Reading column 620 to 640px, and "fill the horizontal space" | The biggest gap. The site is a narrow column on a wide screen, which the house rule calls a mistake. |
| Sticky edges | Content fades out under the header and footer through a gradient from the background colour to transparent, with no line and no blur | No gradients. Blur on the sticky nav. | |
| Card | `#f7f7f7` fill with a 1px `#f0f0f0` border, 12px radius | Fill only, never fill plus border, 16px radius | |
| Text link | Underline 0.5px thick, offset 3px, in the text colour at 22% | No link rule yet | The underline flattens to about 1.6:1 on white, so it is a weak cue on its own |
| Motion length | Nav indicator 320ms, hero entrances to 600ms with an overshooting spring | Interface motion under 300ms | |
| Enter and exit curves | Three exits use `ease-in` | Never `ease-in` | |

### Do not take

These fail the house accessibility floor. Checked with `node contrast.mjs`.

| Pair | Ratio | Floor |
| --- | --- | --- |
| Muted icon `#9f9f9f` on `#fff` | 2.65:1 | 3:1 |
| Control border `#ececeb` on `#fff` | 1.18:1 | 3:1 |
| Dark muted icon `#767573` on `#0e0e0d` | 4.20:1 | Passes for an icon, fails if reused for text |
| Muted text `#71706f` on the button fill `#f6f6f6` | 4.57:1 | Passes at 4.5:1 with no margin, and the house rule asks for margin on small type |
