package meilisearchsearch

import (
	"context"
	"encoding/json/jsontext"
	"fmt"
	"net/http"
	"slices"

	"arkoren.dev/core/service/search"
	"arkoren.dev/driver/meilisearch/meilisearchsearch/internal/index"
)

// Search returns the window of limit results starting at offset. It uses
// Meilisearch's page mode, which counts matches exactly up to the pagination
// limit that [Driver.Sync] pins to [search.MaxHits], and matches every word
// ("all" matching strategy) with the last word as a prefix.
//
// Ordering follows the contract: the query's orders, then relevance, then
// ascending ID in byte order. The sort rules come first among the index's
// ranking rules and the ID is the last, so a query with orders still breaks
// ties by relevance. [search.Match.Score] is Meilisearch's ranking score
// between 0 and 1, and 0 when the query has no word to score.
//
// Page mode needs offset to be a multiple of limit, which [search.Engine]
// always satisfies; any other window fails with [search.ErrUnsupported]. A
// query of more than ten positive words and phrases also fails with it. A never synced
// index fails with [search.ErrNotFound].
// Nonphrase exclusions that split into multiple words are unsupported rather
// than weakened to native phrase exclusions. Raw callers must supply the core's
// normalized schema, spec, positive limit, and bounded offset.
//
// Example:
//
//	query := search.NewQuery(Products, "red shoes")
//	matches, err := driver.Search(ctx, Products.Schema(), query.Inspect(), 20, 0)
//	if err != nil {
//		return err
//	}
//	_ = matches.Total
func (driver *Driver) Search(ctx context.Context, schema search.Schema, spec search.Spec, limit, offset int) (search.Matches, error) {
	if err := ctx.Err(); err != nil {
		return search.Matches{}, err
	}

	uid := driver.uid(schema)

	if offset%limit != 0 {
		return search.Matches{}, fmt.Errorf("meilisearch search %s: %w: offset %d is not a multiple of limit %d", uid, search.ErrUnsupported, offset, limit)
	}

	request, err := index.Query(spec, limit, offset)

	if err != nil {
		return search.Matches{}, fmt.Errorf("meilisearch search %s: %w", uid, err)
	}

	var response struct {
		// Hits holds document fields and the reserved driver and score fields.
		Hits []map[string]jsontext.Value `json:"hits"`

		// TotalHits is the counted total up to the index pagination limit.
		TotalHits int64 `json:"totalHits"`
	}

	if err := driver.do(ctx, http.MethodPost, "/indexes/"+uid+"/search", request, &response); err != nil {
		return search.Matches{}, fmt.Errorf("meilisearch search %s: %w", uid, notFound(err, uid))
	}

	// A text of only exclusions has nothing to rank, like no text at all.
	scored := slices.ContainsFunc(spec.Text, func(term search.Term) bool {
		return !term.Exclude
	})

	matches := search.Matches{
		Items: make([]search.Match, len(response.Hits)),
		Total: response.TotalHits,
	}

	for i, hit := range response.Hits {
		match, err := index.DecodeHit(hit, scored)

		if err != nil {
			return search.Matches{}, fmt.Errorf("meilisearch search %s: %w", uid, err)
		}

		matches.Items[i] = match
	}

	return matches, nil
}
