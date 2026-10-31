package storage

import (
	"context"

	"arkoren.dev/foundation/collection"
)

// Walk traverses object metadata in the driver's lexical source order, starting
// with options.Cursor and retaining options.Prefix and options.Limit for every
// page. Limit bounds each page, not the total number of objects; nonpositive
// values retain the driver default. Use Take to bound the number consumed.
//
// Construction performs no listing or validation. Each traversal calls [Storage.List]
// afresh with the captured context and original options, so validation and telemetry
// apply per page. Reusing a walk does not refresh an expired context or provide a
// snapshot: concurrent storage changes can alter objects observed across pages or
// traversals. As with List, callers must supply a non-nil context.
//
// Walk follows opaque NextCursor values, including after empty or short pages,
// until the cursor is empty. It checks cancellation before page requests and
// between buffered objects. A cancellation or page error is yielded once with a
// zero ObjectInfo, then traversal ends without retry. Items returns the successful
// prefix alongside that error; callers must not treat it as a complete listing.
//
// A false yield stops immediately without another page request. This bounds future
// facade calls, not work already done preparing a page: memory and filesystem
// drivers can scan and sort matching keys, and S3 can read ahead for continuation.
//
// Example:
//
//	files := storage.New(memorystorage.New())
//	objects, err := files.Walk(ctx, storage.ListOptions{Prefix: "reports/", Limit: 20}).Take(5).Items()
//	if err != nil {
//		return err // objects contains only the successfully traversed prefix.
//	}
//	for _, info := range objects {
//		fmt.Println(info.Key)
//	}
func (storage *Storage) Walk(ctx context.Context, options ListOptions) collection.TryLazySlice[ObjectInfo] {
	return collection.NewTryLazySlice(func(yield func(ObjectInfo, error) bool) {
		// current is private to this traversal; repeated traversal restarts at
		// the caller's cursor rather than the previous traversal's last page.
		current := options

		for {
			page, err := storage.List(ctx, current)

			if err != nil {
				yield(ObjectInfo{}, err)

				return
			}

			if !yieldWalkPage(ctx, page.Objects, yield) {
				return
			}

			if page.NextCursor == "" {
				return
			}

			current.Cursor = page.NextCursor
		}
	})
}

// yieldWalkPage delivers buffered objects without additional I/O. Cancellation
// discards the remaining buffer and emits one terminal error; false means the
// caller stopped or cancellation ended traversal, so no further page is fetched.
func yieldWalkPage(ctx context.Context, objects []ObjectInfo, yield func(ObjectInfo, error) bool) bool {
	for _, info := range objects {
		if err := ctx.Err(); err != nil {
			yield(ObjectInfo{}, err)

			return false
		}

		if !yield(info, nil) {
			return false
		}
	}

	return true
}
