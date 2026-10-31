package filesystemstorage

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"strings"

	"arkoren.dev/core/service/storage"
)

// validateListing rejects nonportable prefixes and cursors before walking disk.
// Cursors must remain in the requested literal prefix; nonpositive page limits
// are valid and receive their default only when the result page is built.
func validateListing(ctx context.Context, options storage.ListOptions) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	if err := storage.ValidatePrefix(options.Prefix); err != nil {
		return err
	}

	if options.Cursor == "" {
		return nil
	}

	if err := storage.ValidateKey(options.Cursor); err != nil {
		return err
	}

	if !strings.HasPrefix(options.Cursor, options.Prefix) {
		return fmt.Errorf("invalid storage cursor %q: %w", options.Cursor, storage.ErrInvalidKey)
	}

	return nil
}

// listingObjects collects regular portable objects beneath the directory implied
// by Prefix. Invalid staging keys never enter the result, while valid filenames
// with the same prefix remain visible. A missing starting directory is empty;
// nested traversal or metadata failures remain errors instead of partial pages.
func listingObjects(ctx context.Context, root fs.FS, options storage.ListOptions) ([]storage.ObjectInfo, error) {
	walkRoot := "."

	if index := strings.LastIndex(options.Prefix, "/"); index >= 0 {
		walkRoot = options.Prefix[:index]
	}

	var objects []storage.ObjectInfo

	err := fs.WalkDir(root, walkRoot, func(key string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		if err := ctx.Err(); err != nil {
			return err
		}

		if !entry.Type().IsRegular() || storage.ValidateKey(key) != nil {
			return nil
		}

		if !strings.HasPrefix(key, options.Prefix) || key <= options.Cursor {
			return nil
		}

		info, err := entry.Info()

		if err != nil {
			return err
		}

		objects = append(objects, objectInfo(key, info))

		return nil
	})

	var failure *fs.PathError

	if errors.As(err, &failure) && failure.Path == walkRoot && errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}

	return objects, err
}
