package opensearchsearch

import (
	"context"
	"encoding/json/v2"
	"fmt"
	"net/http"
	"net/url"

	"arkoren.dev/core/service/search"
	"arkoren.dev/driver/opensearch/opensearchsearch/internal/index"
)

const (
	// searchPreference pins searches to the same shard copies for stable scores.
	searchPreference = "arkoren"

	// maxBulkDocuments is the most documents or deletions in one bulk
	// request.
	maxBulkDocuments = 500

	// maxBulkBytes is the size at which a bulk request is sent before it
	// takes another document. It keeps requests below the limits of hosted
	// clusters, commonly 10 MiB.
	maxBulkBytes = 5 << 20
)

// Search runs the query compiled by [searchBody] against the index. It
// returns the window, the score of every hit (0 when the text has no positive
// term), and the number of matches counted up to [search.MaxHits].
// Native timeouts and failed shards are errors, never successful partial pages.
// Raw callers supply normalized schema/spec and the core's bounded positive
// window. Returned Document.Source is the original JSON bytes, not the native
// typed wrapper; document IDs remain independent of fields in that JSON.
//
// Example:
//
//	query := search.NewQuery(Products, "red shoes")
//	matches, err := driver.Search(ctx, Products.Schema(), query.Inspect(), 20, 0)
//	if err != nil {
//		return err
//	}
//	_ = matches.Items
func (driver *Driver) Search(ctx context.Context, schema search.Schema, spec search.Spec, limit, offset int) (search.Matches, error) {
	name, err := driver.indexName(schema.Name)

	if err != nil {
		return search.Matches{}, err
	}

	params := url.Values{"preference": {searchPreference}, "allow_partial_search_results": {"false"}}
	reply, err := driver.doJSON(ctx, http.MethodPost, "/"+name+"/_search", params, index.SearchBody(schema, spec, limit, offset))

	if err != nil {
		return search.Matches{}, err
	}

	if err := reply.err("search " + name); err != nil {
		return search.Matches{}, err
	}

	var result struct {
		// TimedOut rejects partial counts even when HTTP reports success.
		TimedOut bool `json:"timed_out"`

		// Shards reports native partial failures independently of HTTP status.
		Shards struct {
			// Failed counts shards whose missing results invalidate this window.
			Failed int `json:"failed"`
		} `json:"_shards"`

		// Hits contains the counted total and requested result window.
		Hits struct {
			// Total contains the match count bounded by search.MaxHits.
			Total struct {
				// Value is the count returned by the server's total-hits object.
				Value int64 `json:"value"`
			} `json:"total"`

			// Hits preserves the server's final sort and relevance order.
			Hits []struct {
				// ID is the native document key exposed in each search match.
				ID string `json:"_id"`

				// Score is nil when the server has no relevance score for this hit.
				Score *float64 `json:"_score"`

				// Source contains only the requested original JSON string, not the
				// physical scalar containers used for native indexing.
				Source index.Document `json:"_source"`
			} `json:"hits"`
		} `json:"hits"`
	}

	if err := json.Unmarshal(reply.body, &result); err != nil {
		return search.Matches{}, fmt.Errorf("opensearch: search %s: decode response: %w", name, err)
	}

	if result.TimedOut || result.Shards.Failed != 0 {
		return search.Matches{}, fmt.Errorf("opensearch: search %s: incomplete response (timed out %t, failed shards %d)", name, result.TimedOut, result.Shards.Failed)
	}

	matches := search.Matches{
		Total: result.Hits.Total.Value,
	}

	for _, hit := range result.Hits.Hits {
		match := search.Match{
			Document: search.Document{
				ID:     hit.ID,
				Source: []byte(hit.Source.Original),
			},
		}

		if hit.Score != nil && len(spec.Text) > 0 {
			match.Score = *hit.Score
		}

		matches.Items = append(matches.Items, match)
	}

	return matches, nil
}
