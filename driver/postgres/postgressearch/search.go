package postgressearch

import (
	"context"
	"encoding/json/v2"
	"fmt"
	"slices"

	"arkoren.dev/core/service/search"
	"arkoren.dev/driver/postgres/postgressearch/internal/statement"
	"arkoren.dev/foundation/query"
)

// putChunkSize is the number of documents one INSERT carries. Each takes two
// bind parameters, so a chunk stays far below the 65,535 limit of the
// PostgreSQL protocol and keeps statements small.
const putChunkSize = 1000

// definition is the stored form of a [search.Schema], in the definition
// column of the indexes table. The name is the row's key.
type definition struct {
	// Searchable lists the text fields used to build document tsvectors.
	Searchable []string `json:"searchable"`

	// Filterable lists the fields permitted in typed JSON comparisons.
	Filterable []string `json:"filterable"`

	// Sortable lists the fields permitted in explicit result ordering.
	Sortable []string `json:"sortable"`
}

// storedIndex is one row of the indexes table.
type storedIndex struct {
	// Name identifies the logical index within this driver's tables.
	Name string `db:"name"`

	// Definition holds the JSON-encoded field roles of the synced schema.
	Definition string `db:"definition"`
}

// resultRow is one row of a search: the document and its relevance.
type resultRow struct {
	// ID is the document key returned in the search match.
	ID string `db:"id"`

	// Doc is the persisted JSON source returned without driver metadata.
	Doc string `db:"doc"`

	// Score is PostgreSQL's relevance rank for the compiled text query.
	Score float64 `db:"score"`
}

// stored returns the schemas recorded for the named indexes; indexes that
// were never synced are absent from the result.
func (driver *Driver) stored(ctx context.Context, names []string) (map[string]search.Schema, error) {
	rows, err := driver.db.Select[storedIndex](ctx, query.Raw(
		"SELECT name, definition::text AS definition FROM "+query.Postgres.Quote(driver.indexes)+" WHERE name = ANY(?::text[])", names))

	if err != nil {
		return nil, err
	}

	schemas := make(map[string]search.Schema, len(rows))

	for _, row := range rows {
		var stored definition

		if err := json.Unmarshal([]byte(row.Definition), &stored); err != nil {
			return nil, fmt.Errorf("decode definition of index %s: %w", row.Name, err)
		}

		schemas[row.Name] = search.Schema{
			Name:       row.Name,
			Searchable: stored.Searchable,
			Filterable: stored.Filterable,
			Sortable:   stored.Sortable,
		}
	}

	return schemas, nil
}

// requireIndex fails with [search.ErrNotFound] when the index was never
// synced or was dropped.
func (driver *Driver) requireIndex(ctx context.Context, name string) error {
	exists, err := driver.db.Find[bool](ctx, query.Raw(
		"SELECT EXISTS (SELECT 1 FROM "+query.Postgres.Quote(driver.indexes)+" WHERE name = ?)", name))

	if err != nil {
		return err
	}

	if !exists {
		return fmt.Errorf("index %s: %w: it was never synced or was dropped", name, search.ErrNotFound)
	}

	return nil
}

// Sync records the definition of every schema and, for an index whose
// searchable fields changed, recomputes the tsvector of its documents. It
// runs in one transaction under an advisory lock named after the documents
// table, so concurrent deploys queue instead of interleaving, and it changes
// data only. Recomputing is a full pass over the index and can be slow.
// Schema input follows search.Driver's validation contract; the supplied facade
// keeps ownership of its connection and transaction scopes.
//
// Example:
//
//	if err := driver.Sync(ctx, []search.Schema{Products.Schema()}); err != nil {
//		return err
//	}
func (driver *Driver) Sync(ctx context.Context, schemas []search.Schema) error {
	names := make([]string, len(schemas))

	for i, schema := range schemas {
		names[i] = schema.Name
	}

	err := driver.db.WithTransaction(ctx, func(ctx context.Context) error {
		if _, err := driver.db.Exec(ctx, query.Raw("SELECT pg_advisory_xact_lock(hashtextextended(?, 0))", driver.documents)); err != nil {
			return fmt.Errorf("lock: %w", err)
		}

		previous, err := driver.stored(ctx, names)

		if err != nil {
			return err
		}

		for _, schema := range schemas {
			encoded, err := json.Marshal(definition{
				Searchable: schema.Searchable,
				Filterable: schema.Filterable,
				Sortable:   schema.Sortable,
			})

			if err != nil {
				return fmt.Errorf("encode definition of index %s: %w", schema.Name, err)
			}

			if _, err := driver.db.Exec(ctx, query.Raw("INSERT INTO "+query.Postgres.Quote(driver.indexes)+
				" (name, definition) VALUES (?, ?::jsonb) ON CONFLICT (name) DO UPDATE SET definition = EXCLUDED.definition",
				schema.Name, string(encoded))); err != nil {
				return fmt.Errorf("record index %s: %w", schema.Name, err)
			}

			before, exists := previous[schema.Name]

			if !exists || slices.Equal(before.Searchable, schema.Searchable) {
				continue
			}

			if _, err := driver.db.Exec(ctx, statement.Reindex(driver.documents, schema)); err != nil {
				return fmt.Errorf("reindex %s: %w", schema.Name, err)
			}
		}

		return nil
	})

	if err != nil {
		return fmt.Errorf("postgres search sync: %w", err)
	}

	return nil
}

// Check compares the stored definitions with schemas without changing
// anything. It fails with [search.ErrNotFound] for an index without a
// definition row and with [search.ErrSchemaChanged] for a different one.
// Extra stored indexes are ignored. Run Sync explicitly to apply differences;
// Check never writes definitions or creates schema objects.
//
// Example:
//
//	if err := driver.Check(ctx, []search.Schema{Products.Schema()}); err != nil {
//		return err
//	}
func (driver *Driver) Check(ctx context.Context, schemas []search.Schema) error {
	names := make([]string, len(schemas))

	for i, schema := range schemas {
		names[i] = schema.Name
	}

	stored, err := driver.stored(ctx, names)

	if err != nil {
		return fmt.Errorf("postgres search check: %w", err)
	}

	for _, schema := range schemas {
		found, ok := stored[schema.Name]

		switch {
		case !ok:
			return fmt.Errorf("postgres search check: index %s: %w: run Sync", schema.Name, search.ErrNotFound)
		case !schema.Equal(found):
			return fmt.Errorf("postgres search check: index %s: %w: run Sync", schema.Name, search.ErrSchemaChanged)
		}
	}

	return nil
}

// Put upserts docs in chunks of [putChunkSize], each chunk one statement that
// writes only while the index's definition row exists, and holds a share lock
// on that row so a concurrent Drop waits for it. A chunk that writes nothing
// means the index is missing and fails with [search.ErrNotFound]. The
// tsvector comes from the searchable fields of schema.
// IDs must already be valid and unique within docs. Large batches are not atomic
// across chunks; repeating an upsert batch after a failure is safe.
//
// Example:
//
//	err := driver.Put(ctx, Products.Schema(), []search.Document{
//		{ID: "p1", Source: []byte(`{"title":"Red shoes"}`)},
//	})
//	if err != nil {
//		return err
//	}
func (driver *Driver) Put(ctx context.Context, schema search.Schema, docs []search.Document) error {
	if len(docs) == 0 {
		if err := driver.requireIndex(ctx, schema.Name); err != nil {
			return fmt.Errorf("postgres search put: %w", err)
		}

		return nil
	}

	for chunk := range slices.Chunk(docs, putChunkSize) {
		written, err := driver.db.Exec(ctx, statement.Put(driver.documents, driver.indexes, schema, chunk))

		if err != nil {
			return fmt.Errorf("postgres search put %s: %w", schema.Name, err)
		}

		if written == 0 {
			return fmt.Errorf("postgres search put %s: %w: it was never synced or was dropped", schema.Name, search.ErrNotFound)
		}
	}

	return nil
}

// Delete removes the documents with ids; missing IDs succeed. The logical index
// must exist even for an empty ID list, otherwise search.ErrNotFound is returned.
// Names and IDs follow the already-validated search.Driver contract.
//
// Example:
//
//	if err := driver.Delete(ctx, Products.Schema(), []string{"p1"}); err != nil {
//		return err
//	}
func (driver *Driver) Delete(ctx context.Context, schema search.Schema, ids []string) error {
	if err := driver.requireIndex(ctx, schema.Name); err != nil {
		return fmt.Errorf("postgres search delete: %w", err)
	}

	if len(ids) == 0 {
		return nil
	}

	if _, err := driver.db.Exec(ctx, query.Raw(
		"DELETE FROM "+query.Postgres.Quote(driver.documents)+" WHERE index_name = ? AND id = ANY(?::text[])", schema.Name, ids)); err != nil {
		return fmt.Errorf("postgres search delete %s: %w", schema.Name, err)
	}

	return nil
}

// Search answers spec with one SELECT for the window and one capped count for
// the total. The order is the query's orders, then ts_rank for text queries,
// then the ID in byte order. See the package documentation for how text and
// filters compile.
// limit must be positive and offset+limit must not exceed search.MaxHits. The
// supplied spec is already validated; returned document bytes are independent.
// Window and count use separate reads, not a shared snapshot of concurrent writes.
//
// Example:
//
//	matches, err := driver.Search(ctx, Products.Schema(), search.Spec{}, 20, 0)
//	if err != nil {
//		return err
//	}
//	_ = matches.Items
func (driver *Driver) Search(ctx context.Context, schema search.Schema, spec search.Spec, limit, offset int) (search.Matches, error) {
	if err := driver.requireIndex(ctx, schema.Name); err != nil {
		return search.Matches{}, fmt.Errorf("postgres search: %w", err)
	}

	selection, counting, err := statement.Search(driver.documents, schema.Name, spec, limit, offset)

	if err != nil {
		return search.Matches{}, fmt.Errorf("postgres search %s: %w", schema.Name, err)
	}

	rows, err := driver.db.Select[resultRow](ctx, selection)

	if err != nil {
		return search.Matches{}, fmt.Errorf("postgres search %s: %w", schema.Name, err)
	}

	total, err := driver.db.Find[int64](ctx, counting)

	if err != nil {
		return search.Matches{}, fmt.Errorf("postgres search %s: count: %w", schema.Name, err)
	}

	return matchesFromRows(rows, total), nil
}

// matchesFromRows converts native SQL results into independently owned contract
// documents. JSON text becomes fresh bytes; IDs, relevance, order, and the capped
// count are preserved without another database read or JSON normalization.
func matchesFromRows(rows []resultRow, total int64) search.Matches {
	matches := search.Matches{
		Items: make([]search.Match, len(rows)),
		Total: total,
	}

	for i, row := range rows {
		matches.Items[i] = search.Match{
			Document: search.Document{
				ID:     row.ID,
				Source: []byte(row.Doc),
			},
			Score: row.Score,
		}
	}

	return matches
}

// Drop removes the definition row first, which waits for writers holding its
// share lock and stops later ones, then the documents, in one transaction.
// Dropping a missing index succeeds.
// This permanently deletes indexed documents, not the application's source
// records or the shared tables. A later Sync recreates the logical index empty.
//
// Example:
//
//	if err := driver.Drop(ctx, Products.Schema()); err != nil {
//		return err
//	}
func (driver *Driver) Drop(ctx context.Context, schema search.Schema) error {
	err := driver.db.WithTransaction(ctx, func(ctx context.Context) error {
		if _, err := driver.db.Exec(ctx, query.Raw("DELETE FROM "+query.Postgres.Quote(driver.indexes)+" WHERE name = ?", schema.Name)); err != nil {
			return err
		}

		_, err := driver.db.Exec(ctx, query.Raw("DELETE FROM "+query.Postgres.Quote(driver.documents)+" WHERE index_name = ?", schema.Name))

		return err
	})

	if err != nil {
		return fmt.Errorf("postgres search drop %s: %w", schema.Name, err)
	}

	return nil
}
