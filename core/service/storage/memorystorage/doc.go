// Package memorystorage provides a concurrent in-memory storage driver. Its Open and
// Stat methods wrap [storage.ErrNotFound] when a key is absent. [Builder.ListLimit]
// changes the page size List uses when a request sets no limit.
//
// New takes Builder policies for ListLimit. They run directly once in order on
// a fresh draft; nil entries panic and expanded nil/empty slices supply no edits.
// Nonpositive limits resolve to 100 after the final policy. Publication detaches
// settings, so retained drafts cannot change pagination. Construction performs
// no object I/O and starts no background work.
//
// Open returns an independent seekable snapshot; callers close it. Put never
// closes a supplied reader and checks cancellation between reads, but cannot
// interrupt a reader already blocked inside Read. Listing checks cancellation
// while scanning and copying metadata, returns no partial page on cancellation,
// and retains ordinary lexical cursor behavior. Sorting and mutex waits remain
// synchronous; pages across separate calls are not a transactional snapshot.
//
// Example:
//
//	driver := memorystorage.New(func(store *memorystorage.Builder) {
//		store.ListLimit(25)
//	})
//	store := storage.New(driver)
//	_ = store.Driver()
package memorystorage
