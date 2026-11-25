package filesecret

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"

	"arkoren.dev/core/service/secret"
)

// MaxSize is the largest file, in bytes, that [Driver.Get] reads. A secret
// file is a token, key or small document; the cap stops a misconfigured
// reference from loading an arbitrary large file into memory.
const MaxSize = 1 << 20

// Driver reads secrets from files. The reference name is a path, absolute or
// relative to the process working directory, so "file:/run/secrets/token"
// reaches it as "/run/secrets/token" once a secretmux driver has routed on the
// scheme. Symbolic links are followed, which is how container platforms mount
// secrets. Every Get opens and reads the file again, so a rotated file is seen
// on the next read. The source is read-only: Put, Update and Delete fail with
// [secret.ErrUnsupported], and it has no versions, so asking for a concrete
// version is [secret.ErrNotFound]. It holds no state, is safe for concurrent
// use and needs no Start or Stop. Construct it with [New].
//
// The driver applies no path policy: whoever controls a reference chooses the
// file, so never build references from untrusted input. Files larger than
// [MaxSize] are an error.
//
// Example:
//
//	driver := filesecret.New()
//	value, err := driver.Get(ctx, secret.Reference{Name: "/run/secrets/db-password"})
//	if err != nil {
//		return err
//	}
//	useCredential(value.Data)
type Driver struct{}

// New creates a file driver. It has no settings and performs no I/O.
//
// Example:
//
//	driver := filesecret.New()
func New() *Driver {
	return &Driver{}
}

// Get returns the contents of the file named by reference.Name as bytes owned
// by the caller, with trailing newlines kept; decoding into a string drops one
// at the [secret.Store] layer. A missing file returns an error wrapping
// [secret.ErrNotFound]. A file larger than [MaxSize], a directory, an empty
// name and any other read failure are errors. A non-empty reference.Version
// returns [secret.ErrNotFound], because files have no versions. An
// already-canceled context returns its error before touching the file system.
// Errors include the path but never contents.
//
// Example:
//
//	value, err := driver.Get(ctx, secret.Reference{Name: "/run/secrets/db-password"})
//	if errors.Is(err, secret.ErrNotFound) {
//		return errors.New("mount the db-password secret")
//	}
//	if err != nil {
//		return err
//	}
//	useCredential(value.Data)
func (driver *Driver) Get(ctx context.Context, reference secret.Reference) (secret.Value, error) {
	if err := ctx.Err(); err != nil {
		return secret.Value{}, err
	}

	if reference.Name == "" {
		return secret.Value{}, errors.New("filesecret: empty path")
	}

	if reference.Version != "" {
		return secret.Value{}, fmt.Errorf("filesecret: files have no versions: %w", secret.ErrNotFound)
	}

	file, err := os.Open(reference.Name)

	if errors.Is(err, fs.ErrNotExist) {
		return secret.Value{}, fmt.Errorf("filesecret: %w: %w", secret.ErrNotFound, err)
	}

	if err != nil {
		return secret.Value{}, fmt.Errorf("filesecret: %w", err)
	}

	defer file.Close() // Read-only handle: a close failure cannot lose data.

	data, err := io.ReadAll(io.LimitReader(file, MaxSize+1))

	if err != nil {
		return secret.Value{}, fmt.Errorf("filesecret: read %s: %w", reference.Name, err)
	}

	if len(data) > MaxSize {
		clear(data)

		return secret.Value{}, fmt.Errorf("filesecret: %s is larger than %d bytes", reference.Name, MaxSize)
	}

	return secret.Value{Data: data}, nil
}

// Put fails with [secret.ErrUnsupported]: files are read-only. An
// already-canceled context returns its error instead.
//
// Example:
//
//	_, err := driver.Put(ctx, "/run/secrets/token", []byte(token))
//	_ = errors.Is(err, secret.ErrUnsupported) // true
func (driver *Driver) Put(ctx context.Context, _ string, _ []byte) (secret.Value, error) {
	return secret.Value{}, unsupported(ctx, "put")
}

// Update fails with [secret.ErrUnsupported]: files are read-only. An
// already-canceled context returns its error instead.
//
// Example:
//
//	_, err := driver.Update(ctx, "/run/secrets/token", []byte(token))
//	_ = errors.Is(err, secret.ErrUnsupported) // true
func (driver *Driver) Update(ctx context.Context, _ string, _ []byte) (secret.Value, error) {
	return secret.Value{}, unsupported(ctx, "update")
}

// Delete fails with [secret.ErrUnsupported]: files are read-only and the driver
// never removes one. An already-canceled context returns its error instead.
//
// Example:
//
//	err := driver.Delete(ctx, "/run/secrets/token")
//	_ = errors.Is(err, secret.ErrUnsupported) // true
func (driver *Driver) Delete(ctx context.Context, _ string) error {
	return unsupported(ctx, "delete")
}

// unsupported returns the context's error when it is done and otherwise the
// read-only failure for operation, so a canceled context takes precedence as
// the Driver contract requires.
func unsupported(ctx context.Context, operation string) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	return fmt.Errorf("filesecret: %s: files are read-only: %w", operation, secret.ErrUnsupported)
}

var _ secret.Driver = (*Driver)(nil)
