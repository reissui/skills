# Motion

Read this before writing any animation or transition. Answer the four questions in order. Most motion problems are a wrong answer to the first one.

## 1. Should it animate at all?

Decide by how often the person will see it.

| Seen | Decision |
| --- | --- |
| A hundred times a day or more (keyboard shortcuts, a command palette toggle) | No animation |
| Tens of times a day (hover effects, moving through a list) | Remove it or cut it to almost nothing |
| Occasionally (dialogs, drawers, toasts) | Standard animation |
| Rarely or once (onboarding, a confirmation, a celebration) | Room for something more |

**Anything triggered from the keyboard does not animate.** It is repeated all day, and motion makes it feel late.

## 2. What is it for?

Every animation has to answer "why does this move?" with one of these:

- **Origin.** It shows where the thing came from and where it goes back to.
- **State.** It shows that something changed (a button turning into its confirmed state).
- **Feedback.** It confirms the interface registered the input (a press).
- **Continuity.** It stops a change from being abrupt enough to look broken.
- **Explanation.** It shows how something works (marketing and onboarding only).

"It looks good" is not on the list. If that is the only reason and the person will see it often, do not animate.

## 3. Which curve?

| Situation | Curve |
| --- | --- |
| Entering or exiting | `--ease-out` |
| Already on screen, moving or morphing | `--ease-in-out` |
| Hover or colour change | `ease` |
| Constant motion (progress, a held action) | `linear` |
| Sheet or drawer | `--ease-drawer` |
| Anything else | `--ease-out` |

```css
--ease-out: cubic-bezier(0.23, 1, 0.32, 1);
--ease-in-out: cubic-bezier(0.77, 0, 0.175, 1);
--ease-drawer: cubic-bezier(0.32, 0.72, 0, 1);
```

- **Use these curves, not the CSS keywords.** The built-in `ease-out` and `ease-in-out` are too gentle to read as intentional.
- **Never `ease-in` on interface motion.** It starts slowly, and the start is the moment the person is watching. The same duration feels slower.

## 4. How long?

| Element | Duration |
| --- | --- |
| Press feedback | 100 to 160ms (`--dur-press`, 140ms) |
| Tooltip, small popover | 125 to 200ms |
| Dropdown, select, menu | 150 to 250ms (`--dur-ui`, 220ms) |
| Dialog, drawer, sheet | 200 to 500ms |
| Layout move | `--dur-move`, 420ms |
| One-off reveal on a marketing page | `--dur-reveal`, 700ms |

- Interface motion stays under 300ms. A 180ms menu feels more responsive than a 400ms one.
- **Exits are faster than entrances.** The person has already decided. Get out of the way.
- Slow where the person is deciding, fast where the system is responding. A hold-to-confirm fills over 2s linear and snaps back in 200ms.
- A spinner that turns quickly makes the same wait feel shorter.
- One component, one timing family. If a drawer opens in 200ms, the content inside it changes in the same range. One slow part makes the whole thing feel broken.

## Origin

Everything animates from where it came from. This is the rule that most separates considered motion from default motion.

- **Anchored layers** (popover, menu, select, tooltip, hover card) scale from the edge that touches the trigger. Radix exposes it as `var(--radix-popover-content-transform-origin)` (and the matching variable per component). Base UI exposes `var(--transform-origin)`. Use the variable. Do not hard-code `top left`, because the layer flips when it runs out of room.
- **Edge layers** (drawer, sheet, toast, side panel) exit along the same axis they entered. A toast that rose from the bottom leaves downwards, which is also the direction a swipe dismisses it.
- **Dialogs** opened from a control grow from that control. Set `transform-origin` from the trigger's position. Only a dialog with no trigger (a session timeout, a route-level modal) scales from the centre.
- **Shared elements** (a thumbnail that becomes a detail view, a tab indicator, a selected-row highlight) move and resize into the new position instead of fading out and in. Use a layout animation or the View Transitions API.
- **Nothing comes from nowhere.** No `scale(0)`. Enter from `scale(0.95)` or higher with `opacity: 0`. Tooltips from `scale(0.97)`.
- Check the origin in slow motion. A wrong `transform-origin` is invisible at full speed and obvious at a quarter speed.

## Interruptibility

- **CSS transitions, not keyframes, for anything the person can trigger again.** A transition retargets from where it is. A keyframe restarts from zero and jumps.
- Springs keep their velocity when retargeted, so they are the right tool for anything that can reverse mid-flight.
- Enter states without JavaScript:

```css
.toast {
  opacity: 1;
  transform: translateY(0);
  transition: opacity 220ms var(--ease-out), transform 220ms var(--ease-out);

  @starting-style {
    opacity: 0;
    transform: translateY(100%);
  }
}
```

- Never block input while something animates, including during a stagger.

## Springs

- Use a spring for drag and release, for anything that can be interrupted, and for decorative motion that follows the pointer.
- Describe springs by duration and bounce: `{ type: "spring", duration: 0.25, bounce: 0 }` for hover and small responses, `duration: 0.4` to `0.55` with `bounce: 0.1` for larger moves.
- **Bounce is 0 by default.** A small bounce (0.1 to 0.3) belongs only at the end of a drag, where the person applied force. The same element closed by a button gets none.
- A value tied directly to the pointer position feels mechanical. Run it through a spring so it has momentum. Only do this for decoration. A functional readout shows the real value at once.
- A spring that feels wrong usually needs more damping.

## Component rules

- **Press.** Every pressable element scales to `0.97` on `:active` over 140ms. Subtle is 0.95 to 0.98.
- **Tooltips.** Delay the first one. Once one is open, its neighbours open instantly with `transition-duration: 0ms`.
- **Crossfades.** When two states overlap during a fade and it looks like two objects swapping, add `filter: blur(2px)` for the duration of the change. Keep blur small and local. It is expensive, most of all in Safari.
- **Hover exits crossfade.** A state must never visibly snap back.
- **Position by own size.** `translateY(100%)` moves an element by its own height whatever that height is. Prefer percentages to pixel offsets for hide and reveal.
- **Reveals and wipes.** `clip-path: inset()` animates on the compositor and needs no extra elements. Use it for image reveals, comparison sliders and hold-to-confirm fills.
- **Tabs with a moving highlight.** Duplicate the tab list in the active style, clip the copy to the active tab, and animate the clip. Text colour changes in step with the highlight, which separate colour transitions cannot do.
- **List items entering and leaving.** Height and opacity have to work together. Tune them by eye until neither leads the other.
- **Stagger.** 30 to 50ms a step, a handful of items at most, or none. A long cascade reads as slow.

## Performance

- Animate `transform`, `opacity` and `clip-path`. Animating `width`, `height`, `margin`, `padding` or `top` forces layout on every frame.
- Do not move things by updating a CSS variable on a parent every frame. Variables inherit, so every descendant recalculates. Write `transform` on the element itself.
- No React state update per frame. Use motion values or direct style writes.
- Predetermined motion runs in CSS, off the main thread, and stays smooth while the page is busy. JavaScript-driven motion drops frames under load. Use it for what CSS cannot do: springs, gestures, values that depend on input.
- The Web Animations API gives programmatic control at CSS cost when a library is not warranted.
- `will-change` only on an element that is about to animate, and removed afterwards.

## Reduced motion

Reduced motion means gentler, not none.

- Keep opacity and colour changes that help the person follow what happened.
- Remove movement, scale, parallax and anything that travels.
- Direct manipulation stays, because the person is producing that motion themselves.

```css
@media (prefers-reduced-motion: reduce) {
  .panel { transition: opacity 220ms ease; transform: none; }
}
```

## Checking the work

- Play it at a quarter speed, or step through it frame by frame in the browser's animation inspector. Look for two states visibly overlapping, properties out of sync, and a wrong origin.
- Trigger it twice quickly. It should retarget, not restart.
- Look again the next day. Timing problems hide from tired eyes.
- Match the motion to the product. A work tool is crisp. Something playful can carry a little more.
