// Package github fetches the pull request timelines that classification needs.
//
// It works in two passes. The first asks only for identifiers, which is cheap;
// the second fetches timelines in parallel batches, which is where the time
// goes. Splitting them lets the caller name the pull requests it is waiting on
// instead of showing a bare spinner.
package github

import (
	"fmt"
	"sync"

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

// Measured against ~32 open pull requests: 8 per request across 4 concurrent
// requests took ~2.3s, against ~4.5s for one large request and ~4.0s for one
// request per pull request, which pays the per-request overhead every time.
const (
	chunkSize = 8
	workers   = 4
)

const listQuery = `
query($requested: String!, $limit: Int!) {
  viewer {
    login
    pullRequests(states: OPEN, first: $limit,
                 orderBy: {field: UPDATED_AT, direction: DESC}) {
      nodes { id number repository { nameWithOwner isArchived } }
    }
  }
  requested: search(query: $requested, type: ISSUE, first: $limit) {
    nodes { ... on PullRequest { id number repository { nameWithOwner } } }
  }
}`

// Review threads are fetched one comment deep because only the most recent
// reply matters for working out who spoke last.
const timelineQuery = `
query($ids: [ID!]!) {
  nodes(ids: $ids) {
    ... on PullRequest {
      number title url isDraft reviewDecision
      repository { nameWithOwner }
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
	ID         string `json:"id"`
	Number     int    `json:"number"`
	Repository struct {
		NameWithOwner string `json:"nameWithOwner"`
		IsArchived    bool   `json:"isArchived"`
	} `json:"repository"`
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
func Fetch(limit int, progress Progress) (*Response, error) {
	client, err := api.DefaultGraphQLClient()
	if err != nil {
		return nil, fmt.Errorf("connecting to GitHub: %w", err)
	}

	login, authored, requested, err := list(client, limit)
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
func list(client *api.GraphQLClient, limit int) (login string, authored, requested []Ref, err error) {
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

	variables := map[string]any{"requested": requestedSearch, "limit": limit}
	if err := client.Do(listQuery, variables, &raw); err != nil {
		return "", nil, nil, fmt.Errorf("listing pull requests: %w", err)
	}

	// shortcut: viewer.pullRequests has no archived filter, unlike the
	// `archived:false` qualifier search offers, so archived pull requests are
	// dropped here and still count towards --limit. Paginate if you ever hold
	// more open pull requests than that, archived ones included.
	for _, n := range raw.Viewer.PullRequests.Nodes {
		if !n.Repository.IsArchived {
			authored = append(authored, ref(n))
		}
	}
	for _, n := range raw.Requested.Nodes {
		requested = append(requested, ref(n))
	}
	return raw.Viewer.Login, authored, requested, nil
}

func ref(n listNode) Ref {
	return Ref{ID: n.ID, Name: fmt.Sprintf("%s#%d", n.Repository.NameWithOwner, n.Number)}
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
