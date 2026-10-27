# gh-turn

A `gh` extension that sorts your open pull requests by whose move it actually is.

```
$ gh turn

Review requested of you (1)
  Cubemyth/cubemyth#101  not yet reviewed  Multiplayer Lag Story
  https://github.com/Cubemyth/cubemyth/pull/101

Your move (4)
  element-hq/element-web#35552     commented 2026-10-26  Set up or change the recovery key via a dialog
  https://github.com/element-hq/element-web/pull/35552
  matrix-org/matrix-rust-sdk#7328  commented 2026-10-26  feat(crypto): add `UserDevices::has_other_devices_to_veri...
  https://github.com/matrix-org/matrix-rust-sdk/pull/7328

Waiting on them (15)
  element-hq/element-web#35404  review required  Check the recovery key without leaving the logout dialog
  https://github.com/element-hq/element-web/pull/35404
```

## Why

GitHub's pull request dashboard groups PRs by review status, and two of those
groups are wrong in ways that cost you review round-trips:

- **A comment without a requested change never re-prompts you.** A reviewer
  leaves a question, the PR stays under "waiting for review or checks", and it
  sits there until you happen to look.
- **Re-requesting review doesn't reclassify.** You address the feedback, ask
  for another look, and the PR stays under "needs action" because the stale
  `CHANGES_REQUESTED` verdict persists in the search index.

Neither is expressible as a GitHub search query, so no query-driven dashboard
can fix them — `gh-dash` sections included. Both need the PR timeline from the
GraphQL API.

A third problem is just a wrong qualifier: `review-requested:@me` includes
requests routed to you via a team review queue. `gh turn` uses
`user-review-requested:@me`, which matches only requests aimed at you.

## Install

```sh
gh extension install kaylendog/gh-turn
```

## Usage

```sh
gh turn                      # grouped report
gh turn --state yours        # only what needs you, comma separated
gh turn --after 2026-01-01   # or an age: 6mo, 3w, 90d, 1y
gh turn --before 1mo         # the stale end: untouched for a month or more
gh turn --everyone           # approved only counts once every requested reviewer has looked
gh turn --tsv                # tab separated: bucket, ref, reason, title, url
gh turn --limit 100          # PRs to ask for per search (default 50)
gh turn --links never        # print URLs instead of clickable references
```

References are rendered as [OSC 8][osc8] hyperlinks when the output is a
terminal, which drops the URL line under each entry and roughly halves the
report. There is no dependable way to ask a terminal whether it supports them,
and the detection libraries work from an allowlist that misses several that
do — so this is a choice rather than a guess. Terminals that don't understand
the escape swallow it; if yours shows it instead, `--links never` restores the
URL lines.

[osc8]: https://gist.github.com/egmontkob/eb114294efbcd5adb1944c9f3cb5feda

`--after` defaults to **6mo**, so long-abandoned pull requests stay out of the
way. Pass `--after 2015-01-01` to see everything. Both date flags match on last
activity and are applied before any timeline is fetched, so narrowing the range
makes the command faster as well as shorter.

Pipe `--tsv` into whatever you like:

```sh
gh turn --tsv | grep ^yours | fzf | cut -f5 | xargs -r gh pr view --web
```

## Buckets

| Bucket | Meaning |
|---|---|
| `review` | Your review was requested directly and you haven't looked yet |
| `rereview` | You reviewed already; check whether it moved on |
| `yours` | Your move — a human commented after your last push, checks are red, or changes were requested with no re-request outstanding |
| `merge` | Approved, no reviewers outstanding, checks green |
| `theirs` | Waiting on someone else |
| `draft` | Your drafts |

Bots are filtered by GraphQL `__typename`, so codecov and codspeed commenting
on a PR never makes it look like your move.

## Known limits

- The head commit is dated by when it was *committed* rather than pushed, and
  is treated as *your* push whoever authored it, so work committed days before
  you push it reads as older than it is, and a reviewer applying a suggestion
  counts as your activity.
- Only the last 20 review threads per PR are read, so a fresh reply on a very
  old thread of a heavily-reviewed PR can be missed.
- Titles truncate by rune count, not display width, so CJK and emoji overshoot.

## Licence

MIT
