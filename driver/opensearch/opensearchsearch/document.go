package opensearchsearch

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"fmt"
	"net/http"
	"net/url"
	"slices"

	"arkoren.dev/core/service/search"
	"arkoren.dev/driver/opensearch/opensearchsearch/internal/index"
)

// Put writes docs with a bulk request per chunk of documents, replacing
// documents with the same ID. Only the last request waits for the refresh that
// makes the documents searchable, which also covers the earlier ones. It
// fails with [search.ErrNotFound] before writing when the index does not
// exist, because OpenSearch would otherwise create it with a dynamic mapping.
// Per-document failures, such as a value that does not fit the mapping, are
// reported with the first failing ID; documents of earlier chunks stay
// written. The physical _source wraps original JSON and separate scalar fields;
// Search restores original bytes, and no application field names are reserved.
//
// Example:
//
//	docs := []search.Document{{ID: "p1", Source: []byte(`{"title":"red shoes"}`)}}
//	if err := driver.Put(ctx, Products.Schema(), docs); err != nil {
//		return err
//	}
func (driver *Driver) Put(ctx context.Context, schema search.Schema, docs []search.Document) error {
	name, err := driver.indexName(schema.Name)

	if err != nil {
		return err
	}

	reply, err := driver.do(ctx, http.MethodHead, "/"+name, nil, "", nil)

	if err != nil {
		return err
	}

	if reply.status == http.StatusNotFound {
		return fmt.Errorf("opensearch: put %s: %w: index was never synced", name, search.ErrNotFound)
	}

	if err := reply.err("put " + name); err != nil {
		return err
	}

	entries := make([][]byte, len(docs))

	for i, doc := range docs {
		action, err := bulkAction("index", name, doc.ID)

		if err != nil {
			return err
		}

		source, err := index.EncodeDocument(doc)

		if err != nil {
			return err
		}

		entries[i] = slices.Concat(action, source, []byte("\n"))
	}

	return driver.bulk(ctx, "put "+name, entries)
}

// Delete removes the documents with ids with a bulk request per chunk. Missing
// IDs are not an error; a missing index fails with [search.ErrNotFound].
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

	name, err := driver.indexName(schema.Name)

	if err != nil {
		return err
	}

	if len(ids) == 0 {
		reply, err := driver.do(ctx, http.MethodHead, "/"+name, nil, "", nil)

		if err != nil {
			return err
		}

		if reply.status == http.StatusNotFound {
			return fmt.Errorf("opensearch: delete %s: %w", name, search.ErrNotFound)
		}

		return reply.err("delete " + name)
	}

	entries := make([][]byte, len(ids))

	for i, id := range ids {
		if entries[i], err = bulkAction("delete", name, id); err != nil {
			return err
		}
	}

	return driver.bulk(ctx, "delete "+name, entries)
}

// bulkAction returns the action line of a bulk entry, newline included.
func bulkAction(action, name, id string) ([]byte, error) {
	line, err := json.Marshal(map[string]any{action: map[string]any{"_index": name, "_id": id}})

	if err != nil {
		return nil, fmt.Errorf("opensearch: encode bulk action: %w", err)
	}

	return append(line, '\n'), nil
}

// bulk sends entries in requests of at most [maxBulkDocuments] entries or
// about [maxBulkBytes] bytes. The last request has refresh=wait_for, so the
// call returns when every write is searchable. An entry is a complete bulk
// entry: an action line, plus a source line for an index action.
func (driver *Driver) bulk(ctx context.Context, operation string, entries [][]byte) error {
	for len(entries) > 0 {
		var body bytes.Buffer
		count := 0

		for count < len(entries) && count < maxBulkDocuments && (count == 0 || body.Len()+len(entries[count]) <= maxBulkBytes) {
			body.Write(entries[count])
			count++
		}

		entries = entries[count:]

		params := url.Values{}

		if len(entries) == 0 {
			params.Set("refresh", "wait_for")
		}

		reply, err := driver.do(ctx, http.MethodPost, "/_bulk", params, "application/x-ndjson", body.Bytes())

		if err != nil {
			return err
		}

		if err := reply.bulkError(operation); err != nil {
			return err
		}
	}

	return nil
}
