package storage

import (
	"context"
	"crypto/rand"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strings"

	"arkoren.dev/core/service/storage/internal/transfer"
)

// PutFile stores an uploaded file under dir with a generated name and returns
// the stored object's metadata. The key is dir + "/" + name, or just name when
// dir is empty; a non-empty dir must satisfy [ValidateKey] (so no trailing
// slash), otherwise the error wraps [ErrInvalidKey] and nothing is read.
//
// The name is 26 random lowercase base32 characters, making collisions extremely
// unlikely; the underlying Put still replaces a colliding key. The client's
// filename never reaches the key. The extension comes from content detected in
// the first 512 bytes with [http.DetectContentType], not the client's filename
// or Content-Type header. HTML named photo.png can therefore be stored as .html;
// sniffing identifies a format, not a safe payload. Serve untrusted files through
// Download or Inline's restrictive presentation, not an unrestricted static route
// on your application origin. Unknown or application/octet-stream content gets
// no extension. Keep the original file.Filename in your own database if needed.
// file must be non-nil; validate the form field's presence before calling.
//
// The upload is passed to the driver as the file's seekable [multipart.File],
// so a driver that can use a known length, such as the AWS driver, streams
// from disk. A non-seekable reader handed straight to [Storage.Put] is
// buffered in memory there instead.
//
// Cancellation is checked before opening the multipart source. The helper closes
// its read-only source handle on every path, best-effort: a close failure does not
// undo an already successful storage write and is not a reason to repeat it.
//
// Example:
//
// With file obtained from an already validated multipart form:
//
//	info, err := store.PutFile(ctx, "avatars", file)
//	if err != nil {
//		return err
//	}
//	fmt.Println(info.Size)
func (storage *Storage) PutFile(ctx context.Context, dir string, file *multipart.FileHeader) (ObjectInfo, error) {
	if err := ctx.Err(); err != nil {
		return ObjectInfo{}, err
	}

	if dir != "" {
		if err := ValidateKey(dir); err != nil {
			return ObjectInfo{}, err
		}
	}

	contents, err := file.Open()

	if err != nil {
		return ObjectInfo{}, fmt.Errorf("open upload %q: %w", file.Filename, err)
	}

	defer func() {
		// This read-only handle cannot change the completed storage outcome.
		_ = contents.Close()
	}()

	head, err := transfer.ReadHead(contents)

	if err != nil {
		return ObjectInfo{}, fmt.Errorf("read upload %q: %w", file.Filename, err)
	}

	if _, err := contents.Seek(0, io.SeekStart); err != nil {
		return ObjectInfo{}, fmt.Errorf("rewind upload %q: %w", file.Filename, err)
	}

	key := strings.ToLower(rand.Text()) + transfer.Extension(http.DetectContentType(head))

	if dir != "" {
		key = dir + "/" + key
	}

	return storage.Put(ctx, key, contents)
}

// PutFileAs stores an uploaded file at exactly key, replacing any existing
// object, and returns the stored object's metadata. Use it when the key is
// derived from your own data, such as "avatars/<user id>.png". The key is not
// derived from the upload, so choose the extension yourself and do not take
// it from file.Filename; see [Storage.PutFile] for why.
//
// Like PutFile it passes the file's seekable [multipart.File] to the driver and
// closes that source best-effort. Cancellation is checked before opening it;
// cleanup cannot undo a successful write and never changes its reported outcome.
// file must be a non-nil header from a validated multipart form.
//
// Example:
//
// With a validated non-nil file header:
//
//	info, err := store.PutFileAs(ctx, "avatars/"+userID+".png", file)
//	if err != nil {
//		return err
//	}
//	fmt.Println(info.Key)
func (storage *Storage) PutFileAs(ctx context.Context, key string, file *multipart.FileHeader) (ObjectInfo, error) {
	if err := validateObjectKey(ctx, key); err != nil {
		return ObjectInfo{}, err
	}

	contents, err := file.Open()

	if err != nil {
		return ObjectInfo{}, fmt.Errorf("open upload %q: %w", file.Filename, err)
	}

	defer func() {
		// This read-only handle cannot change the completed storage outcome.
		_ = contents.Close()
	}()

	return storage.Put(ctx, key, contents)
}
