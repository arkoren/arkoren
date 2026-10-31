package storage

import (
	"errors"
	"fmt"
	"net/http"

	"arkoren.dev/core/service/storage/internal/transfer"
	"arkoren.dev/foundation/problem"
)

// ErrObjectNotFound is the 404 problem [Storage.Download] and
// [Storage.Inline] return for a missing object, so handlers can return it
// directly and the router renders it. The returned error also wraps the
// driver's error, so errors.Is(err, [ErrNotFound]) stays true.
//
// Example:
//
//	failure := storage.ErrObjectNotFound.Derive(func(details *problem.Builder) {
//		details.Detail("The request cannot be completed.")
//	})
//	_ = failure.Status()
var ErrObjectNotFound = problem.New(func(details *problem.Builder) {
	details.Type("tag:arkoren.dev,2026-09:core:service/storage:object-not-found")
	details.Title("File not found")
	details.Detail("The requested file does not exist.")
	details.Status(http.StatusNotFound)
})

// Download writes the object at key to w as a file download: the response
// carries Content-Disposition: attachment so browsers save it instead of
// showing it. filename is the name offered to the user; empty uses the last
// segment of key. Handlers return the result, so a missing object becomes a
// 404 response. Authorize object access before calling; key validation checks
// syntax, not ownership or permissions.
//
// The Content-Type comes from filename's extension, falling back to the type
// detected from the first 512 bytes. When the driver's [Object.Contents]
// implements [io.Seeker] (the memory and filesystem drivers do), Download uses
// [http.ServeContent], which adds Range requests (206), HEAD, Last-Modified
// and If-Modified-Since (304). Otherwise it streams the body with
// Content-Length from [ObjectInfo.Size]; HEAD gets headers only, and Range and
// conditional requests are not supported. Contents is always closed.
//
// Download sets X-Content-Type-Options: nosniff on object responses. Open and
// initial sniff/rewind failures return before commitment, with [ErrObjectNotFound]
// for a missing key. ServeContent retains its native HTTP error responses for
// invalid ranges or failed size probes. Source, seek, write, short-write, and
// close failures remain discoverable with errors.Is; premature EOF during a
// seekable transfer is [io.ErrUnexpectedEOF]. Cleanup errors join transfer errors.
// These errors can arrive after response commitment: return them for logging,
// never append a replacement response or automatically retry a completed download.
//
// Example:
//
//	handler := func(w http.ResponseWriter, r *http.Request) error {
//		return store.Download(w, r, "reports/2024.pdf", "Q1 report.pdf")
//	}
//	_ = handler // Register with the application's error-returning router.
func (storage *Storage) Download(w http.ResponseWriter, r *http.Request, key, filename string) error {
	return storage.serve(w, r, key, transfer.Presentation{Filename: filename, Disposition: "attachment"})
}

// Inline writes the object at key to w for display in the browser
// (Content-Disposition: inline), such as an image in a page. It behaves like
// [Storage.Download] and adds protections for content that could run in your
// origin: every object response carries Content-Security-Policy: sandbox, and only
// an allow-list of types is shown inline: raster images (PNG, JPEG, GIF,
// WebP, AVIF, BMP, ICO), application/pdf, audio/*, video/* and text/plain.
// Every other type (HTML, SVG, XML, JavaScript, CSS, WebAssembly, JSON, and
// anything unknown) is forced to attachment disposition, because a browser
// could run it or load it with <script src> or <link> from your origin. The
// name offered is the last segment of key.
// Authorize object access before calling; the sandbox is not access control.
//
// Example:
//
//	handler := func(w http.ResponseWriter, r *http.Request) error {
//		return store.Inline(w, r, "avatars/"+r.PathValue("id")+".png")
//	}
//	_ = handler // Authorize access before exposing the object.
func (storage *Storage) Inline(w http.ResponseWriter, r *http.Request, key string) error {
	return storage.serve(w, r, key, transfer.Presentation{Disposition: "inline"})
}

// serve owns one opened object's cleanup for Download and Inline. It prepares
// the body before committing headers, applies the requested security policy,
// and joins cleanup errors without replacing a transfer failure or HTTP body.
func (storage *Storage) serve(w http.ResponseWriter, r *http.Request, key string, presentation transfer.Presentation) (err error) {
	object, err := storage.Open(r.Context(), key)

	if errors.Is(err, ErrNotFound) {
		return ErrObjectNotFound.Derive(func(details *problem.Builder) {
			details.Error(err)
		})
	}

	if err != nil {
		return err
	}

	defer func() {
		if e := object.Contents.Close(); e != nil {
			err = errors.Join(err, fmt.Errorf("close %q: %w", key, e))
		}
	}()

	return transfer.Serve(w, r, key, object.Contents, transfer.Info{
		Key:        object.Key,
		Size:       object.Size,
		ModifiedAt: object.ModifiedAt,
	}, presentation)
}
