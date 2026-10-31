package request_test

import (
	"encoding/json/jsontext"
	"errors"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"arkoren.dev/core/http/internal/bodysize"
	"arkoren.dev/core/http/request"
	"arkoren.dev/core/http/router"
	"arkoren.dev/foundation/problem"
)

// TestBytesNilBodyReturnsEmpty checks the no-body convention with Unlimited.
func TestBytesNilBodyReturnsEmpty(t *testing.T) {
	t.Parallel()

	r, err := http.NewRequest(http.MethodPost, "http://example.test/", nil)

	if err != nil {
		t.Fatal(err)
	}

	value, err := request.Bytes(r, request.Unlimited())

	if err != nil || len(value) != 0 {
		t.Fatalf("body = %q, error = %v, want empty input", value, err)
	}
}

// TestStrictJSONReturnsInvalidJSON verifies unknown member rejection.
func TestStrictJSONReturnsInvalidJSON(t *testing.T) {
	t.Parallel()

	_, err := request.JSON[struct{}](httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"unknown":true}`)), request.Strict())
	details := requireBodyProblemDetails(t, err)

	assertInvalidBodyProblem(t, details, "Invalid JSON request body", "The request body must contain valid JSON.")
	requireBodyErrorCause(t, details.Unwrap())
}

// TestLimitedXMLReturnsInvalidXML verifies malformed XML rejection.
func TestLimitedXMLReturnsInvalidXML(t *testing.T) {
	t.Parallel()

	_, err := request.XML[struct{}](httptest.NewRequest(http.MethodPost, "/", strings.NewReader("<broken>")), request.MaxSize(-1))
	details := requireBodyProblemDetails(t, err)

	assertInvalidBodyProblem(t, details, "Invalid XML request body", "The request body must contain valid XML.")
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

// assertInvalidBodyProblem verifies the body validation problem metadata.
func assertInvalidBodyProblem(t *testing.T, details problem.Details, title, detail string) {
	t.Helper()

	if details.Status() != http.StatusBadRequest {
		t.Fatalf("got %#v", details)
	}

	if details.Title() != title {
		t.Fatalf("got %#v", details)
	}

	if details.Detail() != detail {
		t.Fatalf("got %#v", details)
	}
}

// requireBodyErrorCause fails the test if the validation error lost its cause.
func requireBodyErrorCause(t *testing.T, err error) {
	t.Helper()

	if err == nil {
		t.Fatal("expected decode error to be retained")
	}
}

// TestLimitedJSONPreservesReadError verifies read-error preservation.
func TestLimitedJSONPreservesReadError(t *testing.T) {
	t.Parallel()

	expected := errors.New("read failed")
	req := httptest.NewRequest(http.MethodPost, "/", errReader{
		err: expected,
	})

	_, err := request.JSON[struct{}](req, request.MaxSize(-1))

	if !errors.Is(err, expected) {
		t.Fatalf("expected read error, got %v", err)
	}

	if details, ok := errors.AsType[problem.Details](err); ok {
		t.Fatalf("unexpected problem details: %#v", details)
	}
}

// errReader injects a stable read failure into request bodies so decoder tests
// can verify preservation of the upstream error.
type errReader struct {
	// err is returned on every read to exercise upstream body-read failures.
	err error
}

// Read returns the configured error.
func (errReader errReader) Read([]byte) (int, error) {
	return 0, errReader.err
}

var _ io.Reader = errReader{
	// Only interface conformance is asserted.
}

// TestJSONMapsUpstreamMaxBytesReaderToTooLarge checks that an upstream
// MaxBytesReader failure renders as a 413 problem rather than malformed JSON.
func TestJSONMapsUpstreamMaxBytesReaderToTooLarge(t *testing.T) {
	t.Parallel()

	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"name":"too long"}`))
	r.Body = http.MaxBytesReader(nil, r.Body, 4)

	_, err := request.JSON[map[string]string](r)

	if details, ok := errors.AsType[problem.Details](err); !ok || details.Status() != http.StatusRequestEntityTooLarge {
		t.Fatalf("error = %v, want 413 problem", err)
	}
}

// TestLimitedJSONRejectsOversizedBody checks that a per-call JSON size limit
// reports an oversized-body problem when the payload exceeds it.
func TestLimitedJSONRejectsOversizedBody(t *testing.T) {
	t.Parallel()

	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"name":"too long"}`))

	_, err := request.JSON[map[string]string](r, request.MaxSize(4))

	if details, ok := errors.AsType[problem.Details](err); !ok || details.Status() != http.StatusRequestEntityTooLarge {
		t.Fatalf("error = %v, want 413 problem", err)
	}
}

// TestLimitedBytesMaxInt64 checks that the largest supported body limit does
// not overflow the read limit for an ordinary small body.
func TestLimitedBytesMaxInt64(t *testing.T) {
	t.Parallel()

	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader("body"))

	data, err := request.Bytes(r, request.MaxSize(math.MaxInt64))

	if err != nil || string(data) != "body" {
		t.Fatalf("data = %q, err = %v", data, err)
	}
}

// checkStrictUnsupported fails the test unless call rejects a Strict read with
// request.ErrStrictUnsupported while leaving the body unread.
func checkStrictUnsupported(t *testing.T, call func(*http.Request) error) {
	t.Helper()

	body := strings.NewReader("<a/>")
	r := httptest.NewRequest(http.MethodPost, "/", body)

	if err := call(r); !errors.Is(err, request.ErrStrictUnsupported) {
		t.Fatalf("error = %v, want %v", err, request.ErrStrictUnsupported)
	}

	if body.Len() != 4 {
		t.Fatalf("body was read: %d bytes left, want 4", body.Len())
	}
}

// TestStrictXMLFailsBeforeReading verifies that Strict is rejected for XML
// without consuming the body.
func TestStrictXMLFailsBeforeReading(t *testing.T) {
	t.Parallel()

	checkStrictUnsupported(t, func(r *http.Request) error {
		_, err := request.XML[struct{}](r, request.Strict())

		return err
	})
}

// TestStrictBytesFailsBeforeReading verifies that Strict is rejected for Bytes
// without consuming the body.
func TestStrictBytesFailsBeforeReading(t *testing.T) {
	t.Parallel()

	checkStrictUnsupported(t, func(r *http.Request) error {
		_, err := request.Bytes(r, request.Strict())

		return err
	})
}

// TestStrictStringFailsBeforeReading verifies that Strict is rejected for
// String without consuming the body.
func TestStrictStringFailsBeforeReading(t *testing.T) {
	t.Parallel()

	checkStrictUnsupported(t, func(r *http.Request) error {
		_, err := request.String(r, request.Strict())

		return err
	})
}

// requireTooLarge fails the test unless err is the 413 body problem.
func requireTooLarge(t *testing.T, err error) {
	t.Helper()

	if !errors.Is(err, request.ErrBodyTooLarge) {
		t.Fatalf("error = %v, want ErrBodyTooLarge", err)
	}
}

// TestBytesDefaultLimitWithoutMiddleware verifies a body over
// DefaultMaxBodySize is rejected when no option or context limit is set.
func TestBytesDefaultLimitWithoutMiddleware(t *testing.T) {
	t.Parallel()

	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(strings.Repeat("a", int(request.DefaultMaxBodySize)+1)))

	_, err := request.Bytes(r)

	requireTooLarge(t, err)
}

// TestBytesDefaultLimitAllowsExactSize verifies a body of exactly
// DefaultMaxBodySize is read.
func TestBytesDefaultLimitAllowsExactSize(t *testing.T) {
	t.Parallel()

	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(strings.Repeat("a", int(request.DefaultMaxBodySize))))

	data, err := request.Bytes(r)

	if err != nil || int64(len(data)) != request.DefaultMaxBodySize {
		t.Fatalf("len = %d, err = %v", len(data), err)
	}
}

// TestContextLimitApplies verifies the limit stored in the request context.
func TestContextLimitApplies(t *testing.T) {
	t.Parallel()

	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader("12345"))
	r = r.WithContext(bodysize.NewContext(r.Context(), 4))

	if limit, ok := request.MaxBodySizeFromContext(r.Context()); !ok || limit != 4 {
		t.Fatalf("MaxBodySize = %d, %v", limit, ok)
	}

	_, err := request.Bytes(r)

	requireTooLarge(t, err)
}

// TestMaxBodySizeFromContextAbsent verifies a context without a limit reports none.
func TestMaxBodySizeFromContextAbsent(t *testing.T) {
	t.Parallel()

	if _, ok := request.MaxBodySizeFromContext(httptest.NewRequest(http.MethodGet, "/", nil).Context()); ok {
		t.Fatal("MaxBodySize ok = true, want false")
	}
}

// TestExplicitMaxSizeOverridesContext verifies MaxSize wins over the context
// limit in both directions.
func TestExplicitMaxSizeOverridesContext(t *testing.T) {
	t.Parallel()

	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader("12345"))
	r = r.WithContext(bodysize.NewContext(r.Context(), 4))

	data, err := request.Bytes(r, request.MaxSize(10))

	if err != nil || string(data) != "12345" {
		t.Fatalf("data = %q, err = %v", data, err)
	}

	r = httptest.NewRequest(http.MethodPost, "/", strings.NewReader("12345"))
	r = r.WithContext(bodysize.NewContext(r.Context(), 100))

	_, err = request.Bytes(r, request.MaxSize(4))

	requireTooLarge(t, err)
}

// TestUnlimitedIgnoresDefaultAndContext verifies Unlimited opts out of both
// the default and the context limit.
func TestUnlimitedIgnoresDefaultAndContext(t *testing.T) {
	t.Parallel()

	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(strings.Repeat("a", int(request.DefaultMaxBodySize)+1)))

	data, err := request.Bytes(r, request.Unlimited())

	if err != nil || int64(len(data)) != request.DefaultMaxBodySize+1 {
		t.Fatalf("len = %d, err = %v", len(data), err)
	}

	r = httptest.NewRequest(http.MethodPost, "/", strings.NewReader("12345"))
	r = r.WithContext(bodysize.NewContext(r.Context(), 4))

	if _, err = request.Bytes(r, request.Unlimited()); err != nil {
		t.Fatal(err)
	}
}

// TestLaterMaxSizeOverridesUnlimited verifies options apply in order.
func TestLaterMaxSizeOverridesUnlimited(t *testing.T) {
	t.Parallel()

	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader("12345"))

	_, err := request.Bytes(r, request.Unlimited(), request.MaxSize(4))

	requireTooLarge(t, err)
}

// TestJSONUsesContextLimit verifies the typed decoders honor the context limit.
func TestJSONUsesContextLimit(t *testing.T) {
	t.Parallel()

	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"name":"too long"}`))
	r = r.WithContext(bodysize.NewContext(r.Context(), 4))

	_, err := request.JSON[map[string]string](r)

	requireTooLarge(t, err)
}

// TestJSONNilBodyReturnsInvalidJSON treats a standard client request without a
// body as empty input when it is passed directly to an HTTP handler or helper.
func TestJSONNilBodyReturnsInvalidJSON(t *testing.T) {
	t.Parallel()

	r, err := http.NewRequest(http.MethodPost, "http://example.test/", nil)

	if err != nil {
		t.Fatal(err)
	}

	_, err = request.JSON[struct{}](r)

	if !errors.Is(err, request.ErrInvalidJSON) {
		t.Fatalf("error = %v, want invalid empty JSON body", err)
	}
}

// unavailableJSON models an application-owned unmarshaler whose dependency
// fails independently of whether the HTTP body is valid JSON.
type unavailableJSON struct{}

// UnmarshalJSON returns an operational failure, not an input constraint.
// The request decoder must preserve it as a server error for the router.
//
// Example:
//
//	var value unavailableJSON
//	if err := value.UnmarshalJSON([]byte(`{}`)); !errors.Is(err, io.ErrClosedPipe) {
//		t.Fatal(err)
//	}
func (*unavailableJSON) UnmarshalJSON([]byte) error {
	return io.ErrClosedPipe
}

// TestJSONUnmarshalerFailureRemainsServerError checks that an application
// decoder failure is not misreported to an HTTP client as malformed input.
func TestJSONUnmarshalerFailureRemainsServerError(t *testing.T) {
	t.Parallel()

	var err error
	handler := router.Handler(func(_ http.ResponseWriter, r *http.Request) error {
		_, err = request.JSON[unavailableJSON](r)

		return err
	})

	writer := httptest.NewRecorder()

	handler.ServeHTTP(writer, httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{}`)))

	if !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("error = %v, want retained operational cause", err)
	}

	if writer.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want server error rather than input feedback", writer.Code)
	}
}

// TestXMLInvalidIntegerReturnsInvalidXML checks that a well-formed document
// with an invalid typed field is a decoding problem, not an operational error.
func TestXMLInvalidIntegerReturnsInvalidXML(t *testing.T) {
	t.Parallel()

	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`<input><count>many</count></input>`))

	_, err := request.XML[struct {
		// Count must be a decimal integer in the submitted XML document.
		Count int `xml:"count"`
	}](r)

	if !errors.Is(err, request.ErrInvalidXML) {
		t.Fatalf("error = %v, want invalid XML field problem", err)
	}
}

// unavailableText models the text-only decoding interface, which native JSON
// accepts only for string values before invoking the application method.
type unavailableText string

// UnmarshalText returns an operational failure to distinguish an invoked
// application decoder from JSON's own rejection of a non-string value.
//
// Example:
//
//	var value unavailableText
//	if err := value.UnmarshalText([]byte("value")); !errors.Is(err, io.ErrClosedPipe) {
//		t.Fatal(err)
//	}
func (*unavailableText) UnmarshalText([]byte) error {
	return io.ErrClosedPipe
}

// TestJSONTextDecoderWrongKindRemainsInvalidJSON ensures that method capability
// does not misclassify a native type rejection before the method was invoked.
func TestJSONTextDecoderWrongKindRemainsInvalidJSON(t *testing.T) {
	t.Parallel()

	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`42`))

	_, err := request.JSON[unavailableText](r)

	if !errors.Is(err, request.ErrInvalidJSON) {
		t.Fatalf("error = %v, want native JSON kind mismatch", err)
	}
}

// TestJSONTextDecoderFailureRemainsOperational distinguishes an invoked text
// method's failure from the native wrong-kind rejection tested separately.
func TestJSONTextDecoderFailureRemainsOperational(t *testing.T) {
	t.Parallel()

	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`"value"`))

	_, err := request.JSON[unavailableText](r)

	if !errors.Is(err, io.ErrClosedPipe) || errors.Is(err, request.ErrInvalidJSON) {
		t.Fatalf("error = %v, want retained text-decoder failure", err)
	}
}

// unavailableJSONFrom exercises the JSON v2 streaming-method protocol rather
// than the older byte-slice unmarshaler protocol.
type unavailableJSONFrom struct{}

// UnmarshalJSONFrom reports an operational failure before consuming the value.
// JSON's wrapper must not make that failure into client validation feedback.
//
// Example:
//
//	var value unavailableJSONFrom
//	err := value.UnmarshalJSONFrom(jsontext.NewDecoder(strings.NewReader(`{}`)))
//	if !errors.Is(err, io.ErrClosedPipe) {
//		t.Fatal(err)
//	}
func (*unavailableJSONFrom) UnmarshalJSONFrom(*jsontext.Decoder) error {
	return io.ErrClosedPipe
}

// TestJSONStreamingDecoderFailureRemainsOperational verifies the JSON v2
// streaming callback receives the same failure-preservation policy.
func TestJSONStreamingDecoderFailureRemainsOperational(t *testing.T) {
	t.Parallel()

	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{}`))

	_, err := request.JSON[unavailableJSONFrom](r)

	if !errors.Is(err, io.ErrClosedPipe) || errors.Is(err, request.ErrInvalidJSON) {
		t.Fatalf("error = %v, want retained streaming-decoder failure", err)
	}
}
