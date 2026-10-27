// Package classify decides whose move it is on a pull request.
//
// It exists because GitHub's search index cannot express either of the rules
// that matter most, so no query-driven dashboard gets them right:
//
//   - someone commented after your last push, without requesting changes, so
//     the PR still reads as "waiting for review" and never re-prompts you;
//   - you re-requested review after addressing feedback, but the stale
//     CHANGES_REQUESTED verdict persists and the PR stays under "needs action".
//
// Both need the PR timeline, which only the GraphQL API exposes.
package classify

import (
	"fmt"
	"strings"
	"time"

	"github.com/dustin/go-humanize"
)

type Bucket string

const (
	NeedsReview   Bucket = "review"   // your review was requested, you haven't looked
	NeedsReReview Bucket = "rereview" // you reviewed already, it moved on
	Yours         Bucket = "yours"    // your move
	Merge         Bucket = "merge"    // approved and green
	Theirs        Bucket = "theirs"   // waiting on someone else
	Draft         Bucket = "draft"
)

// Verdict is a bucket plus the reason it landed there, so the output can
// explain itself rather than just sorting.
type Verdict struct {
	Bucket Bucket
	Why    string
}

// Authored reports whose move it is on a PR you opened. With everyone set, an
// approved PR only reads as mergeable once every requested reviewer has looked.
func Authored(pr PullRequest, me string, everyone bool) Verdict {
	mine, theirs := activity(pr, me)
	if push := pr.lastPush(); push.After(mine) {
		mine = push
	}
	pending := pr.ReviewRequests.TotalCount
	ci := pr.checks()

	switch {
	case pr.IsDraft:
		return Verdict{Draft, "draft"}

	case !theirs.IsZero() && theirs.After(mine):
		return Verdict{Yours, "commented " + Ago(theirs)}

	case ci == "FAILURE" || ci == "ERROR":
		return Verdict{Yours, "checks failing"}

	// Only your move while no re-request is outstanding: asking for another
	// look hands the PR back to the reviewer even though GitHub keeps
	// reporting CHANGES_REQUESTED.
	case pr.ReviewDecision == "CHANGES_REQUESTED" && pending == 0:
		return Verdict{Yours, "changes requested"}

	// Outstanding requests don't block merging once GitHub says APPROVED;
	// team fan-out leaves one dangling on most PRs.
	case pr.ReviewDecision == "APPROVED" && (pending == 0 || !everyone) && (ci == "SUCCESS" || ci == "NONE"):
		return Verdict{Merge, "approved, checks green"}

	case pending > 0:
		return Verdict{Theirs, fmt.Sprintf("%d awaiting review", pending)}

	// Nobody is holding this up, CI just hasn't finished. Saying "approved"
	// under a "waiting on them" heading reads as a stuck review.
	case ci == "PENDING" || ci == "EXPECTED":
		return Verdict{Theirs, "checks running"}

	default:
		return Verdict{Theirs, humanise(pr.ReviewDecision)}
	}
}

// Requested reports what to do about a PR where your review was asked for.
// Team-queue requests are excluded upstream by the search query, so everything
// reaching here was aimed at you personally.
func Requested(pr PullRequest, me string) Verdict {
	var last time.Time
	for _, r := range pr.Reviews.Nodes {
		if r.Author == nil || r.Author.Login != me || r.SubmittedAt == nil {
			continue
		}
		if r.SubmittedAt.After(last) {
			last = *r.SubmittedAt
		}
	}
	if last.IsZero() {
		return Verdict{NeedsReview, "not yet reviewed"}
	}
	return Verdict{NeedsReReview, "you reviewed " + Ago(last)}
}

// recent is how long something stays worth phrasing as an age. Past it, a
// date is easier to place than "7 weeks ago".
const recent = 7 * 24 * time.Hour

// Ago renders when something happened, relative for the last week and as a
// plain date before that.
func Ago(t time.Time) string {
	return agoAt(t, time.Now())
}

// skew is how far ahead of us GitHub's clock may be before a timestamp is
// treated as genuinely in the future rather than as a difference in clocks.
const skew = time.Minute

func agoAt(t, now time.Time) string {
	if t.IsZero() {
		return "never"
	}
	if t.After(now) && t.Sub(now) < skew {
		t = now
	}
	if now.Sub(t) < recent && !t.After(now) {
		return humanize.RelTime(t, now, "ago", "from now")
	}
	return t.Format(time.DateOnly)
}

type event struct {
	who string
	at  time.Time
}

// events flattens every human remark on the PR: issue comments, submitted
// reviews, and replies on inline review threads.
func events(pr PullRequest) []event {
	var out []event
	add := func(a *Actor, at *time.Time) {
		if a == nil || at == nil || a.Login == "" || a.IsBot() {
			return
		}
		out = append(out, event{a.Login, *at})
	}

	for _, c := range pr.Comments.Nodes {
		add(c.Author, &c.CreatedAt)
	}
	for _, r := range pr.Reviews.Nodes {
		// An approval is not a prompt. Counting one would make every PR
		// approved after your last push look like your move, and the merge
		// verdict below would be unreachable in its most common case.
		if r.State == "APPROVED" || r.State == "DISMISSED" {
			continue
		}
		add(r.Author, r.SubmittedAt)
	}
	for _, t := range pr.ReviewThreads.Nodes {
		for _, c := range t.Comments.Nodes {
			add(c.Author, &c.CreatedAt)
		}
	}
	return out
}

// activity splits the timeline into your most recent remark and theirs.
func activity(pr PullRequest, me string) (mine, theirs time.Time) {
	for _, e := range events(pr) {
		latest := &theirs
		if e.who == me {
			latest = &mine
		}
		if e.at.After(*latest) {
			*latest = e.at
		}
	}
	return mine, theirs
}

// humanise turns REVIEW_REQUIRED into "review required".
func humanise(decision string) string {
	if decision == "" {
		decision = "REVIEW_REQUIRED"
	}
	return strings.ReplaceAll(strings.ToLower(decision), "_", " ")
}
