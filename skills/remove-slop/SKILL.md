---
name: remove-slop
description: Finds and removes the tells that make an interface or its copy look AI generated. Eyebrows, em dashes, bordered cards, dividers, badges, gradients, helper text, full caps and the rest. Use after any model has built or edited UI, before handing work over, or when asked to clean up, de-slop, simplify or strip a design back.
---

# Remove slop

Slop is everything a model adds because it has seen it a thousand times, not because this interface needs it. The job here is subtraction. Nothing gets redesigned and nothing new gets added.

This skill and `design-engineer` hold the same standard from opposite ends. That one says how to build it. This one removes whatever falls short of it. If `design-engineer` is installed, anything it forbids is slop here, whether or not it appears in the lists below. This skill works on its own without it.

## How to run it

1. **Scan.** From this skill's directory, run `node scan.mjs <path>` on the files or folders that changed. It prints `file:line` for every mechanical tell and a count per pattern. No Node available: use the grep signals in [CATALOG.md](CATALOG.md).
2. **Read.** The scanner cannot see judgment slop (helper text, repeated selling points, a section that exists for no reason). Read every changed screen against the judgment list below.
3. **Remove.** Apply the fix from the catalogue for each hit. Delete first. Only replace when the deletion leaves a hole.
4. **Scan again.** Report the count before and after, and list anything you left in place with the reason.

Scanner hits are candidates, not verdicts. A project's own design system can allow something this skill flags (a brand gradient, a real status badge). If the design system or Reiss put it there, it stays. Say so in the report.

## The rules of removal

- **Take things away. Do not swap one decoration for another.** Removing an eyebrow does not mean adding an icon.
- **Keep the content, lose the wrapper.** A card becomes its contents on the page. A badge becomes plain text, or nothing.
- **Do not restyle what is not slop.** Leave layout, tokens and working components alone.
- **Do not touch behaviour.** No changes to logic, data or routes.
- **Check the gap you leave.** After removing a divider or container, fix the spacing so the grouping still reads.

## Mechanical tells (the scanner finds these)

Full entries with detection signals and fixes are in [CATALOG.md](CATALOG.md).

| Tell | Fix in one line |
| --- | --- |
| Em dash | Full stop, comma or brackets |
| Eyebrow above a heading | Delete it. The heading is the title. |
| Full caps | Sentence case |
| Title Case Headings | Sentence case |
| Numbered section labels ("01") | Delete the number |
| Card stack (rounded box with a border or shadow) | Remove the container, or keep a plain fill |
| Accent stripe on one side of a box | Delete the stripe |
| Dividers and horizontal rules | Delete, then separate with space |
| Decorative badge, tag or pill | Delete, or plain text |
| Check and sparkle bullets | Plain list or prose |
| Emoji in interface copy | Delete |
| Arrow on a button or link | Delete the arrow |
| Gradient, gradient text | Solid fill, solid text |
| Glow, grain, shimmer, blur blobs | Delete |
| Shadow | Delete, or the 1px ring for floating layers |
| Bold weights (600 and above) | Weight 500 at most, with colour for emphasis |
| Text at 10px or below | 11px or larger |
| `ease-in` | A strong ease-out curve |
| Entering from `scale(0)` | `scale(0.95)` with opacity |
| Centre origin on an anchored layer | Scale from the trigger |
| Pure black | Near-black foreground token |
| Default indigo and violet | The project's foreground or its one accent |
| `transition: all` | Name the properties |
| Looping and attention animation | Play once, or delete |
| Scroll snapping | Delete |
| Stock marketing phrases | Say the plain thing |

## Judgment tells (read for these)

- **Helper text.** Section descriptions, subtitles, captions, hints under inputs, placeholder sentences, empty-state paragraphs, breadcrumbs on a shallow site. Delete it. If the context matters, move it into a tooltip on the thing it explains, cut to a few words. What stays on screen: an error, a required format, the consequence of a destructive action, and the one sentence under a marketing hero that says what the product is.
- **Icon and label saying the same thing.** Keep one. If the icon is unmistakable (close, search, add, edit, delete, settings, more), keep the icon and give it an `aria-label` and a tooltip. If it is not, keep the word.
- **Text that fails contrast.** Light grey on white, grey on a tinted surface, coloured text on a coloured fill. Body text needs 4.5:1, large text and control edges 3:1. Replace with the nearest token that passes. If `design-engineer` is installed, check with its `contrast.mjs`.
- **Colour as the only signal.** An error shown only in red, a selected item shown only by tint. Add a word, an icon or a weight.
- **Over-explaining.** Copy that describes what the person can already see ("Below you will find our pricing options").
- **Repetition.** The same selling point in the hero, the features and the footer. Keep the best one.
- **Too much in one place.** A panel carrying every fact at once. Cut to what the person needs for the decision in front of them.
- **Filler sections.** A stats row with invented numbers, a logo wall with no logos, an FAQ nobody asked, a closing call to action section that repeats the hero. A button in the nav plus one in the hero is fine.
- **Tinted containers.** A grey or coloured background behind a block that needs no grouping. A surface is earned only by a separate object the person acts on as a unit (a plan, a product, a media item). A section of text never needs one.
- **Icon in a rounded square above every feature title.** Delete the icon tile.
- **Three identical cards in a row** because three felt balanced, when the content is two things or five.
- **Centred everything.** A narrow centred column wasting a wide screen.
- **Invented components.** UI for features nobody asked for (a floating widget, a theme toggle, a notification bell).
- **Style drift.** A new font size, radius or grey that is close to an existing token but not it. Snap it to the token.
- **Third-party defaults showing.** Stock shadcn styling, vendor branding, the browser's blue focus ring, a stock scrollbar in a custom panel.
- **Mistakes.** Overflowing text, misaligned baselines, uneven padding, an orphaned word in a heading, a hover state that sticks on touch. Fix these on sight.

## Report format

```
Slop removed: 41 → 3

| Tell | Before | After | Left in place |
| --- | --- | --- | --- |
| Em dash | 12 | 0 | |
| Card stack | 6 | 0 | |
| Shadow | 5 | 1 | Dropdown menu keeps the 1px ring |
| Gradient | 2 | 2 | Brand gradient from the design system |

Judgment removals:
- Deleted the description under each section heading (6)
- Merged two calls to action into one
```
