package memorysearch

import (
	"context"

	"arkoren.dev/core/service/search"
	"arkoren.dev/core/service/search/memorysearch/internal/engine"
)

// Driver implements [search.Driver] in process memory. It is safe for
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
type Driver struct {
	// store owns index state and its read/write lock.
	store *engine.Store
}

var _ search.Driver = (*Driver)(nil)

// New returns an empty driver.
//
//	engine := search.New(memorysearch.New(), func(engine *search.Builder) {
//		engine.Indexes(Products)
//	})
func New() *Driver {
	return &Driver{store: engine.NewStore()}
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
func (driver *Driver) Sync(ctx context.Context, schemas []search.Schema) error {
	return driver.store.Sync(ctx, schemas)
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
func (driver *Driver) Check(ctx context.Context, schemas []search.Schema) error {
	return driver.store.Check(ctx, schemas)
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
func (driver *Driver) Put(ctx context.Context, schema search.Schema, docs []search.Document) error {
	return driver.store.Put(ctx, schema, docs)
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
func (driver *Driver) Delete(ctx context.Context, schema search.Schema, ids []string) error {
	return driver.store.Delete(ctx, schema, ids)
}

// Drop removes the index and its documents; an absent index succeeds. It checks
// cancellation again after acquiring the lock, before applying the removal.
//
// Example:
//
//	if err := driver.Drop(ctx, Products.Schema()); err != nil {
//		return err
//	}
func (driver *Driver) Drop(ctx context.Context, schema search.Schema) error {
	return driver.store.Drop(ctx, schema)
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
func (driver *Driver) Search(
	ctx context.Context,
	schema search.Schema,
	spec search.Spec,
	limit, offset int,
) (search.Matches, error) {
	return driver.store.Search(ctx, schema, spec, limit, offset)
}
