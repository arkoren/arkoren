// Package files owns the CLI confined filesystem and exclusive creation mechanisms.
package files

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"arkoren.dev/cmd/arkoren/internal/console"
)

// Destination pins an existing verified parent directory across preflight
// and exclusive creation. Retaining the handle prevents parent-path redirection.
type Destination struct {
	// Path is the absolute diagnostic Path, never used to reopen the parent.
	Path string

	// Parent owns a directory handle; closeCertificateFiles releases it.
	Parent *os.Root

	// Mode distinguishes public certificate output from private key output.
	Mode fs.FileMode
}

// Finish writes one PEM and syncs it before close. Accepting the
// narrow write/sync boundary permits deterministic short-write and sync-failure
// tests without mutable hooks or exposing filesystem operations to the CLI API.
func Finish(file interface {
	io.Writer
	Sync() error
}, content []byte) error {
	written, err := file.Write(content)

	if err != nil {
		return err
	}

	if written != len(content) {
		return io.ErrShortWrite
	}

	return file.Sync()
}

// File is one fully rendered file. Paths are relative, fixed template
// paths; content is formatted before the first filesystem modification.
type File struct {
	// Path identifies the generated file beneath the selected destination.
	Path string
	// Content contains the final bytes, including gofmt for Go source.
	Content []byte
}

// CheckDirectory rejects symlinks and non-directories in a destination's
// ancestry. Missing paths are valid because planning must not create them.
func CheckDirectory(ctx context.Context, path string) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}

		info, err := os.Lstat(path)

		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("inspect %s: %w", path, err)
		}

		if err == nil && !info.IsDir() {
			return console.UsageError("destination path %s is a symlink or not a directory", path)
		}

		parent := filepath.Dir(path)

		if parent == path {
			return nil
		}

		path = parent
	}
}

// Paths exposes the deterministic file inventory only when requested.
func Paths(inventory []File) []string {
	paths := make([]string, 0, len(inventory))

	for _, file := range inventory {
		paths = append(paths, filepath.ToSlash(file.Path))
	}

	return paths
}

// OpenDirectory walks from the filesystem root with scoped handles. Each
// component must be a real directory with the identity checked before opening.
// Each OpenRoot confines the next step to its parent. Read-only callers pass
// create=false so status and dry-run have no side effects; creation uses umask.
func OpenDirectory(ctx context.Context, path string, create bool) (*os.Root, error) {
	anchor := filepath.VolumeName(path) + string(filepath.Separator)
	root, err := os.OpenRoot(anchor)

	if err != nil {
		return nil, err
	}

	for part := range strings.SplitSeq(strings.TrimPrefix(path, anchor), string(filepath.Separator)) {
		next, err := descend(ctx, root, part, create)
		root.Close()

		if err != nil {
			return nil, err
		}

		root = next
	}

	return root, nil
}

// descend creates or opens exactly one directory under a scoped parent.
// Lstat prevents following existing links; OpenRoot prevents escaping parent
// if another process replaces an entry between the check and the open. The
// opened directory's identity must also match the real directory we checked,
// so a symlink swap to a different descendant cannot redirect later writes.
func descend(ctx context.Context, root *os.Root, name string, create bool) (*os.Root, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	if create {
		err := root.Mkdir(name, 0o755)

		if err != nil && !errors.Is(err, os.ErrExist) {
			return nil, err
		}
	}

	info, err := root.Lstat(name)

	if err != nil {
		return nil, err
	}

	if !info.IsDir() {
		return nil, console.UsageError("refusing symlink or non-directory %s", name)
	}

	next, err := root.OpenRoot(name)

	if err != nil {
		return nil, err
	}

	opened, err := next.Stat(".")

	if err != nil {
		next.Close()

		return nil, err
	}

	if !os.SameFile(info, opened) {
		next.Close()

		return nil, console.UsageError("destination directory %s changed during creation", name)
	}

	return next, nil
}

// Write creates files exclusively, never truncating even a file that appears
// after preflight. A failed write can leave newly created files/directories;
// they are deliberately not broadly removed because another process may own
// concurrent changes. The error names the file for manual recovery.
func Write(ctx context.Context, folder string, inventory []File) error {
	for _, file := range inventory {
		path := filepath.Join(folder, file.Path)

		if err := WriteFile(ctx, path, file.Content); err != nil {
			return fmt.Errorf("create %s (partial scaffold may remain): %w", path, err)
		}
	}

	return nil
}

// WriteFile holds a scoped parent for exclusive creation. O_EXCL rejects
// regular-file and symlink collisions atomically; Close errors are returned
// because delayed filesystem failures must not be reported as success.
// The shared creation primitive protects .env at creation: POSIX 0600 or a
// protected current-user-only Windows DACL, independently of parent inheritance.
func WriteFile(ctx context.Context, path string, content []byte) error {
	root, err := OpenDirectory(ctx, filepath.Dir(path), true)

	if err != nil {
		return err
	}

	// This closes only the directory handle; the created file's Close error is returned below.
	defer root.Close()

	if err := ctx.Err(); err != nil {
		return err
	}

	mode := os.FileMode(0o644)

	if filepath.Base(path) == ".env" {
		mode = 0o600
	}

	file, err := Create(Destination{Path: path, Parent: root, Mode: mode})

	if err != nil {
		return err
	}

	_, err = file.Write(content)

	return errors.Join(err, file.Close())
}
