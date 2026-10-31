# Filesystem object storage

`arkoren.dev/driver/filesystem/filesystemstorage` stores regular files beneath
one local directory. Use it for a single-host application or persistent local
development. Install it with `go get arkoren.dev/driver/filesystem`.

With an existing application and operation context, construct the driver and
wrap it for portable storage operations:

```go
import (
	"arkoren.dev/core/service/storage"
	"arkoren.dev/driver/filesystem/filesystemstorage"
)

driver, err := filesystemstorage.New("./data/uploads")
files := storage.New(driver)
```

Register `driver.Start` for application startup and `driver.Stop` for cleanup
before using `files`. Construction only resolves the root path; Start creates
the directory if needed and opens its containment handle. Finish application
operations before Stop; bodies already returned by Open remain readable.

Keys and listing prefixes follow the portable storage rules on every platform,
including Unix. Windows device aliases such as `NUL`, `PRN.txt`, and `COM1`, and
filename metacharacters such as `?` and `*`, are invalid. Ordinary counterexamples
such as `COM0` and `COM10` remain valid; accepted key bytes are not normalized.

Before upgrading an existing root, inventory its names with native filesystem
tools. Newly invalid paths remain on disk, but portable operations reject their
keys and listings omit them. Stop writers, back up the root, and deliberately
migrate affected names and application references with native filesystem tools.
The driver does not rename or delete these paths automatically. See
[`storage.ValidateKey`](https://pkg.go.dev/arkoren.dev/core/service/storage#ValidateKey)
and [`storage.ValidatePrefix`](https://pkg.go.dev/arkoren.dev/core/service/storage#ValidatePrefix)
for the complete restrictions.

Put publishes an object by renaming a complete neighboring upload. Failed
readers leave the previous object intact. Staging names are invalid portable
keys, so incomplete uploads are not listed and valid `.storage-` object keys
remain available. Older versions hid every file with that prefix; inspect
legacy leftovers before removing anything, because valid application objects
can use the same prefix. The driver does not delete legacy files automatically.

Open and Stat accept regular files. Directories are logical prefixes rather
than objects. Other special entries return operational errors; on Unix, opening
a FIFO does not wait for a writer. Containment prevents symlinks from escaping
the root; Open and Stat can follow links whose targets remain inside it.
Listings include regular entries only, sorted by full key, with a default page
limit of 100. Concurrent deletes of the same absent object succeed. Signed URLs
are unsupported and return `storage.ErrUnsupported` after normal validation.

See the [filesystem guide](../../../cmd/arkoren/skills/arkoren/references/guides/file-storage/drivers.md#filesystem)
for application wiring and the [API reference](https://pkg.go.dev/arkoren.dev/driver/filesystem/filesystemstorage)
for ownership, error, and pagination details.
