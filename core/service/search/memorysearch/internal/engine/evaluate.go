package engine

import (
	"bytes"
	"context"

	"arkoren.dev/core/service/search"
)

// result is a matching stored document ranked before selecting a response
// window. Its borrowed document remains protected by the driver's read lock.
type result struct {
	// id is the stored identity used for the final ordering tiebreaker.
	id string

	// doc supplies decoded sort fields and the bytes copied into a response.
	doc stored

	// score is the relevance value shared by matching documents in this driver.
	score float64
}

// Search scans the index, filters, orders, and slices the window. The
// searchable fields come from schema, the one the engine passes, so they
// follow the definition even before a Sync recorded it.
// Returned source bytes are detached from stored documents. Cancellation is
// observed after lock acquisition, between scanned/copied documents, and after
// sorting. No partial result is returned when cancellation is observed.
//
// Example:
//
//	spec := search.NewQuery(Products, "red").Inspect()
//	matches, err := driver.Search(ctx, Products.Schema(), spec, 20, 0)
//	if err != nil {
//		return err
//	}
//	fmt.Println(matches.Total)
func (driver *Store) Search(
	ctx context.Context,
	schema search.Schema,
	spec search.Spec,
	limit, offset int,
) (search.Matches, error) {
	if err := ctx.Err(); err != nil {
		return search.Matches{}, err
	}

	driver.mu.RLock()
	defer driver.mu.RUnlock()

	if err := ctx.Err(); err != nil {
		return search.Matches{}, err
	}

	target, err := driver.lookup(schema)

	if err != nil {
		return search.Matches{}, err
	}

	results, err := scanMatches(ctx, target, schema, spec)

	if err != nil {
		return search.Matches{}, err
	}

	orderResults(results, spec.Orders)

	if err := ctx.Err(); err != nil {
		return search.Matches{}, err
	}

	matches := search.Matches{Total: int64(len(results))}

	for _, item := range results[min(offset, len(results)):min(offset+limit, len(results))] {
		if err := ctx.Err(); err != nil {
			return search.Matches{}, err
		}

		matches.Items = append(matches.Items, search.Match{
			Document: search.Document{ID: item.id, Source: bytes.Clone(item.doc.source)},
			Score:    item.score,
		})
	}

	return matches, nil
}

// scanMatches filters the stored documents under the caller's read lock.
// Cancellation discards the incomplete scan; relevance is computed once because
// every match satisfies all positive terms in this in-memory implementation.
func scanMatches(ctx context.Context, target *index, schema search.Schema, spec search.Spec) ([]result, error) {
	var results []result
	relevance := score(spec.Text)

	for id, doc := range target.docs {
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		if !matchesText(doc, schema.Searchable, spec.Text) || !matchesConditions(doc.fields, spec.Where) {
			continue
		}

		results = append(results, result{
			id:    id,
			doc:   doc,
			score: relevance,
		})
	}

	return results, ctx.Err()
}
