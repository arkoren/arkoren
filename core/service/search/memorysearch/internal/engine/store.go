// Package engine owns memory search storage, matching, relevance and ordering.
package engine

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"fmt"
	"slices"
	"sync"

	"arkoren.dev/core/service/search"
)

// stored is one document with its decoded top-level fields.
type stored struct {
	// source is an owned copy of the encoded document.
	source []byte

	// fields are the decoded top-level values: strings, float64, bool, nil,
	// and nested values the driver never reads.
	fields map[string]any
}

// index is one synced index.
type index struct {
	// schema is the definition the index was last synced with.
	schema search.Schema

	// docs maps document IDs to documents.
	docs map[string]stored
}

// Store implements [search.Driver] in process memory. It is safe for
// concurrent use. Searches take a read lock and scan the whole index.
// Use New to initialize its storage; the zero value is not initialized.
// Cancellation is cooperative: lock acquisition, individual JSON decodes, and
// sorting finish before the next check. Completed mutations are not rolled back.
//
// Example:
//
//	driver := memorysearch.New()
//	engine := search.New(driver, func(engine *search.Builder) {
//		engine.Indexes(Products)
//	})
//	if err := engine.Sync(ctx); err != nil {
//		return err
//	}
type Store struct {
	// mu guards indexes.
	mu sync.RWMutex

	// indexes maps index names to synced indexes.
	indexes map[string]*index
}

// NewStore returns an empty driver.
//
//	engine := search.New(memorysearch.New(), func(engine *search.Builder) {
//		engine.Indexes(Products)
//	})
func NewStore() *Store {
	return &Store{indexes: map[string]*index{}}
}

// Sync creates the missing indexes and records the schemas of existing ones,
// keeping their documents. Searchable text is read from the documents on each
// search, so a changed definition applies at once. The driver copies each field
// list; caller edits after Sync returns cannot change the stored definition.
// Cancellation is checked between schemas and may follow partial updates.
//
// Example:
//
//	driver := memorysearch.New()
//	if err := driver.Sync(ctx, []search.Schema{Products.Schema()}); err != nil {
//		return err
//	}
func (driver *Store) Sync(ctx context.Context, schemas []search.Schema) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	driver.mu.Lock()
	defer driver.mu.Unlock()

	for _, schema := range schemas {
		if err := ctx.Err(); err != nil {
			return err
		}

		schema.Searchable = slices.Clone(schema.Searchable)
		schema.Filterable = slices.Clone(schema.Filterable)
		schema.Sortable = slices.Clone(schema.Sortable)

		if existing, ok := driver.indexes[schema.Name]; ok {
			existing.schema = schema

			continue
		}

		driver.indexes[schema.Name] = &index{schema: schema, docs: map[string]stored{}}
	}

	return ctx.Err()
}

// Check compares the synced schemas with schemas. It returns an error
// wrapping [search.ErrNotFound] for an index that was never synced and
// [search.ErrSchemaChanged] for one synced with a different definition.
// It checks cancellation while comparing indexes and changes no stored state.
//
// Example:
//
//	if err := driver.Check(ctx, []search.Schema{Products.Schema()}); err != nil {
//		return err
//	}
func (driver *Store) Check(ctx context.Context, schemas []search.Schema) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	driver.mu.RLock()
	defer driver.mu.RUnlock()

	for _, schema := range schemas {
		if err := ctx.Err(); err != nil {
			return err
		}

		existing, ok := driver.indexes[schema.Name]

		switch {
		case !ok:
			return fmt.Errorf("index %q was never synced: %w", schema.Name, search.ErrNotFound)
		case !existing.schema.Equal(schema):
			return fmt.Errorf("index %q: %w", schema.Name, search.ErrSchemaChanged)
		}
	}

	return ctx.Err()
}

// Put decodes and stores docs, replacing documents with the same ID. It
// decodes every document before storing any, so a malformed document stores
// nothing. Source bytes are copied before retention. Cancellation is observed
// during decoding, after acquiring the write lock, and between writes; it can
// follow partial writes once publication begins.
//
// Example:
//
//	docs := []search.Document{{ID: "p1", Source: []byte(`{"title":"Red shoes"}`)}}
//	if err := driver.Put(ctx, Products.Schema(), docs); err != nil {
//		return err
//	}
func (driver *Store) Put(ctx context.Context, schema search.Schema, docs []search.Document) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	decoded := make([]stored, len(docs))

	for i, doc := range docs {
		if err := ctx.Err(); err != nil {
			return err
		}

		decoded[i].source = bytes.Clone(doc.Source)

		if err := json.Unmarshal(doc.Source, &decoded[i].fields); err != nil {
			return fmt.Errorf("decode document %s: %w", doc.ID, err)
		}
	}

	driver.mu.Lock()
	defer driver.mu.Unlock()

	if err := ctx.Err(); err != nil {
		return err
	}

	target, err := driver.lookup(schema)

	if err != nil {
		return err
	}

	for i, doc := range docs {
		if err := ctx.Err(); err != nil {
			return err
		}

		target.docs[doc.ID] = decoded[i]
	}

	return nil
}

// Delete removes the documents with ids. Missing IDs succeed; a missing index
// returns search.ErrNotFound. Cancellation is checked after lock acquisition
// and between deletions, so it may follow a partially applied batch.
//
// Example:
//
//	if err := driver.Delete(ctx, Products.Schema(), []string{"p1"}); err != nil {
//		return err
//	}
func (driver *Store) Delete(ctx context.Context, schema search.Schema, ids []string) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	driver.mu.Lock()
	defer driver.mu.Unlock()

	if err := ctx.Err(); err != nil {
		return err
	}

	target, err := driver.lookup(schema)

	if err != nil {
		return err
	}

	for _, id := range ids {
		if err := ctx.Err(); err != nil {
			return err
		}

		delete(target.docs, id)
	}

	return nil
}

// Drop removes the index and its documents; an absent index succeeds. It checks
// cancellation again after acquiring the lock, before applying the removal.
//
// Example:
//
//	if err := driver.Drop(ctx, Products.Schema()); err != nil {
//		return err
//	}
func (driver *Store) Drop(ctx context.Context, schema search.Schema) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	driver.mu.Lock()
	defer driver.mu.Unlock()

	if err := ctx.Err(); err != nil {
		return err
	}

	delete(driver.indexes, schema.Name)

	return nil
}

// lookup returns the synced index for schema, or an error wrapping
// [search.ErrNotFound]. The caller holds mu.
func (driver *Store) lookup(schema search.Schema) (*index, error) {
	target, ok := driver.indexes[schema.Name]

	if !ok {
		return nil, fmt.Errorf("index %q was never synced: %w", schema.Name, search.ErrNotFound)
	}

	return target, nil
}
