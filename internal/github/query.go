// Package github fetches the pull request timelines that classification needs.
//
// It works in two passes. The first asks only for identifiers, which is cheap;
// the second fetches timelines in parallel batches, which is where the time
// goes. Splitting them lets the caller name the pull requests it is waiting on
// instead of showing a bare spinner.
package github

import (
	"cmp"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/cli/go-gh/v2/pkg/api"

	"github.com/kaylendog/gh-turn/internal/classify"
)

// Your own pull requests come from viewer.pullRequests rather than a search.
// The two return an identical set, but for the identifier-only first pass
// search is about eight times slower. (That advantage disappears once
// timelines are attached, which is why the second pass does not use it.)
//
// Review requests have no equivalent connection, so that half stays on search.
// user-review-requested matches only requests aimed at you personally, keeping
// out anything routed via a team review queue.
const requestedSearch = "is:open is:pr user-review-requested:@me archived:false sort:updated-desc"

// Batching matters more than it looks. Measured against ~32 open pull
// requests, parallel batches took ~2.3s against ~4.5s for one large request;
// one request per pull request was worse again at ~4.0s, paying the
// per-request overhead every time. Smaller batches also update the progress
// display more often, which is why these are not larger.
const (
	chunkSize = 4
	workers   = 6
)

const listQuery = `
query($requested: String!, $limit: Int!) {
  viewer {
    login
    pullRequests(states: OPEN, first: $limit,
                 orderBy: {field: UPDATED_AT, direction: DESC}) {
      nodes { id number updatedAt repository { nameWithOwner isArchived } }
    }
  }
  requested: search(query: $requested, type: ISSUE, first: $limit) {
    nodes { ... on PullRequest {
      id number updatedAt repository { nameWithOwner }
    }}
  }
}`

// Review threads are fetched one comment deep because only the most recent
// reply matters for working out who spoke last.
const timelineQuery = `
query($ids: [ID!]!) {
  nodes(ids: $ids) {
    ... on PullRequest {
      number title url isDraft reviewDecision isInMergeQueue
      repository { nameWithOwner }
      timelineItems(last: 1, itemTypes: [ADDED_TO_MERGE_QUEUE_EVENT, REMOVED_FROM_MERGE_QUEUE_EVENT]) {
        nodes { __typename ... on RemovedFromMergeQueueEvent { createdAt reason } }
      }
      reviewRequests(first: 20) { totalCount }
      commits(last: 1) { nodes { commit { committedDate statusCheckRollup { state } } } }
      comments(last: 20) { nodes { author { login __typename } createdAt } }
      reviews(last: 20) { nodes { author { login __typename } state submittedAt } }
      reviewThreads(last: 20) {
        nodes { comments(last: 1) { nodes { author { login __typename } createdAt } } }
      }
    }
  }
}`

// Ref identifies a pull request before its timeline has been fetched.
type Ref struct {
	ID   string
	Name string // owner/repo#number
}

type listNode struct {
	ID         string    `json:"id"`
	Number     int       `json:"number"`
	UpdatedAt  time.Time `json:"updatedAt"`
	Repository struct {
		NameWithOwner string `json:"nameWithOwner"`
		IsArchived    bool   `json:"isArchived"`
	} `json:"repository"`
}

// Options bounds what Fetch asks for. Filtering by date happens in the first
// pass, so anything excluded never costs a timeline fetch.
type Options struct {
	Limit  int
	After  time.Time // zero means no lower bound
	Before time.Time // zero means no upper bound
}

// wanted reports whether a pull request's last activity falls in range.
func (o Options) wanted(n listNode) bool {
	if !o.After.IsZero() && n.UpdatedAt.Before(o.After) {
		return false
	}
	if !o.Before.IsZero() && n.UpdatedAt.After(o.Before) {
		return false
	}
	return true
}

// Progress is called whenever a batch starts or finishes, naming the pull
// requests being fetched right now. Names stay in list order so the display
// does not reshuffle between updates.
type Progress func(done, total int, inFlight []string)

// Response holds the pull requests to classify, archived repositories already
// dropped.
type Response struct {
	Login     string
	Authored  []classify.PullRequest
	Requested []classify.PullRequest
}

// Fetch returns your open pull requests alongside those awaiting your review.
func Fetch(opts Options, progress Progress) (*Response, error) {
	client, err := api.DefaultGraphQLClient()
	if err != nil {
		return nil, fmt.Errorf("connecting to GitHub: %w", err)
	}

	login, authored, requested, err := list(client, opts)
	if err != nil {
		return nil, err
	}

	// A pull request can be in both halves; fetching it twice would be waste,
	// so timelines are keyed by id and looked up per half afterwards.
	both := append(append([]Ref{}, authored...), requested...)
	timelines, err := fetchTimelines(client, dedupe(both), progress)
	if err != nil {
		return nil, err
	}

	return &Response{
		Login:     login,
		Authored:  collect(authored, timelines),
		Requested: collect(requested, timelines),
	}, nil
}

// list names every pull request to fetch, without paying for any timelines.
func list(client *api.GraphQLClient, opts Options) (login string, authored, requested []Ref, err error) {
	var raw struct {
		Viewer struct {
			Login        string `json:"login"`
			PullRequests struct {
				Nodes []listNode `json:"nodes"`
			} `json:"pullRequests"`
		} `json:"viewer"`
		Requested struct {
			Nodes []listNode `json:"nodes"`
		} `json:"requested"`
	}

	variables := map[string]any{"requested": requestedSearch, "limit": opts.Limit}
	if err := client.Do(listQuery, variables, &raw); err != nil {
		return "", nil, nil, fmt.Errorf("listing pull requests: %w", err)
	}

	// shortcut: viewer.pullRequests has no archived filter, unlike the
	// `archived:false` qualifier search offers, so archived pull requests are
	// dropped here and still count towards --limit. Paginate if you ever hold
	// more open pull requests than that, archived ones included.
	var mine, theirs []listNode
	for _, n := range raw.Viewer.PullRequests.Nodes {
		if !n.Repository.IsArchived && opts.wanted(n) {
			mine = append(mine, n)
		}
	}
	for _, n := range raw.Requested.Nodes {
		if opts.wanted(n) {
			theirs = append(theirs, n)
		}
	}
	return raw.Viewer.Login, refs(mine), refs(theirs), nil
}

// refs orders by repository then number so pull requests in the same
// organisation sit together everywhere they are shown.
//
// Two details matter for reading it: the number is compared numerically,
// since sorting the rendered name would put #101 before #96; and the
// repository is folded to lower case, since ASCII order would otherwise file
// every capitalised organisation ahead of the rest.
func refs(nodes []listNode) []Ref {
	slices.SortFunc(nodes, func(a, b listNode) int {
		return cmp.Or(
			cmp.Compare(
				strings.ToLower(a.Repository.NameWithOwner),
				strings.ToLower(b.Repository.NameWithOwner),
			),
			cmp.Compare(a.Number, b.Number),
		)
	})

	out := make([]Ref, 0, len(nodes))
	for _, n := range nodes {
		out = append(out, Ref{
			ID:   n.ID,
			Name: fmt.Sprintf("%s#%d", n.Repository.NameWithOwner, n.Number),
		})
	}
	return out
}

// fetchTimelines runs the batches concurrently and returns them keyed by id.
func fetchTimelines(client *api.GraphQLClient, refs []Ref, progress Progress) (map[string]classify.PullRequest, error) {
	var (
		mu      sync.Mutex
		out     = make(map[string]classify.PullRequest, len(refs))
		failure error
		done    int
		active  = make([]bool, len(refs)) // indexed by position in refs
		wg      sync.WaitGroup
		limiter = make(chan struct{}, workers)
	)

	// emit reports the current in-flight set. The caller must hold mu.
	emit := func() {
		if progress == nil {
			return
		}
		names := make([]string, 0, len(refs))
		for i, inFlight := range active {
			if inFlight {
				names = append(names, refs[i].Name)
			}
		}
		progress(done, len(refs), names)
	}

	for start := 0; start < len(refs); start += chunkSize {
		batch := refs[start:min(start+chunkSize, len(refs))]

		wg.Add(1)
		go func(start int, batch []Ref) {
			defer wg.Done()

			// Acquire first: a batch waiting for a worker is not yet in
			// flight, and showing it as such would be a lie.
			limiter <- struct{}{}
			defer func() { <-limiter }()

			ids := make([]string, len(batch))
			for i, r := range batch {
				ids[i] = r.ID
			}

			mu.Lock()
			for i := range batch {
				active[start+i] = true
			}
			emit()
			mu.Unlock()

			var raw struct {
				Nodes []classify.PullRequest `json:"nodes"`
			}
			err := client.Do(timelineQuery, map[string]any{"ids": ids}, &raw)

			mu.Lock()
			defer mu.Unlock()
			for i := range batch {
				active[start+i] = false
			}
			if err != nil {
				if failure == nil {
					failure = fmt.Errorf("fetching timelines: %w", err)
				}
				return
			}
			// nodes(ids:) answers in the order asked, so index lines up.
			for i, pr := range raw.Nodes {
				if i < len(ids) {
					out[ids[i]] = pr
				}
			}
			done += len(batch)
			emit()
		}(start, batch)
	}

	wg.Wait()
	if failure != nil {
		return nil, failure
	}
	return out, nil
}

// collect restores the order of refs, which the concurrent fetch loses.
func collect(refs []Ref, timelines map[string]classify.PullRequest) []classify.PullRequest {
	prs := make([]classify.PullRequest, 0, len(refs))
	for _, r := range refs {
		if pr, ok := timelines[r.ID]; ok {
			prs = append(prs, pr)
		}
	}
	return prs
}

func dedupe(refs []Ref) []Ref {
	seen := make(map[string]bool, len(refs))
	out := make([]Ref, 0, len(refs))
	for _, r := range refs {
		if !seen[r.ID] {
			seen[r.ID] = true
			out = append(out, r)
		}
	}
	return out
}
