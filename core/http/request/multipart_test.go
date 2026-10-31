package request

import (
	"bytes"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"testing"

	"arkoren.dev/core/http/internal/bodysize"
	"arkoren.dev/core/http/router"
	"arkoren.dev/foundation/problem"
)

// TestMultipartPreservesExplicitOperationalProblem checks that malformed-input
// mapping cannot overwrite an existing server problem.
func TestMultipartPreservesExplicitOperationalProblem(t *testing.T) {
	t.Parallel()

	failure := problem.New(func(details *problem.Builder) {
		details.Type("tag:example.test,2026:body-read-failure")
		details.Title("Body read failed")
		details.Status(http.StatusInternalServerError)
		details.Error(io.EOF)
	})

	r := httptest.NewRequest(http.MethodPost, "/", errReader{err: failure})
	r.Header.Set("Content-Type", "multipart/form-data; boundary=upload")

	_, err := Multipart(r, 1024)
	details := requireBodyProblemDetails(t, err)

	if details.Status() != http.StatusInternalServerError || details.Type() != failure.Type() {
		t.Fatalf("problem = %v, want the explicit operational problem", details)
	}
}

// errReader injects a stable read failure at the transport boundary.
type errReader struct {
	// err is returned on every read.
	err error
}

// Read returns the configured error.
func (reader errReader) Read([]byte) (int, error) {
	return 0, reader.err
}

// requireBodyProblemDetails returns problem details or fails the test.
func requireBodyProblemDetails(t *testing.T, err error) problem.Details {
	t.Helper()

	var details problem.Details

	if !errors.As(err, &details) {
		t.Fatalf("expected problem details, got %T", err)
	}

	return details
}

// multipartRequest builds a POST request with the given text fields and one
// file part per entry in files (field name to content).
func multipartRequest(t *testing.T, fields map[string][]string, files map[string]string) *http.Request {
	t.Helper()

	var body bytes.Buffer

	writer := multipart.NewWriter(&body)

	for name, values := range fields {
		for _, value := range values {
			if err := writer.WriteField(name, value); err != nil {
				t.Fatal(err)
			}
		}
	}

	for name, content := range files {
		part, err := writer.CreateFormFile(name, name+".txt")

		if err != nil {
			t.Fatal(err)
		}

		if _, err := part.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}

	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}

	r := httptest.NewRequest(http.MethodPost, "/", &body)
	r.Header.Set("Content-Type", writer.FormDataContentType())

	return r
}

// TestMultipartParsesFieldsAndFiles verifies the happy path and absence.
func TestMultipartParsesFieldsAndFiles(t *testing.T) {
	t.Parallel()

	r := multipartRequest(t,
		map[string][]string{
			"title": {"first", "second"},
		},
		map[string]string{
			"avatar": "image-bytes",
		},
	)

	form, err := Multipart(r, 1<<20)

	if err != nil {
		t.Fatal(err)
	}

	defer form.RemoveAll() //nolint:errcheck // nothing on disk in this test.

	if got := form.Value("title"); got != "first" {
		t.Errorf("Value(title) = %q, want first", got)
	}

	if got := form.Value("missing"); got != "" {
		t.Errorf("Value(missing) = %q, want empty", got)
	}

	if got := form.Values("title"); !slices.Equal(got, []string{"first", "second"}) {
		t.Errorf("Values(title) = %v", got)
	}

	if got := form.Values("missing"); got != nil {
		t.Errorf("Values(missing) = %v, want nil", got)
	}

	header := form.File("avatar")

	if header == nil || header.Filename != "avatar.txt" || header.Size != int64(len("image-bytes")) {
		t.Fatalf("File(avatar) = %+v", header)
	}

	if form.File("missing") != nil {
		t.Error("File(missing) != nil")
	}

	if got := form.Files("avatar"); len(got) != 1 || got[0] != header {
		t.Errorf("Files(avatar) = %v", got)
	}

	if got := form.Files("missing"); got != nil {
		t.Errorf("Files(missing) = %v, want nil", got)
	}

	file, err := header.Open()

	if err != nil {
		t.Fatal(err)
	}

	defer file.Close()

	if content, _ := io.ReadAll(file); string(content) != "image-bytes" {
		t.Errorf("content = %q", content)
	}
}

// TestMultipartIsCached verifies later calls reuse the first parse and its limit.
func TestMultipartIsCached(t *testing.T) {
	t.Parallel()

	r := multipartRequest(t, map[string][]string{
		"a": {"1"},
	}, nil)

	first, err := Multipart(r, 1<<20)

	if err != nil {
		t.Fatal(err)
	}

	second, err := Multipart(r, 1) // a 1 byte limit would fail a fresh parse

	if err != nil {
		t.Fatalf("second call: %v", err)
	}

	if second.Value("a") != "1" || first.Value("a") != "1" {
		t.Error("cached form lost its fields")
	}
}

// TestMultipartPreservesBodyReadFailure exercises an operational transport
// failure through the public parser instead of misreporting malformed form data.
func TestMultipartPreservesBodyReadFailure(t *testing.T) {
	t.Parallel()

	r := httptest.NewRequest(http.MethodPost, "/", errReader{err: io.ErrClosedPipe})
	r.Header.Set("Content-Type", "multipart/form-data; boundary=upload")

	_, err := Multipart(r, 1024)

	if !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("error = %v, want retained transport failure", err)
	}

	if _, ok := errors.AsType[problem.Details](err); ok {
		t.Fatalf("operational failure became client input feedback: %v", err)
	}
}

// TestMultipartPreservesTemporaryFileFailure uses the public upload parser and
// its real disk-spill threshold. A missing temporary directory is operational,
// not evidence that the client sent a malformed multipart body.
func TestMultipartPreservesTemporaryFileFailure(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir()+"/missing")

	r := multipartRequest(t, nil, map[string]string{
		"upload": strings.Repeat("x", 9<<20),
	})

	_, err := Multipart(r, 20<<20)

	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("error = %v, want retained temporary-file failure", err)
	}

	if _, ok := errors.AsType[problem.Details](err); ok {
		t.Fatalf("temporary-file failure became client input feedback: %v", err)
	}
}

// TestMultipartRemoveAllKeepsMemoryFilesReadable proves the public cleanup
// contract applies to disk storage, not to file data retained in memory.
func TestMultipartRemoveAllKeepsMemoryFilesReadable(t *testing.T) {
	t.Parallel()

	r := multipartRequest(t, nil, map[string]string{"upload": "memory"})

	form, err := Multipart(r, 1024)

	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		if err := form.RemoveAll(); err != nil {
			t.Error(err)
		}
	})

	if err := form.RemoveAll(); err != nil {
		t.Fatal(err)
	}

	header := form.File("upload")

	if header == nil {
		t.Fatal("missing uploaded memory file")
	}

	file, err := header.Open()

	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		if err := file.Close(); err != nil {
			t.Error(err)
		}
	})

	data, err := io.ReadAll(file)

	if err != nil || string(data) != "memory" {
		t.Fatalf("data = %q, error = %v, want retained memory file", data, err)
	}
}

// TestMultipartPartLimitRetainsBodyTooLargeIdentity verifies the existing 413
// mapping for a native part-count limit, distinct from malformed framing.
func TestMultipartPartLimitRetainsBodyTooLargeIdentity(t *testing.T) {
	t.Setenv("GODEBUG", os.Getenv("GODEBUG")+",multipartmaxparts=1")

	r := multipartRequest(t, map[string][]string{"first": {"a"}, "second": {"b"}}, nil)

	_, err := Multipart(r, 1<<20)

	if !errors.Is(err, ErrBodyTooLarge) || errors.Is(err, ErrInvalidMultipart) {
		t.Fatalf("error = %v, want 413 for the native multipart limit", err)
	}
}

// TestMultipartSpillsToDiskAndRemoveAll verifies files above the threshold
// are readable, live on disk, and disappear after RemoveAll (twice is safe).
func TestMultipartSpillsToDiskAndRemoveAll(t *testing.T) {
	t.Parallel()

	r := multipartRequest(t, nil, map[string]string{
		"doc": "on-disk content",
	})

	form, err := parseMultipart(r, 1<<20, 0)

	if err != nil {
		t.Fatal(err)
	}

	header := form.File("doc")

	file, err := header.Open()

	if err != nil {
		t.Fatal(err)
	}

	if _, isDisk := file.(interface{ Name() string }); !isDisk {
		t.Errorf("file is %T, want a file backed by a temporary file", file)
	}

	if content, _ := io.ReadAll(file); string(content) != "on-disk content" {
		t.Errorf("content = %q", content)
	}

	file.Close()

	if err := form.RemoveAll(); err != nil {
		t.Fatal(err)
	}

	if err := form.RemoveAll(); err != nil {
		t.Fatalf("second RemoveAll: %v", err)
	}

	if _, err := header.Open(); err == nil {
		t.Error("Open after RemoveAll succeeded, want an error")
	}
}

// TestMultipartQueryErrorRemovesTemporaryFiles verifies a query error cleans
// up a successfully parsed body even though no form is returned to the caller.
func TestMultipartQueryErrorRemovesTemporaryFiles(t *testing.T) {
	temp := t.TempDir()
	t.Setenv("TMPDIR", temp)

	r := multipartRequest(t, nil, map[string]string{
		"doc": "on-disk content",
	})
	r.URL.RawQuery = "x=1;2"

	form, err := parseMultipart(r, -1, 0)

	if r.MultipartForm != nil {
		t.Cleanup(func() {
			if err := r.MultipartForm.RemoveAll(); err != nil {
				t.Error(err)
			}
		})
	}

	if form != nil {
		t.Fatal("form != nil on error")
	}

	if !errors.Is(err, ErrInvalidMultipart) {
		t.Fatalf("err = %v, want ErrInvalidMultipart", err)
	}

	details := requireBodyProblemDetails(t, err)

	if details.Status() != http.StatusBadRequest || details.Type() != ErrInvalidMultipart.Type() {
		t.Fatalf("problem = %#v, want invalid multipart with status 400", details)
	}

	if details.Unwrap() == nil || details.Unwrap().Error() != "invalid semicolon separator in query" {
		t.Fatalf("cause = %v, want the original query error", details.Unwrap())
	}

	if r.MultipartForm == nil || len(r.MultipartForm.File["doc"]) != 1 {
		t.Fatal("multipart body was not successfully parsed")
	}

	entries, err := os.ReadDir(temp)

	if err != nil {
		t.Fatal(err)
	}

	if len(entries) != 0 {
		t.Fatalf("temporary files remain after query error: %v", entries)
	}
}

// TestMultipartQueryErrorRemovesTemporaryFilesThroughRouter verifies cleanup
// survives the router's request copy and preserves the problem seen by hooks.
func TestMultipartQueryErrorRemovesTemporaryFilesThroughRouter(t *testing.T) {
	temp := t.TempDir()
	t.Setenv("TMPDIR", temp)

	r := multipartRequest(t, nil, map[string]string{
		"doc": "on-disk content",
	})
	r.URL.RawQuery = "x=1;2"

	var copied *http.Request
	var returnedForm *Form
	var hookError error
	var hookRan bool

	handler := router.Handler(func(_ http.ResponseWriter, r *http.Request) error {
		copied = r
		Hooks(r).AfterResponse(func(err error) {
			hookRan = true
			hookError = err
		})

		form, err := parseMultipart(r, -1, 0)
		returnedForm = form

		return err
	})

	response := httptest.NewRecorder()

	handler.ServeHTTP(response, r)

	if copied != nil && copied.MultipartForm != nil {
		t.Cleanup(func() {
			if err := copied.MultipartForm.RemoveAll(); err != nil {
				t.Error(err)
			}
		})
	}

	if copied == nil || copied == r {
		t.Fatal("handler did not receive a copied request")
	}

	if r.MultipartForm != nil {
		t.Fatal("original request has a multipart form that net/http could clean up")
	}

	if copied.MultipartForm == nil || len(copied.MultipartForm.File["doc"]) != 1 {
		t.Fatal("multipart body was not successfully parsed on the copied request")
	}

	if returnedForm != nil {
		t.Fatal("form != nil on error")
	}

	if !hookRan || !errors.Is(hookError, ErrInvalidMultipart) {
		t.Fatalf("after-response hook: ran = %v, err = %v", hookRan, hookError)
	}

	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", response.Code)
	}

	if !strings.Contains(response.Body.String(), ErrInvalidMultipart.Type()) {
		t.Fatalf("response lost the invalid multipart type: %s", response.Body.String())
	}

	entries, err := os.ReadDir(temp)

	if err != nil {
		t.Fatal(err)
	}

	if len(entries) != 0 {
		t.Fatalf("temporary files remain after the 400 response: %v", entries)
	}
}

// checkMultipartError fails the test unless request.Multipart rejects r with
// an error matching want that wraps a cause, and returns no form.
func checkMultipartError(t *testing.T, r *http.Request, maxSize int64, want problem.Details) {
	t.Helper()

	form, err := Multipart(r, maxSize)

	if form != nil {
		t.Error("form != nil on error")
	}

	if !errors.Is(err, want) {
		t.Fatalf("err = %v, want %v", err, want.Title())
	}

	var details problem.Details

	if !errors.As(err, &details) || details.Unwrap() == nil {
		t.Errorf("err %v has no wrapped cause", err)
	}
}

// TestMultipartErrorTooLarge verifies the problem an oversized body maps to.
func TestMultipartErrorTooLarge(t *testing.T) {
	t.Parallel()

	r := multipartRequest(t, nil, map[string]string{
		"f": "0123456789",
	})

	checkMultipartError(t, r, 20, ErrBodyTooLarge)
}

// TestMultipartErrorNotMultipart verifies the problem a non-multipart
// Content-Type maps to.
func TestMultipartErrorNotMultipart(t *testing.T) {
	t.Parallel()

	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{}`))
	r.Header.Set("Content-Type", "application/json")

	checkMultipartError(t, r, 1<<20, ErrUnsupportedMediaType)
}

// TestMultipartErrorNoContentType verifies the problem a missing Content-Type
// maps to.
func TestMultipartErrorNoContentType(t *testing.T) {
	t.Parallel()

	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader("x"))

	checkMultipartError(t, r, 1<<20, ErrUnsupportedMediaType)
}

// TestMultipartErrorMalformedBody verifies the problem a malformed body maps
// to.
func TestMultipartErrorMalformedBody(t *testing.T) {
	t.Parallel()

	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader("not multipart at all"))
	r.Header.Set("Content-Type", "multipart/form-data; boundary=xyz")

	checkMultipartError(t, r, 1<<20, ErrInvalidMultipart)
}

// TestMultipartErrorMissingBoundary verifies the problem a missing boundary
// maps to.
func TestMultipartErrorMissingBoundary(t *testing.T) {
	t.Parallel()

	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader("x"))
	r.Header.Set("Content-Type", "multipart/form-data")

	checkMultipartError(t, r, 1<<20, ErrInvalidMultipart)
}

// TestMultipartRegistersCleanupOnce verifies the AfterResponse hook removes
// temporary files and that repeated calls register it only once.
func TestMultipartRegistersCleanupOnce(t *testing.T) {
	t.Parallel()

	var header *multipart.FileHeader

	var openAfterResponse error

	handler := router.Handler(func(writer http.ResponseWriter, r *http.Request) error {
		for range 3 {
			form, err := parseMultipart(r, 1<<20, 0)

			if err != nil {
				return err
			}

			header = form.File("doc")
		}

		var afterResponse int

		Hooks(r).AfterResponse(func(error) {
			afterResponse++

			// Hooks run last-in first-out, so the cleanup registered by
			// Multipart runs after this one: the file is still there.
			if _, err := header.Open(); err != nil {
				openAfterResponse = err
			}
		})

		writer.WriteHeader(http.StatusNoContent)

		return nil
	})

	handler.ServeHTTP(httptest.NewRecorder(), multipartRequest(t, nil, map[string]string{
		"doc": "content",
	}))

	if openAfterResponse != nil {
		t.Fatalf("file removed before later hooks ran: %v", openAfterResponse)
	}

	if _, err := header.Open(); err == nil {
		t.Error("temporary file still exists after the response")
	}
}

// TestMultipartPanicRemovesTemporaryFiles verifies a successfully parsed upload
// remains readable in the handler and is removed when that handler panics.
// The router must run its cleanup hook without replacing the original panic.
func TestMultipartPanicRemovesTemporaryFiles(t *testing.T) {
	t.Parallel()

	var header *multipart.FileHeader

	handler := router.Handler(func(_ http.ResponseWriter, r *http.Request) error {
		form, err := parseMultipart(r, 1<<20, 0)

		if err != nil {
			return err
		}

		t.Cleanup(func() {
			if err := form.RemoveAll(); err != nil {
				t.Error(err)
			}
		})

		header = form.File("doc")

		file, err := header.Open()

		if err != nil {
			t.Fatal(err)
		}

		if err := file.Close(); err != nil {
			t.Fatal(err)
		}

		panic("upload handler failed")
	})

	defer func() {
		if recovered := recover(); recovered != "upload handler failed" {
			t.Fatalf("recovered = %v, want the original handler panic", recovered)
		}

		file, err := header.Open()

		if err == nil {
			if err := file.Close(); err != nil {
				t.Error(err)
			}
		}

		if !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("Open after panic = %v, want a removed temporary file", err)
		}
	}()

	handler.ServeHTTP(httptest.NewRecorder(), multipartRequest(t, nil, map[string]string{
		"doc": "content",
	}))
}

// TestMultipartNegativeUsesContextLimit verifies a negative maxSize falls back
// to the limit in the request context.
func TestMultipartNegativeUsesContextLimit(t *testing.T) {
	t.Parallel()

	r := multipartRequest(t, nil, map[string]string{
		"f": "0123456789",
	})
	r = r.WithContext(bodysize.NewContext(r.Context(), 20))

	checkMultipartError(t, r, -1, ErrBodyTooLarge)
}

// TestMultipartNegativeUsesDefaultLimit verifies a negative maxSize without a
// context limit parses bodies under DefaultMaxBodySize.
func TestMultipartNegativeUsesDefaultLimit(t *testing.T) {
	t.Parallel()

	r := multipartRequest(t, nil, map[string]string{
		"f": "0123456789",
	})

	form, err := Multipart(r, -1)

	if err != nil || form.File("f") == nil {
		t.Fatalf("form = %v, err = %v", form, err)
	}
}
