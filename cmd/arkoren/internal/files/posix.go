//go:build !windows

package files

import (
	"os"
	"path/filepath"
)

// Create exclusively creates a leaf under the retained parent.
// POSIX creation applies 0600 to private files before they become visible;
// umask can only remove access. Public files request the destination's 0644.
// Certificate output, application keys, and scaffold .env all use this primitive
// so every private output has the same exclusive creation-time access contract.
func Create(destination Destination) (*os.File, error) {
	return destination.Parent.OpenFile(
		filepath.Base(destination.Path), os.O_WRONLY|os.O_CREATE|os.O_EXCL, destination.Mode)
}
