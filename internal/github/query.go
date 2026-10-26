// Package github fetches the pull request timelines that classification needs.
package github

import (
	"fmt"

	"github.com/cli/go-gh/v2/pkg/api"

	"github.com/kaylendog/gh-turn/internal/classify"
)

// The searches are deliberately narrow. user-review-requested matches only
// requests aimed at you personally, so PRs routed to you through a team review
// queue stay out; archived repositories are dropped because nothing in them is
// actionable.
const (
	authoredSearch  = "is:open is:pr author:@me archived:false sort:updated-desc"
	requestedSearch = "is:open is:pr user-review-requested:@me archived:false sort:updated-desc"
)

// Review threads are fetched one comment deep because only the most recent
// reply matters for working out who spoke last.
const query = `
query($authored: String!, $requested: String!, $limit: Int!) {
  viewer { login }
  authored: search(query: $authored, type: ISSUE, first: $limit) {
    nodes { ...prFields }
  }
  requested: search(query: $requested, type: ISSUE, first: $limit) {
    nodes { ... on PullRequest {
      number title url
      repository { nameWithOwner }
      reviews(last: 20) { nodes { author { login __typename } state submittedAt } }
    }}
  }
}
fragment prFields on PullRequest {
  number title url isDraft reviewDecision
  repository { nameWithOwner }
  reviewRequests(first: 20) { totalCount }
  commits(last: 1) { nodes { commit { committedDate statusCheckRollup { state } } } }
  comments(last: 20) { nodes { author { login __typename } createdAt } }
  reviews(last: 20) { nodes { author { login __typename } state submittedAt } }
  reviewThreads(last: 20) {
    nodes { comments(last: 1) { nodes { author { login __typename } createdAt } } }
  }
}`

type results struct {
	Nodes []classify.PullRequest `json:"nodes"`
}

type Response struct {
	Viewer struct {
		Login string `json:"login"`
	} `json:"viewer"`
	Authored  results `json:"authored"`
	Requested results `json:"requested"`
}

// Fetch returns your open pull requests alongside those awaiting your review,
// in one round trip.
func Fetch(limit int) (*Response, error) {
	client, err := api.DefaultGraphQLClient()
	if err != nil {
		return nil, fmt.Errorf("connecting to GitHub: %w", err)
	}

	variables := map[string]any{
		"authored":  authoredSearch,
		"requested": requestedSearch,
		"limit":     limit,
	}

	var response Response
	if err := client.Do(query, variables, &response); err != nil {
		return nil, fmt.Errorf("fetching pull requests: %w", err)
	}
	return &response, nil
}
