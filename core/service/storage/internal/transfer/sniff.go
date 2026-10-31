package transfer

import (
	"errors"
	"io"
	"mime"
	"slices"
	"strings"
)

// sniffLength is how many leading bytes [http.DetectContentType] considers.
const sniffLength = 512

// extensionsByType maps the content types [http.DetectContentType] can
// return to the extension used for generated upload keys. [mime.ExtensionsByType]
// reads the host's mime tables, so its answer varies between machines and
// can list several extensions in any order; the common types are pinned here
// and everything else falls back to the first extension in sorted order.
var extensionsByType = map[string]string{
	"image/png":                     ".png",
	"image/jpeg":                    ".jpg",
	"image/gif":                     ".gif",
	"image/webp":                    ".webp",
	"image/bmp":                     ".bmp",
	"image/x-icon":                  ".ico",
	"application/pdf":               ".pdf",
	"application/zip":               ".zip",
	"application/gzip":              ".gz",
	"application/wasm":              ".wasm",
	"application/ogg":               ".ogg",
	"application/postscript":        ".ps",
	"application/x-rar-compressed":  ".rar",
	"application/vnd.ms-fontobject": ".eot",
	"text/plain":                    ".txt",
	"text/html":                     ".html",
	"text/xml":                      ".xml",
	"audio/mpeg":                    ".mp3",
	"audio/wave":                    ".wav",
	"audio/aiff":                    ".aiff",
	"audio/midi":                    ".mid",
	"audio/basic":                   ".au",
	"video/mp4":                     ".mp4",
	"video/webm":                    ".webm",
	"video/avi":                     ".avi",
	"font/woff":                     ".woff",
	"font/woff2":                    ".woff2",
	"font/ttf":                      ".ttf",
	"font/otf":                      ".otf",
	"font/collection":               ".ttc",
}

// ReadHead reads up to the first 512 bytes of reader, the amount
// [http.DetectContentType] uses. A shorter body is not an error.
func ReadHead(reader io.Reader) ([]byte, error) {
	head := make([]byte, sniffLength)

	count, err := io.ReadFull(reader, head)

	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		return nil, err
	}

	return head[:count], nil
}

// Extension returns the extension, with its dot, to store contentType
// under, or "" when the type is unknown or application/octet-stream.
// Parameters such as "; charset=utf-8" are ignored.
func Extension(contentType string) string {
	mediaType, _, _ := strings.Cut(contentType, ";")
	mediaType = strings.ToLower(strings.TrimSpace(mediaType))

	if extension, ok := extensionsByType[mediaType]; ok {
		return extension
	}

	if mediaType == "" || mediaType == "application/octet-stream" {
		return ""
	}

	extensions, err := mime.ExtensionsByType(mediaType)

	if err != nil || len(extensions) == 0 {
		return ""
	}

	return slices.Min(extensions)
}
