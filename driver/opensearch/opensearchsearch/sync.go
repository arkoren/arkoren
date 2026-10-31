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

// Sync creates the index of every schema with one shard, the text analysis,
// and the mapping of its fields, or, when the index exists, updates its
// mapping and the schema stored with it. When the stored schema changed, Sync
// reindexes the existing documents in place with an update-by-query and waits
// for it, so a field that becomes filterable or sortable covers them too; on a
// large index that takes a while. A field that exists as a keyword cannot become searchable, because
// OpenSearch cannot change a field's type; Sync then fails and names the
// index, and the way out is [Driver.Drop] followed by Sync and a reload.
// Mapping edits are not atomic with reindexing: a failure can leave native
// mapping changes visible. The completed schema checkpoint is published only
// after reindexing succeeds, so a failed attempt remains retryable. Serialize
// schema deployments and external administration; Check is not a transaction.
//
// Example:
//
//	if err := driver.Sync(ctx, []search.Schema{Products.Schema()}); err != nil {
//		return err
//	}
func (driver *Driver) Sync(ctx context.Context, schemas []search.Schema) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	for _, schema := range schemas {
		name, err := driver.indexName(schema.Name)

		if err != nil {
			return err
		}

		operation := "sync " + name

		reply, err := driver.doJSON(ctx, http.MethodPut, "/"+name, nil, index.Body(schema))

		if err != nil {
			return err
		}

		var created struct {
			// Error distinguishes an existing index from other create failures.
			Error failure `json:"error"`
		}

		if reply.status == http.StatusBadRequest && json.Unmarshal(reply.body, &created) == nil && created.Error.Type == "resource_already_exists_exception" {
			if err := driver.update(ctx, name, schema); err != nil {
				return fmt.Errorf("opensearch: %s: %w", operation, err)
			}

			continue
		}

		if err := reply.err(operation); err != nil {
			return err
		}
	}

	return nil
}

// update applies schema to the existing index. When the stored schema
// differs, it updates the mapping and then reindexes the documents in place,
// so fields that became filterable, sortable, or searchable cover documents
// written before the change. An unchanged schema costs one read.
func (driver *Driver) update(ctx context.Context, name string, schema search.Schema) error {
	reply, err := driver.do(ctx, http.MethodGet, "/"+name+"/_mapping", nil, "", nil)

	if err != nil {
		return err
	}

	if err := reply.err("read mapping"); err != nil {
		return err
	}

	stored, found, err := index.StoredSchema(reply.body)

	if err != nil {
		return err
	}

	if found && stored.Equal(schema) {
		return nil
	}

	// Leave the completed checkpoint unchanged until existing documents have
	// picked up the new mapping. A failed attempt must remain retryable.
	mapping := index.MappingBody(schema)
	delete(mapping, "_meta")

	if err := driver.applyMapping(ctx, name, mapping); err != nil {
		return err
	}

	params := url.Values{
		"conflicts":           {"proceed"},
		"refresh":             {"true"},
		"wait_for_completion": {"true"},
	}
	reply, err = driver.do(ctx, http.MethodPost, "/"+name+"/_update_by_query", params, "", nil)

	if err != nil {
		return err
	}

	if err := reply.err("reindex"); err != nil {
		return err
	}

	return driver.applyMapping(ctx, name, map[string]any{"_meta": map[string]any{index.SchemaMeta: schema}})
}

// applyMapping applies one native mapping edit and reports incompatible field
// changes with the application-owned rebuild remedy. Metadata-only checkpoint
// publication uses this same boundary after a successful reindex.
func (driver *Driver) applyMapping(ctx context.Context, name string, mapping map[string]any) error {
	reply, err := driver.doJSON(ctx, http.MethodPut, "/"+name+"/_mapping", nil, mapping)

	if err != nil {
		return err
	}

	if reply.status == http.StatusBadRequest {
		return fmt.Errorf("the mapping cannot change a field's type, drop the index and sync again: %w", reply.err("update mapping"))
	}

	return reply.err("update mapping")
}

// Check reads the schema stored in each index's mapping and compares it with
// schemas. An index without a stored schema, one that was not created by
// [Driver.Sync], counts as changed.
// This compares the last completed driver checkpoint, not every native mapping
// setting. Concurrent external administration can invalidate the observation.
//
// Example:
//
//	if err := driver.Check(ctx, []search.Schema{Products.Schema()}); err != nil {
//		return err
//	}
func (driver *Driver) Check(ctx context.Context, schemas []search.Schema) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	for _, schema := range schemas {
		name, err := driver.indexName(schema.Name)

		if err != nil {
			return err
		}

		reply, err := driver.do(ctx, http.MethodGet, "/"+name+"/_mapping", nil, "", nil)

		if err != nil {
			return err
		}

		if err := reply.err("check " + name); err != nil {
			return err
		}

		stored, found, err := index.StoredSchema(reply.body)

		if err != nil {
			return err
		}

		if !found || !stored.Equal(schema) {
			return fmt.Errorf("opensearch: index %q: %w", name, search.ErrSchemaChanged)
		}
	}

	return nil
}

// Drop deletes the index. A missing index is not an error.
// This is destructive: rebuildable source data remains the application's duty.
//
// Example:
//
//	if err := driver.Drop(ctx, Products.Schema()); err != nil {
//		return err
//	}
func (driver *Driver) Drop(ctx context.Context, schema search.Schema) error {
	name, err := driver.indexName(schema.Name)

	if err != nil {
		return err
	}

	reply, err := driver.do(ctx, http.MethodDelete, "/"+name, nil, "", nil)

	if err != nil {
		return err
	}

	if reply.status == http.StatusNotFound {
		return nil
	}

	return reply.err("drop " + name)
}
