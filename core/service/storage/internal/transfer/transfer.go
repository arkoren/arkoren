package transfer

import (
	"bytes"
	"cmp"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Presentation describes the HTTP filename and requested display policy.
// Download supplies attachment with an optional name; Inline supplies inline
// and the object basename. Unsafe inline types are downgraded before writing.
type Presentation struct {
	// Filename overrides the key basename when nonempty. It affects the HTTP
	// name and MIME inference only, never the stored object's key or contents.
	Filename string

	// Disposition requests attachment or inline rendering. Unsafe inline MIME
	// types are downgraded to attachment before any response is committed.
	Disposition string
}

// Info contains only metadata needed to stream a borrowed body. Key is the
// driver's returned identity, which remains authoritative in write diagnostics.
type Info struct {
	// Key identifies the opened object in transfer error messages.
	Key string

	// Size supplies Content-Length for a sequential body.
	Size int64

	// ModifiedAt supplies conditional and last-modified handling.
	ModifiedAt time.Time
}

// Serve prepares the borrowed contents before committing response headers,
// applies the requested security policy, and retains native ServeContent error
// responses. It returns source/seek/write errors but never closes contents or
// replaces a response body after commitment.
func Serve(
	w http.ResponseWriter,
	r *http.Request,
	key string,
	contents io.Reader,
	info Info,
	presentation Presentation,
) error {
	filename := cmp.Or(presentation.Filename, path.Base(key))

	body, contentType, err := objectBody(contents, filename)

	if err != nil {
		return fmt.Errorf("prepare %q: %w", key, err)
	}

	header := w.Header()
	header.Set("Content-Type", contentType)
	header.Set("X-Content-Type-Options", "nosniff")

	if presentation.Disposition == "inline" {
		header.Set("Content-Security-Policy", "sandbox")

		if !inlineSafe(contentType) {
			presentation.Disposition = "attachment"
		}
	}

	header.Set("Content-Disposition", contentDisposition(presentation.Disposition, filename))

	return writeObject(w, r, body, info)
}

// objectBody resolves the response MIME type without losing sniffed bytes or
// seekability. It borrows contents; the caller owns Close on every path.
// A known extension avoids sniffing. Unknown extensions rewind seekable bodies
// and prepend the sniffed bytes to non-seekable streams.
func objectBody(contents io.Reader, filename string) (io.Reader, string, error) {
	contentType := mime.TypeByExtension(path.Ext(filename))

	if contentType != "" {
		return contents, contentType, nil
	}

	head, err := ReadHead(contents)

	if err != nil {
		return nil, "", err
	}

	contentType = http.DetectContentType(head)

	if seeker, ok := contents.(io.Seeker); ok {
		_, err := seeker.Seek(0, io.SeekStart)

		return contents, contentType, err
	}

	return io.MultiReader(bytes.NewReader(head), contents), contentType, nil
}

// writeObject commits the prepared object through native ServeContent when
// seekable, preserving its range and conditional behavior. Sequential streams
// retain their known length and HEAD support. It does not close the body.
func writeObject(w http.ResponseWriter, r *http.Request, body io.Reader, info Info) error {
	if seeker, ok := body.(io.ReadSeeker); ok {
		transfer := &contentTransfer{ResponseWriter: w, contents: seeker}
		http.ServeContent(transfer, r, "", info.ModifiedAt, transfer)

		return transfer.Err()
	}

	header := w.Header()
	header.Set("Content-Length", strconv.FormatInt(info.Size, 10))

	if !info.ModifiedAt.IsZero() {
		header.Set("Last-Modified", info.ModifiedAt.UTC().Format(http.TimeFormat))
	}

	w.WriteHeader(http.StatusOK)

	if r.Method == http.MethodHead {
		return nil
	}

	if _, err := io.Copy(w, body); err != nil {
		return fmt.Errorf("write %q: %w", info.Key, err)
	}

	return nil
}

// contentTransfer bridges ServeContent's void API to the helper's error result
// without reimplementing range, conditional, or HEAD processing. The embedded
// writer retains header/status handling; the parent retains body ownership.
type contentTransfer struct {
	// ResponseWriter remains owned by the HTTP caller. Headers and status pass
	// through directly; Write observes failures without buffering the response.
	http.ResponseWriter

	// contents supplies seekable object bytes and size probes. It is borrowed
	// from the opened object; the parent alone remains responsible for Close.
	contents io.ReadSeeker

	// mutex guards failures shared by ServeContent's multipart-range producer
	// and the response-copying goroutine.
	mutex sync.Mutex

	// err retains source, seek, and transport failures. Ordinary read EOF is
	// excluded unless it reveals a truncated response or range.
	err error
}

// Read preserves the source result while retaining failures that ServeContent
// would otherwise discard. Its copy limits each read to the remaining response
// or range bytes, so EOF before filling that request means truncation. EOF with
// the full requested bytes is successful. The helper handles sniffing beforehand.
func (transfer *contentTransfer) Read(destination []byte) (int, error) {
	count, err := transfer.contents.Read(destination)

	if errors.Is(err, io.EOF) {
		if count < len(destination) {
			transfer.record(io.ErrUnexpectedEOF)
		}

		return count, err
	}

	transfer.record(err)

	return count, err
}

// Seek preserves native positioning and records size/range probe failures.
// It neither owns nor closes the source connection or object body.
func (transfer *contentTransfer) Seek(offset int64, whence int) (int64, error) {
	position, err := transfer.contents.Seek(offset, whence)
	transfer.record(err)

	return position, err
}

// Write sends bytes directly to the original response and retains transport
// failures. A short write without an error is reported as io.ErrShortWrite.
func (transfer *contentTransfer) Write(contents []byte) (int, error) {
	count, err := transfer.ResponseWriter.Write(contents)

	if err == nil && count != len(contents) {
		err = io.ErrShortWrite
	}

	transfer.record(err)

	return count, err
}

// record retains a failure from either side of a multipart transfer. Nil
// outcomes do nothing; synchronization permits ServeContent's native producer.
func (transfer *contentTransfer) record(err error) {
	if err == nil {
		return
	}

	transfer.mutex.Lock()

	transfer.err = errors.Join(transfer.err, err)
	transfer.mutex.Unlock()
}

// Err returns failures observed before ServeContent returned. The mutex also
// protects reads when a native multipart producer is finishing after a failed
// client write; this wrapper starts no producer and never closes the object.
func (transfer *contentTransfer) Err() error {
	transfer.mutex.Lock()
	defer transfer.mutex.Unlock()

	return transfer.err
}

// inlineSafe reports whether contentType may be displayed inline from the
// application's origin. It is an allow-list: raster images, PDF, audio,
// video and plain text. Everything else (HTML, SVG, XML, JavaScript, CSS,
// WebAssembly, JSON and unknown types) is not safe, because a browser can
// execute it or load it as a script or stylesheet from the same origin,
// which bypasses a 'self' Content-Security-Policy. SVG is never allowed:
// it is an XML document that can carry script.
func inlineSafe(contentType string) bool {
	mediaType, _, _ := strings.Cut(contentType, ";")
	mediaType = strings.ToLower(strings.TrimSpace(mediaType))

	switch mediaType {
	case "image/png", "image/jpeg", "image/gif", "image/webp", "image/avif", "image/bmp",
		"image/x-icon", "image/vnd.microsoft.icon", "application/pdf", "text/plain":
		return true
	}

	return strings.HasPrefix(mediaType, "audio/") || strings.HasPrefix(mediaType, "video/")
}

// contentDisposition builds a Content-Disposition value (RFC 6266) for
// filename. The filename parameter is an ASCII fallback for old clients,
// with every other character replaced by '_'; when the name differs, the
// UTF-8 original follows as filename* (RFC 5987), which clients prefer.
func contentDisposition(disposition, filename string) string {
	fallback := strings.Map(func(char rune) rune {
		if char < ' ' || char > '~' {
			return '_'
		}

		return char
	}, filename)

	value := mime.FormatMediaType(disposition, map[string]string{"filename": fallback})

	if value == "" {
		value = disposition
	}

	if fallback == filename {
		return value
	}

	return value + "; filename*=UTF-8''" + percentEncode(filename)
}

// percentEncode escapes every byte of s outside the RFC 5987 attr-char set.
func percentEncode(s string) string {
	// attrChars is RFC 5987's non-alphanumeric safe set. Other bytes must be
	// escaped so filename* cannot inject parameters or response headers.
	const attrChars = "!#$&+-.^_`|~"

	var builder strings.Builder

	for index := range len(s) {
		char := s[index]

		switch {
		case char >= 'a' && char <= 'z', char >= 'A' && char <= 'Z', char >= '0' && char <= '9',
			strings.IndexByte(attrChars, char) >= 0:
			builder.WriteByte(char)
		default:
			fmt.Fprintf(&builder, "%%%02X", char)
		}
	}

	return builder.String()
}
