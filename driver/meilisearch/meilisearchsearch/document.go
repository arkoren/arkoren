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

// Put adds docs to the index, replacing documents with the same ID entirely,
// and returns when Meilisearch has indexed them. Documents are encoded first
// and sent in requests with an approximate 8-MiB threshold; a document is never
// split, so one document and JSON array overhead can exceed that threshold.
// The server's configured payload limit still applies. Requests are queued and
// awaited together, so a failure part way can leave earlier requests applied,
// which a repeated Put fixes.
//
// It returns an error wrapping [search.ErrNotFound] when the index was never
// synced, and one wrapping [search.ErrUnsupported] for a document with a field
// whose name starts with arkoren_ or equals _rankingScore. Every document
// is checked before the first request.
//
// Example:
//
//	docs := []search.Document{{ID: "p1", Source: []byte(`{"title":"red shoes"}`)}}
//	if err := driver.Put(ctx, Products.Schema(), docs); err != nil {
//		return err
//	}
func (driver *Driver) Put(ctx context.Context, schema search.Schema, docs []search.Document) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	uid := driver.uid(schema)

	var batches [][]jsontext.Value
	var batch []jsontext.Value
	size := 0

	for _, doc := range docs {
		encoded, err := index.EncodeDocument(doc)

		if err != nil {
			return fmt.Errorf("meilisearch put %s: %w", uid, err)
		}

		batch = append(batch, encoded)

		if size += len(encoded); size >= maxBatchBytes {
			batches, batch, size = append(batches, batch), nil, 0
		}
	}

	if len(batch) > 0 {
		batches = append(batches, batch)
	}

	if err := driver.requireIndex(ctx, uid); err != nil {
		return fmt.Errorf("meilisearch put %s: %w", uid, err)
	}

	tasks := make([]int64, len(batches))

	for i, batch := range batches {
		var err error

		if tasks[i], err = driver.enqueue(ctx, http.MethodPost, "/indexes/"+uid+"/documents", batch); err != nil {
			return fmt.Errorf("meilisearch put %s: %w", uid, notFound(err, uid))
		}
	}

	for _, task := range tasks {
		if err := driver.wait(ctx, task); err != nil {
			return fmt.Errorf("meilisearch put %s: %w", uid, notFound(err, uid))
		}
	}

	return nil
}

// Delete removes the documents with ids in requests of at most 1,000 IDs and
// returns when Meilisearch has applied them. IDs that are not stored are
// ignored. It returns an error wrapping [search.ErrNotFound] when the index
// was never synced.
//
// Example:
//
//	if err := driver.Delete(ctx, Products.Schema(), []string{"p1"}); err != nil {
//		return err
//	}
func (driver *Driver) Delete(ctx context.Context, schema search.Schema, ids []string) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	uid := driver.uid(schema)

	if err := driver.requireIndex(ctx, uid); err != nil {
		return fmt.Errorf("meilisearch delete %s: %w", uid, err)
	}

	for chunk := range slices.Chunk(ids, maxDeleteIDs) {
		if err := driver.run(ctx, http.MethodPost, "/indexes/"+uid+"/documents/delete-batch", chunk); err != nil {
			return fmt.Errorf("meilisearch delete %s: %w", uid, notFound(err, uid))
		}
	}

	return nil
}
