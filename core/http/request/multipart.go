package request

import (
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"slices"

	"arkoren.dev/core/internal/hook"
	"arkoren.dev/foundation/problem"
)

// multipartMemory is how many bytes of a multipart body [Multipart] keeps in
// memory. File parts beyond it are spilled to temporary files on disk, which
// is why a parsed [Form] must be cleaned up with [Form.RemoveAll].
const multipartMemory int64 = 8 << 20 // 8 MiB

// ErrInvalidMultipart is returned by [Multipart] when the body is not valid
// multipart/form-data, such as a missing boundary or malformed part. Native
// multipart.ErrMessageTooLarge limits instead match [ErrBodyTooLarge].
//
// Example:
//
//	failure := request.ErrInvalidMultipart.Derive(func(details *problem.Builder) {
//		details.Detail("The request cannot be completed.")
//	})
//	_ = failure.Status()
var ErrInvalidMultipart = problem.New(func(details *problem.Builder) {
	details.Type("tag:arkoren.dev,2026-09:core:http/request:invalid-multipart")
	details.Title("Invalid multipart request body")
	details.Detail("The request body must contain valid multipart/form-data.")
	details.Status(http.StatusBadRequest)
})

// Form is a parsed multipart/form-data body: text fields plus uploaded
// files. It is a thin wrapper around [multipart.Form]; get one with
// [Multipart]. File headers can refer to memory or temporary files; the caller
// must follow [Multipart]'s cleanup ownership rules before retaining them.
//
// Example:
//
//	form, err := request.Multipart(r, 20<<20)
//	if err != nil {
//		return err
//	}
//	return response.String(w, http.StatusOK, form.Value("title"))
type Form struct {
	// form retains parsed values, file headers, and any temporary files until
	// RemoveAll releases them.
	form *multipart.Form
}

// multipartBody records non-EOF read failures at the transport boundary, before
// the standard multipart parser wraps them with input-parsing context. This
// lets Multipart distinguish operational failures from malformed form data.
type multipartBody struct {
	// ReadCloser retains the bounded original body and its Close behavior.
	//
	// Example:
	//	body := &multipartBody{ReadCloser: r.Body}
	//	_ = body.ReadCloser
	io.ReadCloser

	// err is the most recent non-EOF read failure; nil means none was observed.
	err error
}

// Read forwards the read unchanged and retains a non-EOF failure for error
// classification after parsing. A truncated body remains an input error.
//
// Example:
//
//	_, err := r.Body.Read(buffer)
//	if err != nil && !errors.Is(err, io.EOF) {
//		return err
//	}
func (body *multipartBody) Read(buffer []byte) (int, error) {
	n, err := body.ReadCloser.Read(buffer)

	if err != nil && !errors.Is(err, io.EOF) {
		body.err = err
	}

	return n, err
}

// Multipart parses the request body as multipart/form-data and returns its
// fields and files. Up to 8 MiB of file content is held in memory and larger
// files are spilled to temporary files on disk. The body is capped at maxSize
// bytes by replacing r.Body with an [http.MaxBytesReader]. A negative maxSize
// uses the limit in the request context (the bodylimit middleware), else
// [DefaultMaxBodySize].
//
// The body is parsed once. Later calls on the same request return the cached
// result and ignore their maxSize, so the first call's limit wins. After a
// call that returned an error the body is consumed, so do not call again.
//
// Input errors are [problem.Details] the router renders directly, with the
// cause attached. Body transport and temporary-file I/O failures are returned
// without converting them into client validation feedback:
//   - [ErrBodyTooLarge] (413) when the body exceeds maxSize or a standard-library
//     multipart.ErrMessageTooLarge limit is reached.
//   - [ErrUnsupportedMediaType] (415) when the request is not
//     multipart/form-data.
//   - [ErrInvalidMultipart] (400) for malformed multipart input.
//
// Temporary files: when the request was served through a router.Handler,
// Multipart registers an AfterResponse hook that calls [Form.RemoveAll], once
// per request. Without hooks, such as in a plain http.Handler, the caller must
// defer [Form.RemoveAll]. A form parsed elsewhere keeps its existing cleanup
// owner; reusing r.MultipartForm does not transfer that ownership.
//
// Example:
//
//	form, err := request.Multipart(r, 20<<20)
//	if err != nil {
//		return err
//	}
//
//	title := form.Value("title")
//	avatar := form.File("avatar") // nil when not uploaded
func Multipart(r *http.Request, maxSize int64) (*Form, error) {
	return parseMultipart(r, maxSize, multipartMemory)
}

// parseMultipart implements [Multipart] with an explicit in-memory
// threshold so tests can force files onto disk with small bodies.
// If query parsing fails after the body was parsed, it removes the body's
// temporary files before returning a nil form. Cleanup failures are joined
// with the parsing cause so callers can inspect both through the returned error.
func parseMultipart(r *http.Request, maxSize, memory int64) (*Form, error) {
	if r.MultipartForm != nil {
		return &Form{
			form: r.MultipartForm,
		}, nil
	}

	var config bodyConfig

	if maxSize >= 0 {
		config = newBodyConfig([]BodyOption{MaxSize(maxSize)})
	}

	body := &multipartBody{ReadCloser: config.body(r)}
	r.Body = body

	if err := r.ParseMultipartForm(memory); err != nil {
		if r.MultipartForm != nil {
			// Query parsing can fail after the body successfully spills to disk.
			// Retain cleanup failures alongside the original parsing cause.
			err = errors.Join(err, r.MultipartForm.RemoveAll())
		}

		return nil, multipartError(err, body.err)
	}

	form := &Form{
		form: r.MultipartForm,
	}

	if registry, ok := hook.FromContext(r.Context()); ok {
		// Nothing can handle a cleanup failure after the response is sent.
		registry.AfterResponse(func(error) {
			_ = form.RemoveAll()
		})
	}

	return form, nil
}

// multipartError preserves transport and filesystem failures while mapping
// malformed input and known limits to their existing stable HTTP problems.
// readErr comes from the body boundary; io.ErrUnexpectedEOF means truncation,
// not infrastructure failure. Existing HTTP problems keep their identity;
// otherwise a native body-limit failure takes precedence.
func multipartError(err, readErr error) error {
	if _, ok := errors.AsType[problem.Details](err); ok {
		return err
	}

	failure := ErrInvalidMultipart

	switch {
	case overLimit(err) || errors.Is(err, multipart.ErrMessageTooLarge):
		failure = ErrBodyTooLarge
	case errors.Is(err, http.ErrNotMultipart):
		failure = ErrUnsupportedMediaType
	case readErr != nil && !errors.Is(readErr, io.ErrUnexpectedEOF):
		return err
	case errors.As(err, new(*os.PathError)):
		return err
	}

	return failure.Derive(func(details *problem.Builder) {
		details.Error(err)
	})
}

// Value returns the first text field called name, or "" when it is absent.
// Later duplicate values do not replace the first; use [Form.Values] to keep
// every submitted value.
//
// Example:
//
//	form, err := request.Multipart(r, 20<<20)
//	if err != nil {
//		return err
//	}
//	return response.String(w, http.StatusOK, form.Value("title"))
func (form *Form) Value(name string) string {
	values := form.form.Value[name]

	if len(values) == 0 {
		return ""
	}

	return values[0]
}

// Values returns every text field called name in the order sent. The result
// is a copy; it is nil when the field is absent.
// Modifying the result does not change the parsed form's values.
//
// Example:
//
//	form, err := request.Multipart(r, 20<<20)
//	if err != nil {
//		return err
//	}
//	return response.JSON(w, http.StatusOK, form.Values("tag"))
func (form *Form) Values(name string) []string {
	return slices.Clone(form.form.Value[name])
}

// File returns the first file uploaded under name, or nil when none was.
// Reading a header cannot fail, so there is no error: absence is the only
// case, and it is not an error because most upload fields are optional. Use
// validate.File with a callback that calls FileBuilder.Required to demand one.
//
// Example:
//
//	form, err := request.Multipart(r, 20<<20)
//	if err != nil {
//		return err
//	}
//	if header := form.File("avatar"); header != nil {
//		_ = header.Size // inspect metadata before opening or storing the upload
//	}
func (form *Form) File(name string) *multipart.FileHeader {
	files := form.form.File[name]

	if len(files) == 0 {
		return nil
	}

	return files[0]
}

// Files returns every file uploaded under name in the order sent. The result
// is a copy of the slice; it is nil when none was uploaded.
// The headers themselves remain shared and keep the lifetime described by
// [Multipart] and [Form.RemoveAll].
//
// Example:
//
//	form, err := request.Multipart(r, 20<<20)
//	if err != nil {
//		return err
//	}
//	for _, header := range form.Files("attachments") {
//		_ = header.Filename
//	}
func (form *Form) Files(name string) []*multipart.FileHeader {
	return slices.Clone(form.form.File[name])
}

// RemoveAll deletes the temporary files holding uploads that exceeded the
// in-memory threshold. It is safe to call more than once. When [Multipart]
// parses the form with hooks installed, it arranges cleanup after the response.
// Headers for successfully removed disk files can no longer be opened;
// in-memory file parts remain readable. A removal failure is returned and can
// leave files behind. Consume and close uploaded files before cleanup.
//
// Example:
//
//	form, err := request.Multipart(r, 20<<20)
//	if err != nil {
//		return err
//	}
//	// Consume uploaded files before explicitly releasing temporary storage.
//	if err := form.RemoveAll(); err != nil {
//		return err
//	}
func (form *Form) RemoveAll() error {
	return form.form.RemoveAll()
}
