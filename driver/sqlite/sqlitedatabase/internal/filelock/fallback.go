//go:build !(darwin || linux || freebsd || netbsd || openbsd || dragonfly)

package filelock

import "context"

// Acquire has no cross-process implementation on this platform. It returns a
// nil release function and reports the lock as acquired, so SQLite locks span
// only the current process here.
func Acquire(context.Context, string, bool) (func() error, bool, error) {
	return nil, true, nil
}
