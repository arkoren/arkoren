package storage_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"mime/multipart"
	"regexp"
	"strings"
	"testing"

	"arkoren.dev/core/service/storage"
	"arkoren.dev/core/service/storage/memorystorage"
)

// uploadHeader parses a multipart body and returns its only file header.
func uploadHeader(t *testing.T, filename, content string) *multipart.FileHeader {
	t.Helper()

	var body bytes.Buffer

	writer := multipart.NewWriter(&body)

	part, err := writer.CreateFormFile("file", filename)

	if err != nil {
		t.Fatal(err)
	}

	if _, err := part.Write([]byte(content)); err != nil {
		t.Fatal(err)
	}

	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}

	form, err := multipart.NewReader(&body, writer.Boundary()).ReadForm(1 << 20)

	if err != nil {
		t.Fatal(err)
	}

	return form.File["file"][0]
}

// checkPutFileKeyShape stores content under dir and fails the test unless the
// generated key matches keyRegexp and the stored object holds content.
func checkPutFileKeyShape(t *testing.T, dir, filename, content, keyRegexp string) {
	t.Helper()

	store := storage.New(memorystorage.New())

	info, err := store.PutFile(context.Background(), dir, uploadHeader(t, filename, content))

	if err != nil {
		t.Fatal(err)
	}

	if !regexp.MustCompile(keyRegexp).MatchString(info.Key) {
		t.Errorf("key = %q, want match %s", info.Key, keyRegexp)
	}

	if info.Size != int64(len(content)) {
		t.Errorf("Size = %d, want %d", info.Size, len(content))
	}

	assertStored(t, store, info.Key, content)
}

// TestPutFileKeyShapePNG verifies the generated key: directory, 26 lowercase
// base32 characters, and an extension from the sniffed content rather than
// the client's filename.
func TestPutFileKeyShapePNG(t *testing.T) {
	t.Parallel()

	checkPutFileKeyShape(t, "avatars", "me.png", "\x89PNG\r\n\x1a\nrest", `^avatars/[a-z2-7]{26}\.png$`)
}

// TestPutFileKeyShapeClientExtensionNotTrusted verifies the generated key: directory, 26 lowercase
// base32 characters, and an extension from the sniffed content rather than
// the client's filename.
func TestPutFileKeyShapeClientExtensionNotTrusted(t *testing.T) {
	t.Parallel()

	checkPutFileKeyShape(t, "uploads", "evil.html", "\xff\xd8\xff\xe0rest", `^uploads/[a-z2-7]{26}\.jpg$`)
}

// TestPutFileKeyShapeHTMLContentIgnoresPNGName verifies the generated key: directory, 26 lowercase
// base32 characters, and an extension from the sniffed content rather than
// the client's filename.
func TestPutFileKeyShapeHTMLContentIgnoresPNGName(t *testing.T) {
	t.Parallel()

	checkPutFileKeyShape(t, "uploads", "pic.png", "<!doctype html><p>x", `^uploads/[a-z2-7]{26}\.html$`)
}

// TestPutFileKeyShapePDF verifies the generated key: directory, 26 lowercase
// base32 characters, and an extension from the sniffed content rather than
// the client's filename.
func TestPutFileKeyShapePDF(t *testing.T) {
	t.Parallel()

	checkPutFileKeyShape(t, "a/b", "x", "%PDF-1.4", `^a/b/[a-z2-7]{26}\.pdf$`)
}

// TestPutFileKeyShapePlainText verifies the generated key: directory, 26 lowercase
// base32 characters, and an extension from the sniffed content rather than
// the client's filename.
func TestPutFileKeyShapePlainText(t *testing.T) {
	t.Parallel()

	checkPutFileKeyShape(t, "t", "x.bin", "hello", `^t/[a-z2-7]{26}\.txt$`)
}

// TestPutFileKeyShapeZip verifies the generated key: directory, 26 lowercase
// base32 characters, and an extension from the sniffed content rather than
// the client's filename.
func TestPutFileKeyShapeZip(t *testing.T) {
	t.Parallel()

	checkPutFileKeyShape(t, "z", "x.docx", "PK\x03\x04rest", `^z/[a-z2-7]{26}\.zip$`)
}

// TestPutFileKeyShapeUnknownHasNoExtension verifies the generated key: directory, 26 lowercase
// base32 characters, and an extension from the sniffed content rather than
// the client's filename.
func TestPutFileKeyShapeUnknownHasNoExtension(t *testing.T) {
	t.Parallel()

	checkPutFileKeyShape(t, "bin", "x.exe", "\x00\x01\x02\x03binary", `^bin/[a-z2-7]{26}$`)
}

// TestPutFileKeyShapeRootDir verifies the generated key: directory, 26 lowercase
// base32 characters, and an extension from the sniffed content rather than
// the client's filename.
func TestPutFileKeyShapeRootDir(t *testing.T) {
	t.Parallel()

	checkPutFileKeyShape(t, "", "x.png", "\x89PNG\r\n\x1a\n", `^[a-z2-7]{26}\.png$`)
}

// TestPutFileNamesAreUnique verifies two uploads never share a key.
func TestPutFileNamesAreUnique(t *testing.T) {
	t.Parallel()

	store := storage.New(memorystorage.New())
	header := uploadHeader(t, "a.txt", "same")

	first, err := store.PutFile(context.Background(), "d", header)

	if err != nil {
		t.Fatal(err)
	}

	second, err := store.PutFile(context.Background(), "d", header)

	if err != nil {
		t.Fatal(err)
	}

	if first.Key == second.Key {
		t.Errorf("both uploads stored at %q", first.Key)
	}
}

// TestPutFileStoresWholeContentAfterSniff verifies the sniff read is rewound
// for content longer than the sniff window.
func TestPutFileStoresWholeContentAfterSniff(t *testing.T) {
	t.Parallel()

	content := "%PDF-1.4" + strings.Repeat("y", 4000)
	store := storage.New(memorystorage.New())

	info, err := store.PutFile(context.Background(), "docs", uploadHeader(t, "a.pdf", content))

	if err != nil {
		t.Fatal(err)
	}

	assertStored(t, store, info.Key, content)
}

// checkPutFileInvalidDir fails the test unless storing into dir is rejected
// with ErrInvalidKey.
func checkPutFileInvalidDir(t *testing.T, dir string) {
	t.Helper()

	store := storage.New(memorystorage.New())

	_, err := store.PutFile(context.Background(), dir, uploadHeader(t, "a.txt", "x"))

	if !errors.Is(err, storage.ErrInvalidKey) {
		t.Fatalf("err = %v, want ErrInvalidKey", err)
	}
}

// TestPutFileInvalidDirAbsolute verifies a bad directory is rejected before storing.
func TestPutFileInvalidDirAbsolute(t *testing.T) {
	t.Parallel()

	checkPutFileInvalidDir(t, "/abs")
}

// TestPutFileInvalidDirTrailingSlash verifies a bad directory is rejected before storing.
func TestPutFileInvalidDirTrailingSlash(t *testing.T) {
	t.Parallel()

	checkPutFileInvalidDir(t, "a/")
}

// TestPutFileInvalidDirParentTraversal verifies a bad directory is rejected before storing.
func TestPutFileInvalidDirParentTraversal(t *testing.T) {
	t.Parallel()

	checkPutFileInvalidDir(t, "../up")
}

// TestPutFileInvalidDirEmptySegment verifies a bad directory is rejected before storing.
func TestPutFileInvalidDirEmptySegment(t *testing.T) {
	t.Parallel()

	checkPutFileInvalidDir(t, "a//b")
}

// TestPutFileAs verifies the exact key is used and invalid keys are rejected.
func TestPutFileAs(t *testing.T) {
	t.Parallel()

	store := storage.New(memorystorage.New())

	info, err := store.PutFileAs(context.Background(), "avatars/42.png", uploadHeader(t, "evil.html", "content"))

	if err != nil {
		t.Fatal(err)
	}

	if info.Key != "avatars/42.png" {
		t.Errorf("Key = %q", info.Key)
	}

	assertStored(t, store, "avatars/42.png", "content")

	_, err = store.PutFileAs(context.Background(), "../x", uploadHeader(t, "a.txt", "x"))

	if !errors.Is(err, storage.ErrInvalidKey) {
		t.Errorf("err = %v, want ErrInvalidKey", err)
	}
}

// assertStored fails unless key holds want.
func assertStored(t *testing.T, store *storage.Storage, key, want string) {
	t.Helper()

	object, err := store.Open(context.Background(), key)

	if err != nil {
		t.Fatal(err)
	}

	defer object.Contents.Close()

	got, err := io.ReadAll(object.Contents)

	if err != nil {
		t.Fatal(err)
	}

	if string(got) != want {
		t.Errorf("stored %d bytes %q, want %d bytes %q", len(got), got, len(want), want)
	}
}

// TestUploadCancellationPrecedesInvalidInput avoids opening a multipart source
// or returning a key error when its operation was already canceled.
func TestUploadCancellationPrecedesInvalidInput(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	store := storage.New(memorystorage.New())

	if _, err := store.PutFile(ctx, "../invalid", &multipart.FileHeader{}); !errors.Is(err, context.Canceled) {
		t.Errorf("PutFile = %v, want context.Canceled", err)
	}

	if _, err := store.PutFileAs(ctx, "../invalid", &multipart.FileHeader{}); !errors.Is(err, context.Canceled) {
		t.Errorf("PutFileAs = %v, want context.Canceled", err)
	}
}
