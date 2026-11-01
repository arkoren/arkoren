package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"arkoren.dev/cmd/arkoren/internal/console"
	"arkoren.dev/cmd/arkoren/internal/files"
	"arkoren.dev/foundation/certificate"
)

// writePrivateKey creates one private file exclusively. On write/sync/close
// failure it leaves the newly created file for manual inspection, never deletes
// or overwrites a path that another process might have replaced in the meantime.
// The shared creation primitive applies 0600 on POSIX and a protected current-
// user-only DACL on Windows atomically, rather than repairing permissions later.
func writePrivateKey(path string, encoded []byte) error {
	parent, err := os.OpenRoot(filepath.Dir(path))

	if err != nil {
		return fmt.Errorf("open private key parent: %w", err)
	}

	defer parent.Close() // The read-only directory handle has no buffered output.

	file, err := files.Create(files.Destination{Path: path, Parent: parent, Mode: 0o600})

	if err != nil {
		return fmt.Errorf("create private key file: %w", err)
	}

	written, err := file.Write(encoded)

	if written != len(encoded) && err == nil {
		err = io.ErrShortWrite
	}

	if err == nil {
		err = file.Sync()
	}

	if err := errors.Join(err, file.Close()); err != nil {
		return fmt.Errorf("private key file may be incomplete; inspect it before use: %w", err)
	}

	return nil
}

// prepareCertificateFiles validates both explicit output names without writes.
// Neither stdout, existing entries (including dangling links), linked parents,
// nor same-parent names differing only by case are accepted. Case folding avoids
// ambiguous pair destinations on case-insensitive filesystems.
func prepareCertificateFiles(ctx context.Context, paths certificatePaths, dir string) ([]files.Destination, error) {
	if paths.cert == "" || paths.key == "" {
		return nil, console.UsageError("certificate generation requires --cert FILE and --key FILE")
	}

	if paths.cert == "-" || paths.key == "-" {
		return nil, console.UsageError("certificate generation requires separate files, not stdout")
	}

	inventory := []files.Destination{
		{Path: keyPath(dir, paths.cert), Mode: 0o644},
		{Path: keyPath(dir, paths.key), Mode: 0o600},
	}

	for index := range inventory {
		if err := preflightCertificateFile(ctx, &inventory[index]); err != nil {
			closeCertificateFiles(inventory)

			return nil, err
		}
	}

	if err := distinctCertificateFiles(inventory); err != nil {
		closeCertificateFiles(inventory)

		return nil, err
	}

	return inventory, nil
}

// preflightCertificateFile opens only real directories and requires an absent
// destination leaf. The retained parent is closed by the caller even on error.
func preflightCertificateFile(ctx context.Context, file *files.Destination) error {
	path, err := filepath.Abs(file.Path)

	if err != nil {
		return fmt.Errorf("resolve certificate output %q: %w", file.Path, err)
	}

	file.Path = path
	file.Parent, err = files.OpenDirectory(ctx, filepath.Dir(path), false)

	if err != nil {
		return fmt.Errorf("open certificate output parent %q: %w", filepath.Dir(path), err)
	}

	_, err = file.Parent.Lstat(filepath.Base(path))

	if err == nil {
		return fmt.Errorf("certificate output %q already exists: %w", path, os.ErrExist)
	}

	if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect certificate output %q: %w", path, err)
	}

	return nil
}

// distinctCertificateFiles compares real parent identities, not just lexical
// paths. This also rejects alias paths to one absent output before either write.
func distinctCertificateFiles(inventory []files.Destination) error {
	left, err := inventory[0].Parent.Stat(".")

	if err != nil {
		return fmt.Errorf("inspect output parent %q: %w", inventory[0].Path, err)
	}

	right, err := inventory[1].Parent.Stat(".")

	if err != nil {
		return fmt.Errorf("inspect output parent %q: %w", inventory[1].Path, err)
	}

	if os.SameFile(left, right) && strings.EqualFold(filepath.Base(inventory[0].Path), filepath.Base(inventory[1].Path)) {
		return console.UsageError("certificate and private key require distinct destinations: %q and %q",
			inventory[0].Path, inventory[1].Path)
	}

	return nil
}

// closeCertificateFiles releases directory handles, including partial preflight.
// Closing a read-only directory has no pending output; failures cannot change
// the command's persistence outcome and are intentionally ignored.
func closeCertificateFiles(inventory []files.Destination) {
	for _, file := range inventory {
		if file.Parent != nil {
			_ = file.Parent.Close() // No writes are buffered on these directory handles.
		}
	}
}

// persistCertificate writes the certificate then its matching private key.
// Pair writes are not atomic. Any failure names both exact paths because either
// file may remain; paths are never removed after a concurrent replacement.
func persistCertificate(
	ctx context.Context,
	inventory []files.Destination,
	identity certificate.Identity,
	env console.Environment,
) error {
	material := [][]byte{identity.Bundle().PEM(), identity.PrivatePEM()}

	for index, file := range inventory {
		if err := writeCertificateFile(ctx, file, material[index]); err != nil {
			return fmt.Errorf("certificate pair incomplete; inspect %q and %q; files may remain: %w",
				inventory[0].Path, inventory[1].Path, err)
		}
	}

	return env.Result(map[string]any{
		"created": true, "certificate": inventory[0].Path, "key": inventory[1].Path, "trust_changed": false,
	})
}

// writeCertificateFile still uses exclusive creation after preflight. Private
// access protection is applied at creation before any bytes are written. An error leaves
// only this invocation's new file, possibly empty or incomplete, for inspection.
func writeCertificateFile(ctx context.Context, destination files.Destination, content []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	file, err := files.Create(destination)

	if err != nil {
		return fmt.Errorf("create certificate output %q: %w", destination.Path, err)
	}

	err = files.Finish(file, content)

	return errors.Join(err, file.Close())
}
