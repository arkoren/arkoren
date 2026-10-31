package meilisearchsearch

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"arkoren.dev/core/service/search"
	"arkoren.dev/driver/meilisearch/meilisearchsearch/internal/index"
)

// Sync creates the Meilisearch index of every schema that has none, with the
// primary key arkoren_id, and applies the settings of each schema that differ
// from the stored ones. Settings that already match are not sent again, so a
// repeated Sync does nothing. A changed searchable, filterable, or sortable
// attribute list makes Meilisearch reindex the index, which waits in the task
// queue behind earlier tasks and can be slow on large indexes; documents are
// kept.
//
// It returns an error when an index of that uid exists with another primary
// key, which means it was not created by this driver.
// Metadata and task observations are not atomic with external administration;
// serialize deployments and do not change a managed index concurrently.
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
		uid := driver.uid(schema)

		if err := driver.ensureIndex(ctx, uid); err != nil {
			return fmt.Errorf("meilisearch sync %s: %w", uid, err)
		}

		want := index.SettingsFor(schema)
		got, err := driver.settings(ctx, uid)

		if err != nil {
			return fmt.Errorf("meilisearch sync %s: read settings: %w", uid, err)
		}

		if want.Equal(got) {
			continue
		}

		if err := driver.run(ctx, http.MethodPatch, "/indexes/"+uid+"/settings", want); err != nil {
			return fmt.Errorf("meilisearch sync %s: apply settings: %w", uid, err)
		}
	}

	return nil
}

// ensureIndex creates only missing indexes and checks the winning creator's
// primary key after asynchronous completion. It cannot prevent later external
// administration; callers must serialize schema deployment with such changes.
func (driver *Driver) ensureIndex(ctx context.Context, uid string) error {
	err := driver.checkPrimaryKey(ctx, uid)

	if !errors.Is(err, search.ErrNotFound) {
		return err
	}

	err = driver.run(ctx, http.MethodPost, "/indexes", map[string]string{"uid": uid, "primaryKey": index.IDField})

	if err != nil && !hasCode(err, codeIndexAlreadyExists) {
		return fmt.Errorf("create index: %w", err)
	}

	return driver.checkPrimaryKey(ctx, uid)
}

// settings reads the managed settings of an index.
func (driver *Driver) settings(ctx context.Context, uid string) (index.Settings, error) {
	var settings index.Settings
	err := driver.do(ctx, http.MethodGet, "/indexes/"+uid+"/settings", nil, &settings)

	return settings, err
}

// Check compares the managed settings of every schema's index with what
// [Driver.Sync] would apply, changing nothing. It returns an error wrapping
// [search.ErrNotFound] for an index that does not exist and one wrapping
// [search.ErrSchemaChanged] for one whose searchable, filterable, or sortable
// attributes, ranking rules, typo tolerance, pagination limit, or primary key differ.
// Other settings, and other indexes on the server, are ignored.
//
// Example:
//
//	if err := driver.Check(ctx, []search.Schema{Products.Schema()}); err != nil {
//		return err // Synchronize or investigate incompatible native metadata.
//	}
func (driver *Driver) Check(ctx context.Context, schemas []search.Schema) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	for _, schema := range schemas {
		uid := driver.uid(schema)

		if err := driver.checkPrimaryKey(ctx, uid); err != nil {
			return fmt.Errorf("meilisearch check %s: %w", uid, err)
		}

		got, err := driver.settings(ctx, uid)

		if err != nil {
			return fmt.Errorf("meilisearch check %s: %w", uid, notFound(err, uid))
		}

		if !index.SettingsFor(schema).Equal(got) {
			return fmt.Errorf("meilisearch check %s: %w: settings differ from the definition", uid, search.ErrSchemaChanged)
		}
	}

	return nil
}

// checkPrimaryKey reads index metadata rather than inferring compatibility from
// search settings. Missing indexes preserve ErrNotFound; foreign keys are schema
// differences and must never be silently taken over by Sync or Check.
func (driver *Driver) checkPrimaryKey(ctx context.Context, uid string) error {
	var metadata struct {
		// PrimaryKey is the server's document-identity field, not a search setting.
		PrimaryKey string `json:"primaryKey"`
	}

	if err := driver.do(ctx, http.MethodGet, "/indexes/"+uid, nil, &metadata); err != nil {
		return notFound(err, uid)
	}

	if metadata.PrimaryKey != index.IDField {
		return fmt.Errorf("primary key %q, want %q: %w", metadata.PrimaryKey, index.IDField, search.ErrSchemaChanged)
	}

	return nil
}

// requireIndex returns an error wrapping [search.ErrNotFound] when the index
// does not exist. Writes need it because Meilisearch creates a missing index
// when documents arrive, with settings the contract does not know.
func (driver *Driver) requireIndex(ctx context.Context, uid string) error {
	return notFound(driver.do(ctx, http.MethodGet, "/indexes/"+uid, nil, nil), uid)
}

// Drop deletes the Meilisearch index with its documents and settings and
// returns when the task has finished. A missing index succeeds.
// Dropping is destructive and does not remove the application's source data.
//
// Example:
//
//	if err := driver.Drop(ctx, Products.Schema()); err != nil {
//		return err
//	}
func (driver *Driver) Drop(ctx context.Context, schema search.Schema) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	uid := driver.uid(schema)

	// Deleting a missing index is a failed task, or in older versions an error
	// response; both carry the same code.
	if err := driver.run(ctx, http.MethodDelete, "/indexes/"+uid, nil); err != nil && !hasCode(err, codeIndexNotFound) {
		return fmt.Errorf("meilisearch drop %s: %w", uid, err)
	}

	return nil
}
