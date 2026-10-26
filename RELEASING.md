# Releasing

Releases are cut by pushing a tag. Nothing else is needed.

```sh
git tag v1.2.3
git push origin v1.2.3
```

The `release` workflow then:

1. Builds `gh-turn` for darwin, linux, freebsd and windows on every
   architecture `cli/gh-extension-precompile` supports (12 binaries).
2. Attaches a build provenance attestation to each binary.
3. Creates a GitHub release titled `gh-turn 1.2.3` with auto-generated
   notes from the merged pull requests since the previous tag.

Users pick it up with `gh extension upgrade turn`.

## Rules

- Tags must start with `v`; anything else does not trigger the workflow.
- A tag containing a hyphen (`v1.3.0-rc1`) is published as a pre-release,
  which `gh extension upgrade` skips.
- Tag from `main` once `ci` is green. The release build runs no tests.
- Re-pushing a tag re-uploads over the existing release assets, so a bad
  release can be fixed by deleting the tag, re-tagging and pushing again.
  Prefer a new patch version where the old one may already be installed.

## Verifying a binary

```sh
gh attestation verify ~/.local/share/gh/extensions/gh-turn/gh-turn --repo kaylendog/gh-turn
```
