# Releasing

1. Check `ci` is green on `main` — the release build runs no tests.
2. Tag: `git tag v1.2.3`. The `v` is required; a hyphen (`v1.3.0-rc1`)
   makes it a pre-release.
3. Push: `git push origin v1.2.3`. Nothing else is needed.
4. Verify: `gh release view v1.2.3`, and once it lands,
   `gh extension upgrade turn && gh turn --version`.

The `release` workflow then:

1. Builds `gh-turn` for darwin, linux, freebsd and windows on every
   architecture `cli/gh-extension-precompile` supports (12 binaries).
2. Attaches a build provenance attestation to each binary.
3. Creates a GitHub release titled `gh-turn 1.2.3` with auto-generated
   notes from the merged pull requests since the previous tag.

Users pick it up with `gh extension upgrade turn`.

## Rules

- `gh extension upgrade` skips pre-releases.
- Re-pushing a tag re-uploads over the existing release assets, so a bad
  release can be fixed by deleting the tag, re-tagging and pushing again.
  Prefer a new patch version where the old one may already be installed.

## Verifying a binary

```sh
gh attestation verify ~/.local/share/gh/extensions/gh-turn/gh-turn --repo kaylendog/gh-turn
```
