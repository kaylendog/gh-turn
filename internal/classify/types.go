package classify

import (
	"fmt"
	"strings"
	"time"
)

// Actor is the author of a comment, review or thread reply. Nil when the
// account has since been deleted.
type Actor struct {
	Login    string `json:"login"`
	TypeName string `json:"__typename"`
}

// IsBot reports whether the actor is an app rather than a person. Without
// this, codecov and codspeed make every PR look like it needs you.
func (a *Actor) IsBot() bool {
	return a.TypeName == "Bot" || strings.HasSuffix(a.Login, "[bot]")
}

type Comment struct {
	Author    *Actor    `json:"author"`
	CreatedAt time.Time `json:"createdAt"`
}

// Review carries a nil SubmittedAt while it is still pending.
type Review struct {
	Author      *Actor     `json:"author"`
	State       string     `json:"state"`
	SubmittedAt *time.Time `json:"submittedAt"`
}

type ReviewThread struct {
	Comments struct {
		Nodes []Comment `json:"nodes"`
	} `json:"comments"`
}

// Rollup is the aggregate state of a commit's checks, nil when it has none.
type Rollup struct {
	State string `json:"state"`
}

type Commit struct {
	CommittedDate     time.Time `json:"committedDate"`
	StatusCheckRollup *Rollup   `json:"statusCheckRollup"`
}

type CommitNode struct {
	Commit Commit `json:"commit"`
}

// QueueEvent is a merge queue addition or removal; Reason is only set on a
// removal, e.g. "failed_checks".
type QueueEvent struct {
	TypeName  string    `json:"__typename"`
	CreatedAt time.Time `json:"createdAt"`
	Reason    string    `json:"reason"`
}

type PullRequest struct {
	Number         int    `json:"number"`
	Title          string `json:"title"`
	URL            string `json:"url"`
	IsDraft        bool   `json:"isDraft"`
	ReviewDecision string `json:"reviewDecision"`
	IsInMergeQueue bool   `json:"isInMergeQueue"`

	// Only the most recent merge queue event is fetched, so a removal here
	// is one nobody has re-queued after.
	TimelineItems struct {
		Nodes []QueueEvent `json:"nodes"`
	} `json:"timelineItems"`

	Repository struct {
		NameWithOwner string `json:"nameWithOwner"`
	} `json:"repository"`

	ReviewRequests struct {
		TotalCount int `json:"totalCount"`
	} `json:"reviewRequests"`

	Commits struct {
		Nodes []CommitNode `json:"nodes"`
	} `json:"commits"`

	Comments struct {
		Nodes []Comment `json:"nodes"`
	} `json:"comments"`

	Reviews struct {
		Nodes []Review `json:"nodes"`
	} `json:"reviews"`

	ReviewThreads struct {
		Nodes []ReviewThread `json:"nodes"`
	} `json:"reviewThreads"`
}

// Ref is the short human identifier, e.g. "element-hq/element-web#35552".
func (pr PullRequest) Ref() string {
	return fmt.Sprintf("%s#%d", pr.Repository.NameWithOwner, pr.Number)
}

// lastPush is the head commit's date.
//
// shortcut: committedDate is when the commit was written, not when it was
// pushed, so work committed days before you push it reads as older than it is
// and a reply can stay in the wrong bucket. The head commit is also attributed
// to you whoever authored it, so a reviewer applying a suggestion counts as
// your activity. Upgrade to timelineItems(PULL_REQUEST_COMMIT) with each
// commit's pushedDate if either starts misfiling PRs.
func (pr PullRequest) lastPush() time.Time {
	if len(pr.Commits.Nodes) == 0 {
		return time.Time{}
	}
	return pr.Commits.Nodes[0].Commit.CommittedDate
}

// dequeued returns the merge queue removal still waiting on you, if any: the
// latest queue event is a removal and nothing has been pushed since.
func (pr PullRequest) dequeued() (reason string, ok bool) {
	if len(pr.TimelineItems.Nodes) == 0 {
		return "", false
	}
	last := pr.TimelineItems.Nodes[0]
	if last.TypeName != "RemovedFromMergeQueueEvent" || !last.CreatedAt.After(pr.lastPush()) {
		return "", false
	}
	return last.Reason, true
}

// checks is the aggregate CI state, or "NONE" when the PR has no checks.
func (pr PullRequest) checks() string {
	if len(pr.Commits.Nodes) == 0 {
		return "NONE"
	}
	if rollup := pr.Commits.Nodes[0].Commit.StatusCheckRollup; rollup != nil {
		return rollup.State
	}
	return "NONE"
}
