//go:build !unix

package filesystemstorage

import "os"

// objectReadFlags uses native read-only opening on platforms without Unix FIFOs.
// Open still checks the opened handle and never returns a non-regular object.
const objectReadFlags = os.O_RDONLY
