package classify

import (
	"testing"
	"time"
)

const me = "kaylendog"

func ts(t *testing.T, s string) time.Time {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatalf("bad fixture timestamp %q: %v", s, err)
	}
	return parsed
}

// remark is one timeline entry: who said something, when, whether they are a
// bot, and for reviews what verdict they left.
type remark struct {
	who   string
	at    string
	bot   bool
	state string // reviews only; defaults to COMMENTED
}

func (r remark) actor() *Actor {
	kind := "User"
	if r.bot {
		kind = "Bot"
	}
	return &Actor{Login: r.who, TypeName: kind}
}

type fixture struct {
	draft    bool
	decision string
	pending  int
	pushed   string
	ci       string // "" means no checks at all
	comments []remark
	reviews  []remark
	threads  []remark
}

func (f fixture) build(t *testing.T) PullRequest {
	t.Helper()
	var pr PullRequest
	pr.Number = 1
	pr.Title = "fixture"
	pr.Repository.NameWithOwner = "element-hq/element-web"
	pr.IsDraft = f.draft
	pr.ReviewDecision = f.decision
	pr.ReviewRequests.TotalCount = f.pending

	if f.pushed != "" {
		node := CommitNode{Commit: Commit{CommittedDate: ts(t, f.pushed)}}
		if f.ci != "" {
			node.Commit.StatusCheckRollup = &Rollup{State: f.ci}
		}
		pr.Commits.Nodes = []CommitNode{node}
	}

	for _, c := range f.comments {
		pr.Comments.Nodes = append(pr.Comments.Nodes,
			Comment{Author: c.actor(), CreatedAt: ts(t, c.at)})
	}
	for _, r := range f.reviews {
		at := ts(t, r.at)
		state := r.state
		if state == "" {
			state = "COMMENTED"
		}
		pr.Reviews.Nodes = append(pr.Reviews.Nodes,
			Review{Author: r.actor(), State: state, SubmittedAt: &at})
	}
	for _, r := range f.threads {
		var thread ReviewThread
		thread.Comments.Nodes = []Comment{{Author: r.actor(), CreatedAt: ts(t, r.at)}}
		pr.ReviewThreads.Nodes = append(pr.ReviewThreads.Nodes, thread)
	}
	return pr
}

func TestAuthored(t *testing.T) {
	cases := []struct {
		name string
		pr   fixture
		want Bucket
	}{{
		name: "bot comment after my push is not my move",
		pr: fixture{
			decision: "REVIEW_REQUIRED", pending: 1,
			pushed: "2026-10-20T00:00:00Z", ci: "SUCCESS",
			comments: []remark{{who: "codecov", at: "2026-10-25T00:00:00Z", bot: true}},
		},
		want: Theirs,
	}, {
		name: "app-suffixed login counts as a bot",
		pr: fixture{
			decision: "REVIEW_REQUIRED", pending: 1,
			pushed: "2026-10-20T00:00:00Z", ci: "SUCCESS",
			comments: []remark{{who: "dependabot[bot]", at: "2026-10-25T00:00:00Z"}},
		},
		want: Theirs,
	}, {
		name: "human comment after my push is my move",
		pr: fixture{
			decision: "APPROVED", pending: 0,
			pushed: "2026-10-20T00:00:00Z", ci: "SUCCESS",
			comments: []remark{{who: "jmartinesp", at: "2026-10-25T00:00:00Z"}},
		},
		want: Yours,
	}, {
		name: "inline thread reply after my push is my move",
		pr: fixture{
			decision: "REVIEW_REQUIRED", pending: 0,
			pushed: "2026-10-20T00:00:00Z", ci: "SUCCESS",
			threads: []remark{{who: "uhoreg", at: "2026-10-25T00:00:00Z"}},
		},
		want: Yours,
	}, {
		name: "my own comment does not summon me",
		pr: fixture{
			decision: "REVIEW_REQUIRED", pending: 1,
			pushed: "2026-10-20T00:00:00Z", ci: "SUCCESS",
			comments: []remark{{who: me, at: "2026-10-25T00:00:00Z"}},
		},
		want: Theirs,
	}, {
		name: "replying after their comment hands it back",
		pr: fixture{
			decision: "REVIEW_REQUIRED", pending: 1,
			pushed: "2026-10-20T00:00:00Z", ci: "SUCCESS",
			comments: []remark{
				{who: "uhoreg", at: "2026-10-24T00:00:00Z"},
				{who: me, at: "2026-10-25T00:00:00Z"},
			},
		},
		want: Theirs,
	}, {
		name: "pushing after their comment hands it back",
		pr: fixture{
			decision: "REVIEW_REQUIRED", pending: 1,
			pushed: "2026-10-26T00:00:00Z", ci: "SUCCESS",
			comments: []remark{{who: "uhoreg", at: "2026-10-25T00:00:00Z"}},
		},
		want: Theirs,
	}, {
		name: "changes requested and not re-requested is my move",
		pr: fixture{
			decision: "CHANGES_REQUESTED", pending: 0,
			pushed: "2026-10-25T00:00:00Z", ci: "SUCCESS",
		},
		want: Yours,
	}, {
		name: "changes requested but review re-requested waits on them",
		pr: fixture{
			decision: "CHANGES_REQUESTED", pending: 2,
			pushed: "2026-10-25T00:00:00Z", ci: "SUCCESS",
		},
		want: Theirs,
	}, {
		name: "failing checks are my move",
		pr: fixture{
			decision: "REVIEW_REQUIRED", pending: 1,
			pushed: "2026-10-25T00:00:00Z", ci: "FAILURE",
		},
		want: Yours,
	}, {
		name: "approved and green is ready to merge",
		pr: fixture{
			decision: "APPROVED", pending: 0,
			pushed: "2026-10-25T00:00:00Z", ci: "SUCCESS",
		},
		want: Merge,
	}, {
		name: "approved with no checks at all is ready to merge",
		pr: fixture{
			decision: "APPROVED", pending: 0,
			pushed: "2026-10-25T00:00:00Z",
		},
		want: Merge,
	}, {
		name: "approved but still awaiting another reviewer",
		pr: fixture{
			decision: "APPROVED", pending: 1,
			pushed: "2026-10-25T00:00:00Z", ci: "SUCCESS",
		},
		want: Theirs,
	}, {
		// An approval arriving after your last push is the usual way a PR
		// becomes mergeable; counting it as a remark hid the merge bucket.
		name: "approval after my push is still ready to merge",
		pr: fixture{
			decision: "APPROVED", pending: 0,
			pushed: "2026-10-20T00:00:00Z", ci: "SUCCESS",
			reviews: []remark{{who: "uhoreg", at: "2026-10-25T00:00:00Z", state: "APPROVED"}},
		},
		want: Merge,
	}, {
		name: "a commenting review after my push is my move",
		pr: fixture{
			decision: "REVIEW_REQUIRED", pending: 0,
			pushed: "2026-10-20T00:00:00Z", ci: "SUCCESS",
			reviews: []remark{{who: "uhoreg", at: "2026-10-25T00:00:00Z", state: "COMMENTED"}},
		},
		want: Yours,
	}, {
		name: "a changes-requested review after my push is my move",
		pr: fixture{
			decision: "CHANGES_REQUESTED", pending: 0,
			pushed: "2026-10-20T00:00:00Z", ci: "SUCCESS",
			reviews: []remark{{who: "uhoreg", at: "2026-10-25T00:00:00Z", state: "CHANGES_REQUESTED"}},
		},
		want: Yours,
	}, {
		name: "a dismissed review is not a remark",
		pr: fixture{
			decision: "APPROVED", pending: 0,
			pushed: "2026-10-20T00:00:00Z", ci: "SUCCESS",
			reviews: []remark{{who: "uhoreg", at: "2026-10-25T00:00:00Z", state: "DISMISSED"}},
		},
		want: Merge,
	}, {
		name: "draft outranks everything",
		pr: fixture{
			draft: true, decision: "CHANGES_REQUESTED", pending: 0,
			pushed: "2026-10-20T00:00:00Z", ci: "FAILURE",
			comments: []remark{{who: "uhoreg", at: "2026-10-25T00:00:00Z"}},
		},
		want: Draft,
	}, {
		name: "empty PR does not panic",
		pr:   fixture{},
		want: Theirs,
	}}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Authored(tc.pr.build(t), me)
			if got.Bucket != tc.want {
				t.Errorf("bucket = %q (%s), want %q", got.Bucket, got.Why, tc.want)
			}
			if got.Why == "" {
				t.Error("verdict has no reason attached")
			}
		})
	}
}

// Approved with CI still running is waiting on the robots, and saying
// "approved" under a "waiting on them" heading reads as a stuck review.
func TestChecksRunningIsExplained(t *testing.T) {
	pr := fixture{
		decision: "APPROVED", pending: 0,
		pushed: "2026-10-25T00:00:00Z", ci: "PENDING",
	}.build(t)

	got := Authored(pr, me)
	if got.Bucket != Theirs || got.Why != "checks running" {
		t.Errorf("verdict = %+v, want {theirs checks running}", got)
	}
}

func TestRequested(t *testing.T) {
	cases := []struct {
		name string
		pr   fixture
		want Bucket
	}{{
		name: "never reviewed by me",
		pr:   fixture{reviews: []remark{{who: "uhoreg", at: "2026-10-25T00:00:00Z"}}},
		want: NeedsReview,
	}, {
		name: "no reviews at all",
		pr:   fixture{},
		want: NeedsReview,
	}, {
		name: "I reviewed, so it is a re-review",
		pr:   fixture{reviews: []remark{{who: me, at: "2026-10-25T00:00:00Z"}}},
		want: NeedsReReview,
	}}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Requested(tc.pr.build(t), me); got.Bucket != tc.want {
				t.Errorf("bucket = %q (%s), want %q", got.Bucket, got.Why, tc.want)
			}
		})
	}
}

// A pending review carries a null submittedAt, which must not be read as an
// zero-time review by me.
func TestPendingReviewIgnored(t *testing.T) {
	var pr PullRequest
	pr.Reviews.Nodes = []Review{{Author: &Actor{Login: me, TypeName: "User"}}}

	if got := Requested(pr, me); got.Bucket != NeedsReview {
		t.Errorf("bucket = %q, want %q", got.Bucket, NeedsReview)
	}
}

func TestHumanise(t *testing.T) {
	cases := map[string]string{
		"REVIEW_REQUIRED":   "review required",
		"CHANGES_REQUESTED": "changes requested",
		"":                  "review required",
	}
	for in, want := range cases {
		if got := humanise(in); got != want {
			t.Errorf("humanise(%q) = %q, want %q", in, got, want)
		}
	}
}
