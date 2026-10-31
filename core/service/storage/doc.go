// Package storage defines portable streaming object storage contracts.
//
// Keys are literal UTF-8 relative paths of at most 1024 bytes with slash-separated
// segments. [ValidateKey] rejects traversal/empty segments, control characters,
// Win32 filename metacharacters, trailing dots/spaces, and reserved device names.
// Device aliases with extensions are excluded for older Windows compatibility
// even where Windows 11 permits them. Prefixes are empty or use the same syntax
// with one optional final slash. Names are never cleaned, renamed, or case-folded;
// filesystem-specific length and case-sensitivity limits can still apply.
//
//	files := storage.New(memorystorage.New())
//	info, err := files.PutBytes(ctx, "reports/a.txt", []byte("report"))
//	if err != nil {
//		return err
//	}
//	fmt.Println(info.Size) // 6
//
// Every [Driver] implementation maps a missing key from Open or Stat to an
// error wrapping [ErrNotFound]. [Storage] preserves that driver-owned
// identity; it does not infer missing objects from provider errors. Delete
// succeeds when a key is missing. Open returns a body that callers must close.
// Put consumes its reader once and, on success, makes the entire supplied object
// current. List cursors are opaque, valid only with the same driver and prefix,
// and have no ordering or snapshot guarantee while objects change.
//
// [Storage.Walk] flattens pages into a collection.TryLazySlice of ObjectInfo.
// It defers listing until consumption, follows opaque continuations, and stops
// requesting pages as soon as the consumer stops. Limit remains a per-page
// limit; Take limits consumed objects. Each traversal starts fresh I/O with the
// original options and captured context, without a snapshot guarantee. Errors
// terminate traversal; Items returns the successful prefix with the error.
// Cancellation is checked before page requests and between buffered objects.
// Page preparation can still scan/sort matching keys or perform S3 read-ahead.
//
//	objects, err := files.Walk(ctx, storage.ListOptions{Prefix: "reports/", Limit: 100}).Take(10).Items()
//	if err != nil {
//		return err // objects is an incomplete prefix, not a complete listing.
//	}
//	for _, info := range objects {
//		fmt.Println(info.Key)
//	}
//
// [Driver.SignURL] returns a time-limited URL that lets a client with no
// credentials GET or PUT one key directly against the backend, for example a
// browser upload or a download link. Signing does not check that the object
// exists. Drivers whose backend has no presigned access, such as the memory
// and filesystem drivers, return an error wrapping [ErrUnsupported]; a TTL
// that is not positive or exceeds the driver's maximum wraps [ErrInvalidTTL].
// Cancellation precedes malformed input. The facade delegates maximum-TTL,
// method, binding, and capability checks together so a backend limit cannot be
// preempted by a later method/binding error. Signed URLs are bearer credentials;
// authorize their recipients and keep them out of ordinary logs and telemetry.
//
// [Storage.PutFile] and [Storage.PutFileAs] store an uploaded
// *[multipart.FileHeader], and [Storage.Download] and [Storage.Inline] serve
// an object as an HTTP response with Range support when the driver's body is
// seekable.
// Both helpers close the body and preserve observed transfer and cleanup errors
// through errors.Is. Errors returned after response commitment are for logging;
// never append a replacement problem body or automatically retry the transfer.
// Upload helpers close their read-only multipart sources best-effort without
// changing an already completed storage write's result.
//
// # Telemetry
//
// With [Builder.Telemetry], or [telemetry.Default] when it is not set, each
// [Storage] operation emits a client span "storage {operation}" and the
// arkoren.storage.operations and arkoren.storage.operation.duration metrics,
// identically for every driver. [Builder.Name] tells apart several stores. Keys,
// prefixes, cursors and signed URLs are never recorded, and a missing object
// is not an error. Disabled telemetry costs nothing.
// Walk reuses List instrumentation once per requested page, with no separate
// traversal span or metric.
//
//	files := storage.New(driver, func(store *storage.Builder) {
//		store.Telemetry(app.Telemetry())
//	})
//	_ = files.Driver()
//
// # Events
//
// With [Builder.Events], [Storage.Put] and the helpers built on it publish
// [ObjectWritten] ("storage.object.written") and [Storage.Delete] publishes
// [ObjectDeleted] ("storage.object.deleted") after success, for every
// driver, with the store name and key. Reads, lists, and signed URLs
// publish nothing.
//
//	files := storage.New(driver, func(store *storage.Builder) {
//		store.Events(app.Events())
//	})
//	_ = files.Driver()
//
// New takes Builder policies for Telemetry, Name and Events. They run directly
// once in order on a fresh draft; nil entries panic and expanded nil/empty slices
// supply no edits. Publication detaches settings, not the live driver or shared
// services. Retained drafts cannot reconfigure Storage. Nil telemetry resolves
// the process default on each operation.
package storage
