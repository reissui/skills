# Skills

My defaults for building interfaces, written down so an agent gets them right the first time.

Agents build the average of everything they have seen. These skills replace that average with how I work: minimal, monochrome first, small type, icons over labels, quiet motion, plain copy.

## Install

```bash
npx skills@latest add reissui/skills
```

## Reference

- **[design-engineer](skills/design-engineer/SKILL.md)** My default taste. Stack, type, icons, colour, spacing, surfaces, motion, gestures and copy, each as a decision with a value. Ships with a contrast checker. Load it before building any UI.
- **[remove-slop](skills/remove-slop/SKILL.md)** Strips what a model adds out of habit. Eyebrows, em dashes, bordered cards, dividers, badges, gradients, helper text. Ships with a scanner that finds the mechanical tells and a catalogue of fixes.

## Scanner

```bash
node skills/remove-slop/scan.mjs path/to/src
```

Prints `file:line` for every tell and a count per pattern. Exit code 1 when it finds anything.
