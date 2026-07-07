# Changesets

This repo uses [changesets](https://github.com/changesets/changesets) to track
skill changes and cut versioned releases.

When you change a skill in a way worth releasing, add a changeset:

```sh
npm run changeset
```

Answer the prompts (bump type + summary). This writes a markdown file here.
On merge to `main`, the release workflow versions the package (consuming the
pending changesets into `CHANGELOG.md`) and tags the release.

Skills are installed straight from the repo via `npx skills add reissui/skills`,
so a "release" is a tagged, changelogged snapshot rather than an npm publish.
