// Package filesystemstorage provides a local filesystem storage driver.
//
// Objects are regular files beneath a root directory, replaced atomically by
// Put. [Driver.Start] creates the root and opens it as an [os.Root]; every
// later file operation goes through that handle, so a symlink beneath the root
// that points outside it can not be read, written, deleted, or listed through
// the driver. Operations called before Start or after [Driver.Stop] fail with
// an error that is not [storage.ErrNotFound]. Open and Stat wrap
// [storage.ErrNotFound] when a key is absent.
//
// List walks only the directory its prefix implies and returns regular files
// sorted by key.
// All valid .storage- keys are ordinary objects. Unpublished upload files use
// names outside the portable key grammar and are not listed. Legacy staging
// files with valid names cannot be distinguished from application objects;
// inspect any leftovers before removing them rather than deleting by prefix.
//
// Keys and listing prefixes use the portable storage validators on every
// platform. Windows device aliases and filename metacharacters are rejected even
// on Unix; ordinary names such as COM0 and COM10 remain valid. Existing paths
// rejected by these rules are left untouched but cannot be accessed or listed
// through the driver. Stop writers and back up the root before migrating with
// native filesystem tools. The application owns this migration and must update
// stored key references; the driver never cleans or renames them automatically.
//
// Open and Stat reject non-regular entries. Directories remain missing logical
// objects; other special entries report operational errors. Unix FIFO opening
// does not wait for a writer. Stop does not join application work: finish active
// operations first. A reader returned by Open remains usable after Stop.
//
// Example:
//
//	err := app.Run(ctx, func(compose *application.Composer) {
//		driver := compose.Must(filesystemstorage.New("./data/uploads"))
//		compose.Cleanup(driver.Stop)
//		compose.Startup(driver.Start)
//	})
//	if err != nil {
//		return err
//	}
package filesystemstorage
