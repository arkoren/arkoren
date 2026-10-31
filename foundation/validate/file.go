package validate

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
)

// sniffLen is how many leading bytes [http.DetectContentType] considers.
const sniffLen = 512

// FileSchema validates uploaded files as *[multipart.FileHeader]. Create one
// with [File]. A nil header is absent, so only [FileBuilder.Required] makes
// a missing upload an error.
//
// The rules judge what the client sent: the size is the one the multipart
// parser counted, the extension is the client's filename extension, and the
// content type is detected from the file's first 512 bytes with
// [http.DetectContentType], never from the client's Content-Type header.
// Detection is coarse. Office documents (.docx, .xlsx) detect as
// application/zip, so combine [FileBuilder.Types]("application/zip") with
// [FileBuilder.Extensions]("docx"). SVG detects as text/xml or text/plain
// and is deliberately not treated as an image, because it can carry
// scripts. There is no Image shorthand for the same reason.
//
// A FileSchema works standalone and on Struct fields of type
// *[multipart.FileHeader].
//
// Example:
//
//	schema := validate.File(func(upload *validate.FileBuilder) {
//		upload.Max(2<<20).Extensions("png", "jpg")
//	})
//
//	if _, err := schema.JSONSchema(); err != nil {
//		panic(err)
//	}
type FileSchema struct {
	// base retains shared immutable rule state for this concrete schema role.
	base[*multipart.FileHeader]
}

// File publishes a schema for *[multipart.FileHeader]. Callbacks run once in
// order; retained drafts cannot change published rules or allowed-type lists.
//
// Example:
//
//	avatar := validate.File(func(upload *validate.FileBuilder) {
//		upload.Required().Max(2<<20).Extensions("png", "jpg").Types("image/png", "image/jpeg")
//	})
//	if _, err := avatar.JSONSchema(); err != nil {
//		panic(err)
//	}
func File(configure ...FileBuilderFunc) FileSchema {
	draft := &FileBuilder{}

	for _, callback := range configure {
		callback(draft)
	}

	return draft.snapshot()
}

// rtype returns the exact validated Go type. Compilation uses it to reject
// mismatched field registrations without converting the input.
func (schema FileSchema) rtype() reflect.Type {
	return fileType
}

// compile binds this schema's recorded rules and children to an execution
// node. Construction problems are collected in rep before publication.
func (schema FileSchema) compile(e *env, rep *report, where string) *node {
	return schema.build(e, rep, where, leafHooks[*multipart.FileHeader]{
		typ: fileType,
		get: func(v reflect.Value) *multipart.FileHeader {
			header, _ := v.Interface().(*multipart.FileHeader)

			return header
		},
	})
}

// fileType is the reflect type of the value a [FileSchema] validates.
var fileType = reflect.TypeFor[*multipart.FileHeader]()

// Validate checks the input using the same preparation and rules as Parse,
// without returning the prepared value. It returns nil, [Errors], or a custom
// Check error; caller-owned input is not modified.
//
// Example:
//
//	schema := validate.File()
//	if err := schema.Validate(context.Background(), nil); err != nil {
//		panic(err)
//	}
func (schema FileSchema) Validate(ctx context.Context, v *multipart.FileHeader) error {
	_, err := schema.Parse(ctx, v)

	return err
}

// Parse checks the input and returns its normalized, defaulted result. It
// copies storage before applying schema changes and returns the zero value
// with [Errors] or a custom Check error when validation fails.
//
// Example:
//
//	schema := validate.File()
//	value, err := schema.Parse(context.Background(), nil)
//	if err != nil {
//		panic(err)
//	}
//	_ = value
func (schema FileSchema) Parse(ctx context.Context, v *multipart.FileHeader) (*multipart.FileHeader, error) {
	return execute(ctx, compileStandalone[*multipart.FileHeader](schema), v)
}

// Min requires a file of at least n bytes.
//
// Example:
//
//	schema := validate.File(func(upload *validate.FileBuilder) {
//		upload.Min(1)
//	})
//	if _, err := schema.JSONSchema(); err != nil {
//		panic(err)
//	}
func (rules *FileBuilder) Min(n int64) *FileBuilder {
	rules.base = rules.addRule(func(_ context.Context, v *multipart.FileHeader) error {
		if v.Size >= n {
			return nil
		}

		return Fail("file.min", map[string]any{"min": n})
	})

	return rules
}

// Max allows a file of at most n bytes.
//
// Example:
//
//	schema := validate.File(func(upload *validate.FileBuilder) {
//		upload.Max(2 << 20)
//	})
//	if _, err := schema.JSONSchema(); err != nil {
//		panic(err)
//	}
func (rules *FileBuilder) Max(n int64) *FileBuilder {
	rules.base = rules.addRule(func(_ context.Context, v *multipart.FileHeader) error {
		if v.Size <= n {
			return nil
		}

		return Fail("file.max", map[string]any{"max": n})
	})

	return rules
}

// Extensions requires the client filename to end in one of exts. The
// comparison ignores case and a leading dot, so "PNG" and ".png" both match
// "photo.PNG". The extension is only what the client claimed; pair it with
// [FileBuilder.Types] to check the content.
//
// Example:
//
//	schema := validate.File(func(upload *validate.FileBuilder) {
//		upload.Extensions("png", "jpg")
//	})
//	if _, err := schema.JSONSchema(); err != nil {
//		panic(err)
//	}
func (rules *FileBuilder) Extensions(exts ...string) *FileBuilder {
	allowed := make([]string, len(exts))

	for index, ext := range exts {
		allowed[index] = strings.TrimPrefix(strings.ToLower(ext), ".")
	}

	listed := make([]any, len(allowed))

	for index, ext := range allowed {
		listed[index] = ext
	}

	rules.base = rules.addRule(func(_ context.Context, v *multipart.FileHeader) error {
		ext := strings.TrimPrefix(strings.ToLower(filepath.Ext(v.Filename)), ".")

		if slices.Contains(allowed, ext) {
			return nil
		}

		return Fail("file.extensions", map[string]any{"extensions": listed})
	})

	return rules
}

// Types requires the detected content type to be one of types. The type is
// detected from the first 512 bytes of the file with
// [http.DetectContentType], so the file is opened; a failure to open or read
// it is returned to the caller as an error rather than reported as a
// violation. Entries may be exact ("application/pdf") or wildcards
// ("image/*"), and parameters such as "; charset=utf-8" are ignored on both
// sides, so Types("text/plain") accepts "text/plain; charset=utf-8".
//
// Example:
//
//	schema := validate.File(func(upload *validate.FileBuilder) {
//		upload.Types("image/png", "image/jpeg")
//	})
//	if _, err := schema.JSONSchema(); err != nil {
//		panic(err)
//	}
func (rules *FileBuilder) Types(types ...string) *FileBuilder {
	allowed := make([]string, len(types))

	for index, name := range types {
		allowed[index] = baseMediaType(name)
	}

	listed := make([]any, len(allowed))

	for index, name := range allowed {
		listed[index] = name
	}

	rules.base = rules.addRule(func(_ context.Context, v *multipart.FileHeader) error {
		detected, err := detectFileType(v)

		if err != nil {
			return err
		}

		for _, name := range allowed {
			if prefix, wildcard := strings.CutSuffix(name, "/*"); wildcard {
				if strings.HasPrefix(detected, prefix+"/") {
					return nil
				}
			} else if detected == name {
				return nil
			}
		}

		return Fail("file.types", map[string]any{"types": listed})
	})

	return rules
}

// baseMediaType lower-cases a media type and drops its parameters.
func baseMediaType(mediaType string) string {
	base, _, _ := strings.Cut(mediaType, ";")

	return strings.ToLower(strings.TrimSpace(base))
}

// detectFileType opens header and returns its content type, without
// parameters, as detected from the first 512 bytes.
func detectFileType(header *multipart.FileHeader) (string, error) {
	file, err := header.Open()

	if err != nil {
		return "", fmt.Errorf("validate: open %q: %w", header.Filename, err)
	}

	defer file.Close()

	head := make([]byte, sniffLen)

	count, err := io.ReadFull(file, head)

	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		return "", fmt.Errorf("validate: read %q: %w", header.Filename, err)
	}

	return baseMediaType(http.DetectContentType(head[:count])), nil
}
