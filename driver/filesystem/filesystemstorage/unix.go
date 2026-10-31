//go:build unix

package filesystemstorage

import (
	"os"
	"syscall"
)

// objectReadFlags avoids waiting for a writer if an existing entry is a FIFO,
// including one substituted between path lookup and open. Open checks the opened
// handle's mode before returning it. O_NONBLOCK does not change regular-file I/O.
const objectReadFlags = os.O_RDONLY | syscall.O_NONBLOCK
