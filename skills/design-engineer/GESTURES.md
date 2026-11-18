# Gestures

Read this before building anything that is dragged, swiped, flung or held.

A gesture is direct manipulation. While the finger is down the element is the finger. When it lifts, physics finishes the move. Every rule here follows from that split.

## Build it or use what exists

A drawer done properly needs momentum, an overlay tied to drag progress, velocity-aware dismissal, focus trapping and Escape to close. For a bottom sheet or drawer, use Vaul (shadcn's Drawer). For drag and drop, sortable lists and similar, use UI SDK behaviours. Write a gesture by hand only when nothing covers the interaction.

## The two phases

| Pointer | Phase | Rule |
| --- | --- | --- |
| Down | Track | Position follows the pointer 1:1. No easing, no duration, no spring. |
| Up | Release | A spring finishes the move, starting at the speed of the release. Never a keyframe. |

Mixing the two is the usual reason a gesture feels wrong.

### Track

- **1:1 means 1:1.** Drive position from a motion value or a direct `transform` write. A spring or transition while tracking makes the element trail the finger.
- Write `transform` on the dragged element itself. Do not move it through a CSS variable on a parent, and do not set React state on every move event.
- Capture the pointer when the drag starts, so it continues when the pointer leaves the element.
- Ignore any extra touch that lands after the drag has begun. Without this, a second finger makes the element jump.
- **Resist at the edges, do not stop.** Past a boundary the element moves a fraction of the distance, and less the further it goes (`dragElastic` around 0.2). A hard stop feels like a wall.
- Set `touch-action` for the axis you own (`pan-x` on a vertical drag) so the browser does not scroll against the gesture.
- Mark interactive children as not draggable, so pressing a button inside a sheet does not start a drag.

### Release

- **Carry the velocity.** The element leaves the finger at the speed it was moving. A fixed-duration ease makes every fling land the same and reads as fake.
- **Dismiss on distance or velocity.** Passing 40 to 50% of the travel dismisses. So does a quick flick over a short distance. Hand-rolled, measure distance over elapsed time and dismiss above about 0.11px per millisecond. In Motion, dismiss when `info.velocity` passes about 500.
- **Snap back fast.** A gesture that falls short returns quickly. A slow return feels like the interface arguing.
- A little bounce is allowed here and only here, because force was applied.

```jsx
<motion.div
  drag="y"
  dragConstraints={{ top: 0 }}
  dragElastic={0.2}
  onDragEnd={(event, info) => {
    if (info.offset.y > threshold || info.velocity.y > 500) dismiss();
  }}
/>
```

`dragMomentum` is on by default. Right for a flung card, wrong for a simple reposition.

## Always interruptible

The person must be able to catch a sheet while it is closing and pull it back. Springs and CSS transitions allow that. Keyframes do not. **Never put a keyframe animation on anything a gesture can touch.**

## Linked values

The dragged element is rarely alone: an overlay fades, the page behind scales. Derive those from the drag, do not animate them separately.

```jsx
const y = useMotionValue(0);
const overlayOpacity = useTransform(y, [0, height], [1, 0]);
```

One source of truth keeps the overlay exactly as faded as the sheet is dismissed, including when the person reverses.

## Direction

**A thing is dismissed in the direction it entered.** A sheet that rose from the bottom is swiped down. A panel from the right is swiped right. A gesture that reverses the entrance needs no instruction.

## Sheets

- A full-width sheet attached to the screen edge: `transform` over 500ms with `--ease-drawer`. The curve is steep at the start, which keeps 500ms feeling quick.
- A small floating drawer that does not touch the edge: about 200ms.
- Height changes and content crossfades inside the sheet use the same timing family.

## Holds

Press and hold (hold to delete, hold to confirm) is asymmetric. The fill runs `linear`, because it shows time passing and time is linear. Letting go early snaps back in about 200ms with `--ease-out`.

## Touch

- Hit targets are at least 44 by 44px. Grow a small target with an invisible pseudo-element, not by changing the layout.
- Hover effects sit behind `@media (hover: hover) and (pointer: fine)`. Touch fires phantom hovers.
- Every gesture has a non-gesture equivalent: a close button, an Escape key, a menu item. A swipe is a shortcut, never the only way.
- Test on a real phone over the local network. A trackpad lies about touch, and a 60Hz monitor hides roughness a 120Hz phone shows.

## Reduced motion

Tracking stays, because the person is making that motion. Flings, bounce and decorative motion around the gesture go. On release, settle with a short fade or a small move.
