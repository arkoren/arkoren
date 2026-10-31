package storage_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"arkoren.dev/core/http/router"
	"arkoren.dev/core/service/storage"
	"arkoren.dev/core/service/storage/memorystorage"
	"arkoren.dev/foundation/problem"
)

// streamDriver wraps the memory driver and hides Seek on Contents, so
// downloads take the non-seekable path. It records whether Contents was closed.
type streamDriver struct {
	// Driver supplies metadata and writes beneath the non-seekable Open override.
	*memorystorage.Driver
	// closed records whether the caller released the object body.
	closed *bool
}

// streamBody is a read-only, non-seekable body that records Close.
type streamBody struct {
	// Reader exposes only sequential reads, forcing the response streaming path.
	io.Reader
	// closed records whether the caller released the object body.
	closed *bool
}

// Close records that the body was closed.
func (body streamBody) Close() error {
	*body.closed = true

	return nil
}

// Open returns the stored object with a non-seekable body.
func (driver streamDriver) Open(ctx context.Context, key string) (storage.Object, error) {
	object, err := driver.Driver.Open(ctx, key)

	if err != nil {
		return object, err
	}

	object.Contents = streamBody{Reader: object.Contents, closed: driver.closed}

	return object, nil
}

// seekableStore and streamStore return a store holding key -> content, whose
// bodies do and do not implement io.Seeker.
func seekableStore(t *testing.T, key, content string) *storage.Storage {
	t.Helper()

	store := storage.New(memorystorage.New())

	if _, err := store.PutBytes(context.Background(), key, []byte(content)); err != nil {
		t.Fatal(err)
	}

	return store
}

// streamStore seeds an object and wraps its body as non-seekable. Closing the
// body sets closed, allowing the caller to check response cleanup.
func streamStore(t *testing.T, key, content string, closed *bool) *storage.Storage {
	t.Helper()

	driver := memorystorage.New()

	if _, err := driver.Put(context.Background(), key, strings.NewReader(content)); err != nil {
		t.Fatal(err)
	}

	return storage.New(streamDriver{Driver: driver, closed: closed})
}

// checkDownloadHeaders downloads key from a seekable or, when streamed is
// true, a non-seekable store and fails the test unless the response carries the
// wanted content type and disposition, the security headers, and the content.
func checkDownloadHeaders(t *testing.T, streamed bool, key, content, filename, contentType, disposition string) {
	t.Helper()

	var store *storage.Storage

	if streamed {
		store = streamStore(t, key, content, new(bool))
	} else {
		store = seekableStore(t, key, content)
	}

	rec := httptest.NewRecorder()

	if err := store.Download(rec, httptest.NewRequest(http.MethodGet, "/", nil), key, filename); err != nil {
		t.Fatal(err)
	}

	assertHeader(t, rec, "Content-Type", contentType)
	assertHeader(t, rec, "Content-Disposition", disposition)
	assertHeader(t, rec, "X-Content-Type-Options", "nosniff")
	assertHeader(t, rec, "Content-Security-Policy", "")

	if rec.Code != http.StatusOK || rec.Body.String() != content {
		t.Errorf("status %d body %q, want 200 %q", rec.Code, rec.Body.String(), content)
	}
}

// TestDownloadHeadersFilenameFromKeySeekable verifies content type, disposition and security
// headers for a seekable body.
func TestDownloadHeadersFilenameFromKeySeekable(t *testing.T) {
	t.Parallel()

	checkDownloadHeaders(t, false, "docs/a.pdf", "%PDF-1.4", "", "application/pdf", "attachment; filename=a.pdf")
}

// TestDownloadHeadersFilenameFromKeyStream verifies content type, disposition and security
// headers for a streamed body.
func TestDownloadHeadersFilenameFromKeyStream(t *testing.T) {
	t.Parallel()

	checkDownloadHeaders(t, true, "docs/a.pdf", "%PDF-1.4", "", "application/pdf", "attachment; filename=a.pdf")
}

// TestDownloadHeadersExplicitFilenameSeekable verifies content type, disposition and security
// headers for a seekable body.
func TestDownloadHeadersExplicitFilenameSeekable(t *testing.T) {
	t.Parallel()

	checkDownloadHeaders(t, false, "docs/blob", "x", "report.txt", "text/plain; charset=utf-8", "attachment; filename=report.txt")
}

// TestDownloadHeadersExplicitFilenameStream verifies content type, disposition and security
// headers for a streamed body.
func TestDownloadHeadersExplicitFilenameStream(t *testing.T) {
	t.Parallel()

	checkDownloadHeaders(t, true, "docs/blob", "x", "report.txt", "text/plain; charset=utf-8", "attachment; filename=report.txt")
}

// TestDownloadHeadersSniffedWhenNoExtensionSeekable verifies content type, disposition and security
// headers for a seekable body.
func TestDownloadHeadersSniffedWhenNoExtensionSeekable(t *testing.T) {
	t.Parallel()

	checkDownloadHeaders(t, false, "blob", "\x89PNG\r\n\x1a\nrest", "", "image/png", "attachment; filename=blob")
}

// TestDownloadHeadersSniffedWhenNoExtensionStream verifies content type, disposition and security
// headers for a streamed body.
func TestDownloadHeadersSniffedWhenNoExtensionStream(t *testing.T) {
	t.Parallel()

	checkDownloadHeaders(t, true, "blob", "\x89PNG\r\n\x1a\nrest", "", "image/png", "attachment; filename=blob")
}

// TestDownloadHeadersQuotesAndSpacesSeekable verifies content type, disposition and security
// headers for a seekable body.
func TestDownloadHeadersQuotesAndSpacesSeekable(t *testing.T) {
	t.Parallel()

	checkDownloadHeaders(t, false, "k", "x", `my "file".txt`, "text/plain; charset=utf-8", `attachment; filename="my \"file\".txt"`)
}

// TestDownloadHeadersQuotesAndSpacesStream verifies content type, disposition and security
// headers for a streamed body.
func TestDownloadHeadersQuotesAndSpacesStream(t *testing.T) {
	t.Parallel()

	checkDownloadHeaders(t, true, "k", "x", `my "file".txt`, "text/plain; charset=utf-8", `attachment; filename="my \"file\".txt"`)
}

// TestDownloadHeadersNonASCIISeekable verifies content type, disposition and security
// headers for a seekable body.
func TestDownloadHeadersNonASCIISeekable(t *testing.T) {
	t.Parallel()

	checkDownloadHeaders(t, false, "k", "x", `résumé "final".txt`, "text/plain; charset=utf-8", `attachment; filename="r_sum_ \"final\".txt"; filename*=UTF-8''r%C3%A9sum%C3%A9%20%22final%22.txt`)
}

// TestDownloadHeadersNonASCIIStream verifies content type, disposition and security
// headers for a streamed body.
func TestDownloadHeadersNonASCIIStream(t *testing.T) {
	t.Parallel()

	checkDownloadHeaders(t, true, "k", "x", `résumé "final".txt`, "text/plain; charset=utf-8", `attachment; filename="r_sum_ \"final\".txt"; filename*=UTF-8''r%C3%A9sum%C3%A9%20%22final%22.txt`)
}

// TestDownloadHeadersControlCharactersSeekable verifies content type, disposition and security
// headers for a seekable body.
func TestDownloadHeadersControlCharactersSeekable(t *testing.T) {
	t.Parallel()

	checkDownloadHeaders(t, false, "k", "x", "a\r\nb.txt", "text/plain; charset=utf-8", `attachment; filename=a__b.txt; filename*=UTF-8''a%0D%0Ab.txt`)
}

// TestDownloadHeadersControlCharactersStream verifies content type, disposition and security
// headers for a streamed body.
func TestDownloadHeadersControlCharactersStream(t *testing.T) {
	t.Parallel()

	checkDownloadHeaders(t, true, "k", "x", "a\r\nb.txt", "text/plain; charset=utf-8", `attachment; filename=a__b.txt; filename*=UTF-8''a%0D%0Ab.txt`)
}

// assertHeader fails unless the recorded response header equals want.
func assertHeader(t *testing.T, rec *httptest.ResponseRecorder, name, want string) {
	t.Helper()

	if got := rec.Header().Get(name); got != want {
		t.Errorf("%s = %q, want %q", name, got, want)
	}
}

// checkMissingWritesNothing fails the test unless call reports a 404 problem
// matching ErrObjectNotFound and ErrNotFound and writes nothing to the response.
func checkMissingWritesNothing(t *testing.T, call func(http.ResponseWriter, *http.Request) error) {
	t.Helper()

	rec := httptest.NewRecorder()

	err := call(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	if !errors.Is(err, storage.ErrObjectNotFound) || !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("err = %v, want ErrObjectNotFound wrapping ErrNotFound", err)
	}

	details, ok := errors.AsType[problem.Details](err)

	if !ok || details.Status() != http.StatusNotFound {
		t.Errorf("err is not a 404 problem: %v", err)
	}

	if rec.Flushed || rec.Body.Len() != 0 || len(rec.Header()) != 0 {
		t.Errorf("response was written: flushed=%v body=%q headers=%v", rec.Flushed, rec.Body, rec.Header())
	}
}

// TestDownloadMissingWritesNothing verifies the 404 problem from Download still
// matches ErrNotFound and that nothing reached the response.
func TestDownloadMissingWritesNothing(t *testing.T) {
	t.Parallel()

	store := storage.New(memorystorage.New())

	checkMissingWritesNothing(t, func(w http.ResponseWriter, r *http.Request) error {
		return store.Download(w, r, "missing.txt", "")
	})
}

// TestInlineMissingWritesNothing verifies the 404 problem from Inline still
// matches ErrNotFound and that nothing reached the response.
func TestInlineMissingWritesNothing(t *testing.T) {
	t.Parallel()

	store := storage.New(memorystorage.New())

	checkMissingWritesNothing(t, func(w http.ResponseWriter, r *http.Request) error {
		return store.Inline(w, r, "missing.txt")
	})
}

// TestDownloadInvalidKeyIsNotNotFound verifies key validation errors pass through.
func TestDownloadInvalidKeyIsNotNotFound(t *testing.T) {
	t.Parallel()

	err := storage.New(memorystorage.New()).Download(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil), "../x", "")

	if !errors.Is(err, storage.ErrInvalidKey) || errors.Is(err, storage.ErrObjectNotFound) {
		t.Fatalf("err = %v, want ErrInvalidKey only", err)
	}
}

// TestDownloadRange verifies a seekable body answers a Range request.
func TestDownloadRange(t *testing.T) {
	t.Parallel()

	store := seekableStore(t, "digits.txt", "0123456789")
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("Range", "bytes=2-4")

	rec := httptest.NewRecorder()

	if err := store.Download(rec, r, "digits.txt", ""); err != nil {
		t.Fatal(err)
	}

	if rec.Code != http.StatusPartialContent || rec.Body.String() != "234" {
		t.Errorf("status %d body %q, want 206 234", rec.Code, rec.Body.String())
	}

	assertHeader(t, rec, "Content-Range", "bytes 2-4/10")
	assertHeader(t, rec, "Content-Disposition", "attachment; filename=digits.txt")
}

// TestDownloadConditional verifies If-Modified-Since yields 304 for seekable bodies.
func TestDownloadConditional(t *testing.T) {
	t.Parallel()

	store := seekableStore(t, "a.txt", "hello")

	first := httptest.NewRecorder()

	if err := store.Download(first, httptest.NewRequest(http.MethodGet, "/", nil), "a.txt", ""); err != nil {
		t.Fatal(err)
	}

	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("If-Modified-Since", first.Header().Get("Last-Modified"))

	rec := httptest.NewRecorder()

	if err := store.Download(rec, r, "a.txt", ""); err != nil {
		t.Fatal(err)
	}

	if rec.Code != http.StatusNotModified {
		t.Errorf("status = %d, want 304", rec.Code)
	}
}

// checkDownloadHead fails the test unless HEAD on a seekable or, when streamed
// is true, a non-seekable store returns headers and length without a body.
func checkDownloadHead(t *testing.T, streamed bool) {
	t.Helper()

	closed := new(bool)
	store := seekableStore(t, "a.txt", "hello")

	if streamed {
		store = streamStore(t, "a.txt", "hello", closed)
	}

	rec := httptest.NewRecorder()

	if err := store.Download(rec, httptest.NewRequest(http.MethodHead, "/", nil), "a.txt", ""); err != nil {
		t.Fatal(err)
	}

	if rec.Code != http.StatusOK || rec.Body.Len() != 0 {
		t.Errorf("status %d body %q, want 200 and no body", rec.Code, rec.Body)
	}

	assertHeader(t, rec, "Content-Length", "5")

	if streamed && !*closed {
		t.Error("Contents not closed")
	}
}

// TestDownloadHeadSeekable verifies HEAD returns headers and length without a
// body for a seekable store.
func TestDownloadHeadSeekable(t *testing.T) {
	t.Parallel()

	checkDownloadHead(t, false)
}

// TestDownloadHeadStream verifies HEAD returns headers and length without a
// body for a streamed store, and closes the contents.
func TestDownloadHeadStream(t *testing.T) {
	t.Parallel()

	checkDownloadHead(t, true)
}

// TestDownloadStreamKeepsSniffedBytes verifies a non-seekable body larger
// than the sniff window arrives intact, and is closed.
func TestDownloadStreamKeepsSniffedBytes(t *testing.T) {
	t.Parallel()

	content := "<!doctype html><p>" + strings.Repeat("x", 5000)
	closed := new(bool)
	store := streamStore(t, "blob", content, closed)

	rec := httptest.NewRecorder()

	if err := store.Download(rec, httptest.NewRequest(http.MethodGet, "/", nil), "blob", ""); err != nil {
		t.Fatal(err)
	}

	if rec.Body.String() != content {
		t.Errorf("body length %d, want %d", rec.Body.Len(), len(content))
	}

	assertHeader(t, rec, "Content-Type", "text/html; charset=utf-8")
	assertHeader(t, rec, "Content-Length", "5018")
	assertHeader(t, rec, "Accept-Ranges", "")

	if !*closed {
		t.Error("Contents not closed")
	}
}

// checkInline serves key through Inline and fails the test unless the response
// carries the wanted content type and disposition, the sandbox policy, and
// nosniff.
func checkInline(t *testing.T, key, content, contentType, disposition string) {
	t.Helper()

	store := seekableStore(t, key, content)

	rec := httptest.NewRecorder()

	if err := store.Inline(rec, httptest.NewRequest(http.MethodGet, "/", nil), key); err != nil {
		t.Fatal(err)
	}

	assertHeader(t, rec, "Content-Type", contentType)
	assertHeader(t, rec, "Content-Disposition", disposition)
	assertHeader(t, rec, "Content-Security-Policy", "sandbox")
	assertHeader(t, rec, "X-Content-Type-Options", "nosniff")
}

// TestInlineImage verifies the inline disposition and sandbox policy, and that
// active content types are forced to attachment.
func TestInlineImage(t *testing.T) {
	t.Parallel()

	checkInline(t, "pic.png", "\x89PNG\r\n\x1a\n", "image/png", "inline; filename=pic.png")
}

// TestInlinePDF verifies the inline disposition and sandbox policy, and that
// active content types are forced to attachment.
func TestInlinePDF(t *testing.T) {
	t.Parallel()

	checkInline(t, "doc.pdf", "%PDF-1.4", "application/pdf", "inline; filename=doc.pdf")
}

// TestInlineHTMLByExtension verifies the inline disposition and sandbox policy, and that
// active content types are forced to attachment.
func TestInlineHTMLByExtension(t *testing.T) {
	t.Parallel()

	checkInline(t, "page.html", "<p>hi", "text/html; charset=utf-8", "attachment; filename=page.html")
}

// TestInlineSVGByExtension verifies the inline disposition and sandbox policy, and that
// active content types are forced to attachment.
func TestInlineSVGByExtension(t *testing.T) {
	t.Parallel()

	checkInline(t, "logo.svg", "<svg/>", "image/svg+xml", "attachment; filename=logo.svg")
}

// TestInlineXHTMLByExtension verifies the inline disposition and sandbox policy, and that
// active content types are forced to attachment.
func TestInlineXHTMLByExtension(t *testing.T) {
	t.Parallel()

	checkInline(t, "page.xhtml", "<html/>", "application/xhtml+xml", "attachment; filename=page.xhtml")
}

// TestInlineXMLSniffed verifies the inline disposition and sandbox policy, and that
// active content types are forced to attachment.
func TestInlineXMLSniffed(t *testing.T) {
	t.Parallel()

	checkInline(t, "blob", `<?xml version="1.0"?><a/>`, "text/xml; charset=utf-8", "attachment; filename=blob")
}

// TestInlineHTMLSniffed verifies the inline disposition and sandbox policy, and that
// active content types are forced to attachment.
func TestInlineHTMLSniffed(t *testing.T) {
	t.Parallel()

	checkInline(t, "blob", "<!doctype html><p>", "text/html; charset=utf-8", "attachment; filename=blob")
}

// TestInlineJavaScriptIsAttachment verifies scripts cannot be loaded from the
// origin through an inline response.
func TestInlineJavaScriptIsAttachment(t *testing.T) {
	t.Parallel()

	checkInline(t, "app.js", "alert(1)", "text/javascript; charset=utf-8", "attachment; filename=app.js")
}

// TestInlineCSSIsAttachment verifies stylesheets are not served inline.
func TestInlineCSSIsAttachment(t *testing.T) {
	t.Parallel()

	checkInline(t, "site.css", "a{}", "text/css; charset=utf-8", "attachment; filename=site.css")
}

// TestInlineWasmIsAttachment verifies WebAssembly is not served inline.
func TestInlineWasmIsAttachment(t *testing.T) {
	t.Parallel()

	checkInline(t, "mod.wasm", "\x00asm", "application/wasm", "attachment; filename=mod.wasm")
}

// TestInlineVideo verifies video types are shown inline.
func TestInlineVideo(t *testing.T) {
	t.Parallel()

	checkInline(t, "clip.mp4", "\x00\x00\x00\x18ftypmp42", "video/mp4", "inline; filename=clip.mp4")
}

// TestInlinePlainText verifies text/plain is shown inline with its charset.
func TestInlinePlainText(t *testing.T) {
	t.Parallel()

	checkInline(t, "notes.txt", "hello", "text/plain; charset=utf-8", "inline; filename=notes.txt")
}

// closeFailureBody exposes a non-seekable stream whose cleanup can fail. The
// caller can observe both the returned error and whether cleanup was attempted.
type closeFailureBody struct {
	// Reader supplies the object bytes without exposing Seek.
	io.Reader

	// err is the failure returned by Close.
	err error

	// closed records resource release through the public object body.
	closed bool
}

// Close records the release attempt and returns the configured failure without
// changing the preceding read outcome.
func (body *closeFailureBody) Close() error {
	body.closed = true

	return body.err
}

// TestDownloadReturnsCloseFailure reports cleanup failure after a successful
// transfer instead of turning the completed HTTP response into silent success.
func TestDownloadReturnsCloseFailure(t *testing.T) {
	t.Parallel()

	want := errors.New("object close failed")
	body := &closeFailureBody{Reader: strings.NewReader("body"), err: want}
	store := storage.New(&driver{object: storage.Object{Size: 4, Contents: body}})
	recorder := httptest.NewRecorder()

	err := store.Download(recorder, httptest.NewRequest(http.MethodGet, "/", nil), "object.txt", "")

	if !errors.Is(err, want) || !body.closed {
		t.Fatalf("Download = %v, closed = %v; want close failure", err, body.closed)
	}

	if recorder.Code != http.StatusOK || recorder.Body.String() != "body" {
		t.Fatalf("response = %d %q, want completed body", recorder.Code, recorder.Body.String())
	}
}

// failingResponse preserves HTTP header/status observation but refuses body
// writes, like a client connection that fails after response commitment.
type failingResponse struct {
	// ResponseRecorder records the HTTP result before the failed write.
	*httptest.ResponseRecorder

	// err is the transport failure returned to the body producer.
	err error
}

// Write fails without accepting any bytes. The caller still owns the response
// and must not render a replacement body after commitment.
func (response failingResponse) Write([]byte) (int, error) {
	return 0, response.err
}

// TestDownloadSeekableWriteFailure exposes a transfer error swallowed by the
// void ServeContent API, while retaining its normal response status and headers.
func TestDownloadSeekableWriteFailure(t *testing.T) {
	t.Parallel()

	want := errors.New("client connection failed")
	recorder := httptest.NewRecorder()
	response := failingResponse{ResponseRecorder: recorder, err: want}
	store := seekableStore(t, "report.txt", "report")

	err := store.Download(response, httptest.NewRequest(http.MethodGet, "/", nil), "report.txt", "")

	if !errors.Is(err, want) {
		t.Fatalf("Download = %v, want transfer failure", err)
	}

	if recorder.Code != http.StatusOK || recorder.Header().Get("Content-Length") != "6" {
		t.Fatalf("response = %d %v, want ServeContent headers", recorder.Code, recorder.Header())
	}
}

// brokenSeekableBody reports a size from its embedded reader but fails reads,
// modeling an object whose remote contents become unavailable after opening.
type brokenSeekableBody struct {
	// Reader provides native size and range positioning without storing new data.
	*strings.Reader

	// err is the source failure returned by every read.
	err error

	// closeErr is an independent cleanup failure, if any.
	closeErr error
}

// Read exposes the configured failure without producing bytes.
func (body brokenSeekableBody) Read([]byte) (int, error) {
	return 0, body.err
}

// Close returns the configured cleanup outcome. No external resource is owned
// by this deterministic in-memory body.
func (body brokenSeekableBody) Close() error {
	return body.closeErr
}

// TestDownloadSeekableTruncation reports premature EOF instead of treating a
// response shorter than its advertised size as a successful transfer.
func TestDownloadSeekableTruncation(t *testing.T) {
	t.Parallel()

	body := brokenSeekableBody{Reader: strings.NewReader("report"), err: io.EOF}
	store := storage.New(&driver{object: storage.Object{Size: 6, Contents: body}})

	err := store.Download(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil), "report.txt", "")

	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("Download = %v, want io.ErrUnexpectedEOF", err)
	}
}

// TestDownloadSeekableReadAndCloseFailures preserves both failure identities
// instead of replacing the interrupted transfer with its cleanup error.
func TestDownloadSeekableReadAndCloseFailures(t *testing.T) {
	t.Parallel()

	readFailure := errors.New("object read failed")
	closeFailure := errors.New("object close failed")
	body := brokenSeekableBody{Reader: strings.NewReader("report"), err: readFailure, closeErr: closeFailure}
	store := storage.New(&driver{object: storage.Object{Size: 6, Contents: body}})

	err := store.Download(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil), "report.txt", "")

	if !errors.Is(err, readFailure) || !errors.Is(err, closeFailure) {
		t.Fatalf("Download = %v, want read and close failures", err)
	}
}

// TestDownloadRangeReadFailure retains the native partial-content response
// while returning a source failure that occurs during the requested range.
func TestDownloadRangeReadFailure(t *testing.T) {
	t.Parallel()

	want := errors.New("range source failed")
	body := brokenSeekableBody{Reader: strings.NewReader("0123456789"), err: want}
	store := storage.New(&driver{object: storage.Object{Size: 10, Contents: body}})
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.Header.Set("Range", "bytes=2-4")

	recorder := httptest.NewRecorder()

	err := store.Download(recorder, request, "digits.txt", "")

	if !errors.Is(err, want) {
		t.Fatalf("Download = %v, want range read failure", err)
	}

	if recorder.Code != http.StatusPartialContent || recorder.Header().Get("Content-Range") != "bytes 2-4/10" {
		t.Fatalf("range response = %d %v", recorder.Code, recorder.Header())
	}
}

// TestDownloadMultipleRanges preserves ServeContent's native multipart body,
// including independent range positions and exact bytes in each part.
func TestDownloadMultipleRanges(t *testing.T) {
	t.Parallel()

	store := seekableStore(t, "digits.txt", "0123456789")
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.Header.Set("Range", "bytes=1-2,7-8")

	recorder := httptest.NewRecorder()

	if err := store.Download(recorder, request, "digits.txt", ""); err != nil {
		t.Fatal(err)
	}

	mediaType, parameters, err := mime.ParseMediaType(recorder.Header().Get("Content-Type"))

	if err != nil || mediaType != "multipart/byteranges" {
		t.Fatalf("multipart type = %q, %v", mediaType, err)
	}

	reader := multipart.NewReader(recorder.Body, parameters["boundary"])

	for _, want := range []string{"12", "78"} {
		part, err := reader.NextPart()

		if err != nil {
			t.Fatal(err)
		}

		contents, err := io.ReadAll(part)

		if err != nil || string(contents) != want {
			t.Fatalf("range body = %q, %v; want %q", contents, err, want)
		}

		if err := part.Close(); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := reader.NextPart(); !errors.Is(err, io.EOF) {
		t.Fatalf("multipart end = %v, want EOF", err)
	}
}

// TestDownloadHTTPKeepsCommittedResponse ensures returning a cleanup failure
// through the public router adapter never appends a replacement problem body.
func TestDownloadHTTPKeepsCommittedResponse(t *testing.T) {
	t.Parallel()

	body := &closeFailureBody{Reader: strings.NewReader("report"), err: errors.New("close failed")}
	store := storage.New(&driver{object: storage.Object{Size: 6, Contents: body}})
	handler := router.Handler(func(writer http.ResponseWriter, request *http.Request) error {
		return store.Download(writer, request, "report.txt", "")
	})

	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/", nil))

	if recorder.Code != http.StatusOK || recorder.Body.String() != "report" {
		t.Fatalf("response = %d %q, want unchanged download", recorder.Code, recorder.Body.String())
	}

	if !body.closed || recorder.Header().Get("Content-Type") != "text/plain; charset=utf-8" {
		t.Fatalf("cleanup = %v, headers = %v", body.closed, recorder.Header())
	}
}

// shortResponse exposes a writer that accepts fewer bytes without supplying an
// error, allowing Download to report the standard short-write identity.
type shortResponse struct {
	// ResponseRecorder records the accepted prefix and native HTTP headers.
	*httptest.ResponseRecorder
}

// Write accepts only half the requested bytes. This intentionally broken writer
// result exercises the io.Writer short-write rule at the public HTTP boundary.
func (response shortResponse) Write(contents []byte) (int, error) {
	return response.ResponseRecorder.Write(contents[:len(contents)/2])
}

// TestDownloadSeekableShortWrite makes a short client write observable even
// though native ServeContent does not return its copying error.
func TestDownloadSeekableShortWrite(t *testing.T) {
	t.Parallel()

	store := seekableStore(t, "report.txt", "report")
	response := shortResponse{ResponseRecorder: httptest.NewRecorder()}

	err := store.Download(response, httptest.NewRequest(http.MethodGet, "/", nil), "report.txt", "")

	if !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("Download = %v, want io.ErrShortWrite", err)
	}
}

// finalEOFBody returns EOF with its final bytes, a valid io.Reader result that
// must not be mistaken for truncation when all requested bytes were delivered.
type finalEOFBody struct {
	// Reader supplies normal size and range positioning for the response.
	*strings.Reader
}

// Read preserves the final data while returning EOF on the same call.
func (body finalEOFBody) Read(destination []byte) (int, error) {
	count, err := body.Reader.Read(destination)

	if body.Len() == 0 {
		return count, io.EOF
	}

	return count, err
}

// Close has no external resource to release; the reader remains GC-owned.
func (finalEOFBody) Close() error {
	return nil
}

// TestDownloadSeekableFinalEOFSucceeds distinguishes a completed transfer from
// premature EOF without relying on a private copying implementation.
func TestDownloadSeekableFinalEOFSucceeds(t *testing.T) {
	t.Parallel()

	body := finalEOFBody{Reader: strings.NewReader("report")}
	store := storage.New(&driver{object: storage.Object{Size: 6, Contents: body}})
	response := httptest.NewRecorder()

	if err := store.Download(response, httptest.NewRequest(http.MethodGet, "/", nil), "report.txt", ""); err != nil {
		t.Fatal(err)
	}

	if response.Body.String() != "report" {
		t.Fatalf("body = %q, want report", response.Body.String())
	}
}

// seekFailureBody keeps normal reads and owned cleanup while failing native
// size/range positioning. It exercises ServeContent's committed error response.
type seekFailureBody struct {
	// ReadCloser supplies the object bytes and records cleanup independently.
	io.ReadCloser

	// err is the native seek failure.
	err error
}

// Seek returns the same positioning failure without moving the source.
func (body seekFailureBody) Seek(int64, int) (int64, error) {
	return 0, body.err
}

// TestDownloadSeekFailurePreservesNativeResponse retains ServeContent's HTTP
// error status, returns its source cause, and still closes the opened body.
func TestDownloadSeekFailurePreservesNativeResponse(t *testing.T) {
	t.Parallel()

	want := errors.New("size probe failed")
	contents := &closeFailureBody{Reader: strings.NewReader("report")}
	body := seekFailureBody{ReadCloser: contents, err: want}
	store := storage.New(&driver{object: storage.Object{Size: 6, Contents: body}})
	response := httptest.NewRecorder()

	err := store.Download(response, httptest.NewRequest(http.MethodGet, "/", nil), "report.txt", "")

	if !errors.Is(err, want) || !contents.closed {
		t.Fatalf("Download = %v, closed = %v; want seek failure and cleanup", err, contents.closed)
	}

	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want native 500", response.Code)
	}
}

// TestDownloadMultipleRangesReadFailure reports an error from the native
// multipart producer rather than losing it at ServeContent's void boundary.
func TestDownloadMultipleRangesReadFailure(t *testing.T) {
	t.Parallel()

	want := errors.New("multipart range source failed")
	body := brokenSeekableBody{Reader: strings.NewReader("0123456789"), err: want}
	store := storage.New(&driver{object: storage.Object{Size: 10, Contents: body}})
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.Header.Set("Range", "bytes=1-2,7-8")

	response := httptest.NewRecorder()

	err := store.Download(response, request, "digits.txt", "")

	if !errors.Is(err, want) {
		t.Fatalf("Download = %v, want multipart read failure", err)
	}

	if response.Code != http.StatusPartialContent {
		t.Fatalf("status = %d, want native 206", response.Code)
	}
}

// ExampleStorage_Download exercises the declaration's HTTP handler pattern
// without a server, network request, or wall-clock wait.
func ExampleStorage_Download() {
	ctx := context.Background()
	files := storage.New(memorystorage.New())

	if _, err := files.PutBytes(ctx, "report.txt", []byte("report")); err != nil {
		fmt.Println(err)

		return
	}

	handler := func(writer http.ResponseWriter, request *http.Request) error {
		return files.Download(writer, request, "report.txt", "annual.txt")
	}

	request := httptest.NewRequest(http.MethodGet, "/report", nil)
	request.Header.Set("Range", "bytes=2-3")

	response := httptest.NewRecorder()

	if err := handler(response, request); err != nil {
		fmt.Println(err)

		return
	}

	fmt.Println(response.Code, response.Body.String())
	// Output: 206 po
}
