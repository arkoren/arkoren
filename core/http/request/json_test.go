package request_test

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"

	"arkoren.dev/core/http/middleware/bodylimit"
	"arkoren.dev/core/http/request"
	"arkoren.dev/core/http/response"
	"arkoren.dev/core/http/router"
	"arkoren.dev/foundation/problem"
	"arkoren.dev/foundation/validate"
)

// ExampleJSON distinguishes absent input from operational decoder failures.
func ExampleJSON() {
	incoming, err := http.NewRequest(http.MethodPost, "http://example.test/", nil)

	if err != nil {
		fmt.Println(err)

		return
	}

	_, err = request.JSON[struct{}](incoming)
	fmt.Println(errors.Is(err, request.ErrInvalidJSON))
	// Output: true
}

// protocolInput supplies native JSON integer, field-name, and timestamp
// representations without application decoding hooks or extra validation rules.
type protocolInput struct {
	// Count exercises the bounds and grammar of a typed JSON integer.
	Count int8 `json:"count"`

	// Name exercises case-sensitive JSON member matching.
	Name string `json:"name"`

	// At uses JSON v2's built-in RFC 3339 timestamp representation.
	At time.Time `json:"at"`
}

// TestJSONRejectsDuplicateNames locks down the JSON v2 default without a
// Strict option; duplicate names remain the stable malformed-input problem.
func TestJSONRejectsDuplicateNames(t *testing.T) {
	t.Parallel()

	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"name":"first","name":"second"}`))

	_, err := request.JSON[protocolInput](r)

	if !errors.Is(err, request.ErrInvalidJSON) {
		t.Fatalf("error = %v, want invalid JSON for duplicate names", err)
	}
}

// TestValidateJSONDuplicateNamesRemainMalformed separates JSON syntax policy
// from the 422 field feedback used for typed values and strict unknown members.
func TestValidateJSONDuplicateNamesRemainMalformed(t *testing.T) {
	t.Parallel()

	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"name":"first","name":"second"}`))

	_, err := request.ValidateJSON(r, validate.Struct[protocolInput]())

	if !errors.Is(err, request.ErrInvalidJSON) || errors.Is(err, request.ErrValidationFailed) {
		t.Fatalf("error = %v, want malformed JSON rather than schema feedback", err)
	}
}

// TestJSONIntegerRejectsExponent preserves the JSON v2 integer grammar instead
// of normalizing an exponent-spelled number into an integer before decoding.
func TestJSONIntegerRejectsExponent(t *testing.T) {
	t.Parallel()

	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"count":1e0}`))

	_, err := request.JSON[protocolInput](r)

	if !errors.Is(err, request.ErrInvalidJSON) {
		t.Fatalf("error = %v, want invalid typed JSON number", err)
	}
}

// TestValidateJSONIntegerOverflowIsInputFeedback preserves the existing typed
// decoding policy while operational callback failures are kept out of it.
func TestValidateJSONIntegerOverflowIsInputFeedback(t *testing.T) {
	t.Parallel()

	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"count":128}`))

	_, err := request.ValidateJSON(r, validate.Struct[protocolInput]())

	if !errors.Is(err, request.ErrValidationFailed) {
		t.Fatalf("error = %v, want typed numeric validation feedback", err)
	}

	errs := requireErrors(t, err)

	requireCodes(t, errs, "/count", "decode")
}

// TestJSONMemberNamesAreCaseSensitive confirms unmatched names are ignored by
// default, not silently matched using the legacy encoding/json name policy.
func TestJSONMemberNamesAreCaseSensitive(t *testing.T) {
	t.Parallel()

	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"Name":"unexpected case"}`))

	value, err := request.JSON[protocolInput](r)

	if err != nil || value.Name != "" {
		t.Fatalf("name = %q, error = %v, want ignored unmatched member", value.Name, err)
	}
}

// TestValidateJSONStrictNameIsUnknown exercises the public JSON v2 unknown-name
// classification without matching the decoder's error-message text.
func TestValidateJSONStrictNameIsUnknown(t *testing.T) {
	t.Parallel()

	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"Name":"unexpected case"}`))

	_, err := request.ValidateJSON(r, validate.Struct[protocolInput](), request.Strict())

	if !errors.Is(err, request.ErrValidationFailed) {
		t.Fatalf("error = %v, want strict unknown-name feedback", err)
	}

	requireCodes(t, requireErrors(t, err), "/Name", "unknown")
}

// TestValidateJSONNativeTimeFormatIsInputFeedback protects native timestamp
// errors from being confused with errors returned by application callbacks.
func TestValidateJSONNativeTimeFormatIsInputFeedback(t *testing.T) {
	t.Parallel()

	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"at":"not a timestamp"}`))

	_, err := request.ValidateJSON(r, validate.Struct[protocolInput]())

	if !errors.Is(err, request.ErrValidationFailed) {
		t.Fatalf("error = %v, want native timestamp input feedback", err)
	}

	requireCodes(t, requireErrors(t, err), "/at", "decode")
}

// TestValidateJSONBinaryTypeReportsString checks the native JSON v2 binary
// representation rather than reporting the underlying Go slice as an array.
func TestValidateJSONBinaryTypeReportsString(t *testing.T) {
	t.Parallel()

	type input struct {
		// Data uses JSON v2's default base64 string representation.
		Data []byte `json:"data"`
	}
	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"data":42}`))

	_, err := request.ValidateJSON(r, validate.Struct[input]())
	errs := requireErrors(t, err)

	requireCodes(t, errs, "/data", "decode")

	if got := errs["/data"][0].Params["default_representation"]; got != "string" {
		t.Fatalf("default representation = %v, want JSON binary string", got)
	}
}

// TestValidateJSONBinaryArrayTypeReportsString preserves the JSON v2 byte-array
// representation alongside byte slices, rather than treating it as a JSON array.
func TestValidateJSONBinaryArrayTypeReportsString(t *testing.T) {
	t.Parallel()

	type input struct {
		// Data uses the native fixed-length binary string representation.
		Data [4]byte `json:"data"`
	}
	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"data":42}`))

	_, err := request.ValidateJSON(r, validate.Struct[input]())
	errs := requireErrors(t, err)

	requireCodes(t, errs, "/data", "decode")

	if got := errs["/data"][0].Params["default_representation"]; got != "string" {
		t.Fatalf("default representation = %v, want native binary string", got)
	}
}

// TestValidateJSONTextTypeReportsString checks a text-only standard type whose
// underlying Go struct is not its JSON representation.
func TestValidateJSONTextTypeReportsString(t *testing.T) {
	t.Parallel()

	type input struct {
		// Address is decoded through the standard text-unmarshal interface.
		Address netip.Addr `json:"address"`
	}
	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"address":42}`))

	_, err := request.ValidateJSON(r, validate.Struct[input]())
	errs := requireErrors(t, err)

	requireCodes(t, errs, "/address", "decode")

	if got := errs["/address"][0].Params["default_representation"]; got != "string" {
		t.Fatalf("default representation = %v, want text-decoded JSON string", got)
	}
}

// TestValidateJSONUintptrTypeReportsInteger checks the remaining native unsigned
// integer kind, without changing which kinds URL Scalar getters admit.
func TestValidateJSONUintptrTypeReportsInteger(t *testing.T) {
	t.Parallel()

	type input struct {
		// Value uses a native JSON integer representation.
		Value uintptr `json:"value"`
	}
	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"value":"bad"}`))

	_, err := request.ValidateJSON(r, validate.Struct[input]())
	errs := requireErrors(t, err)

	requireCodes(t, errs, "/value", "decode")

	if got := errs["/value"][0].Params["default_representation"]; got != "integer" {
		t.Fatalf("default representation = %v, want native unsigned integer", got)
	}
}

// TestJSONUintptrUsesNativeIntegerRepresentation proves this diagnostic label
// describes a supported JSON v2 number target, not an unsupported Go type.
func TestJSONUintptrUsesNativeIntegerRepresentation(t *testing.T) {
	t.Parallel()

	type input struct {
		// Value is an unsigned Go integer with pointer-sized range.
		Value uintptr `json:"value"`
	}
	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"value":42}`))

	value, err := request.JSON[input](r)

	if err != nil || value.Value != 42 {
		t.Fatalf("value = %v, error = %v, want supported native integer decoding", value.Value, err)
	}
}

// TestValidateJSONUsesEscapedSchemaFieldPath checks that a schema violation is
// keyed by the JSON member name, with JSON Pointer escaping, not the Go name.
func TestValidateJSONUsesEscapedSchemaFieldPath(t *testing.T) {
	t.Parallel()

	type input struct {
		// Name has punctuation that must be escaped in the response's JSON Pointer.
		Name string `json:"a/b~name"`
	}
	schema := validate.Struct(func(object *validate.StructBuilder[input]) {
		object.Field("Name", func(name *validate.StringBuilder[string]) {
			name.Min(3)
		})
	})

	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"a/b~name":"ab"}`))

	_, err := request.ValidateJSON(r, schema)

	requireCodes(t, requireErrors(t, err), "/a~1b~0name", "string.min")
}

// TestValidateJSONUsesEscapedDecodeFieldPath checks that native decoder and
// schema diagnostics use the same JSON member path rather than the Go field.
func TestValidateJSONUsesEscapedDecodeFieldPath(t *testing.T) {
	t.Parallel()

	type input struct {
		// Name has a JSON name that differs from its Go selector.
		Name string `json:"a/b~name"`
	}
	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"a/b~name":42}`))

	_, err := request.ValidateJSON(r, validate.Struct[input]())

	requireCodes(t, requireErrors(t, err), "/a~1b~0name", "decode")
}

// TestParseJSONUnmarshalerFailureRemainsServerError checks that decoding fails
// before schema parsing can turn an operational callback failure into a 422.
func TestParseJSONUnmarshalerFailureRemainsServerError(t *testing.T) {
	t.Parallel()

	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{}`))

	_, err := request.ParseJSON(r, validate.Struct[unavailableJSON]())

	if !errors.Is(err, io.ErrClosedPipe) || errors.Is(err, request.ErrValidationFailed) {
		t.Fatalf("error = %v, want retained operational cause without validation feedback", err)
	}
}

// TestJSONPreservesExplicitOperationalProblem prevents an EOF-shaped cause
// from replacing an already classified server failure with a 400 input problem.
func TestJSONPreservesExplicitOperationalProblem(t *testing.T) {
	t.Parallel()

	failure := problem.New(func(details *problem.Builder) {
		details.Type("tag:example.test,2026:body-read-failure")
		details.Title("Body read failed")
		details.Status(http.StatusInternalServerError)
		details.Error(io.EOF)
	})

	r := httptest.NewRequest(http.MethodPost, "/", errReader{err: failure})

	_, err := request.JSON[protocolInput](r)
	details := requireBodyProblemDetails(t, err)

	if details.Status() != http.StatusInternalServerError || details.Type() != failure.Type() {
		t.Fatalf("problem = %v, want the explicit operational problem", details)
	}
}

// TestJSONIgnoresContentType preserves the documented separation between body
// decoding and endpoint media-type policy; the middleware enforces that policy.
func TestJSONIgnoresContentType(t *testing.T) {
	t.Parallel()

	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"name":"accepted"}`))
	r.Header.Set("Content-Type", "text/plain")

	value, err := request.JSON[protocolInput](r)

	if err != nil || value.Name != "accepted" {
		t.Fatalf("value = %+v, error = %v, want explicitly decoded bytes", value, err)
	}
}

// TestUnlimitedCannotRaiseMiddlewareCeiling checks unknown-length input, where
// the physical MaxBytesReader rather than Content-Length enforces the cap.
func TestUnlimitedCannotRaiseMiddlewareCeiling(t *testing.T) {
	t.Parallel()

	handler := bodylimit.Middleware(func(limit *bodylimit.Builder) {
		limit.Limit(4)
	})(func(_ http.ResponseWriter, r *http.Request) error {
		_, err := request.Bytes(r, request.Unlimited())

		return err
	})

	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader("12345"))
	r.ContentLength = -1

	err := handler(httptest.NewRecorder(), r)

	if !errors.Is(err, request.ErrBodyTooLarge) {
		t.Fatalf("error = %v, want middleware body ceiling", err)
	}
}

// TestJSONRejectsSecondValue confirms JSON consumes exactly one value, unlike
// the explicitly first-element XML contract.
func TestJSONRejectsSecondValue(t *testing.T) {
	t.Parallel()

	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{} {}`))

	_, err := request.JSON[protocolInput](r)

	if !errors.Is(err, request.ErrInvalidJSON) {
		t.Fatalf("error = %v, want invalid trailing JSON data", err)
	}
}

// TestRequestFailureDoesNotReplaceCommittedResponse checks the public router
// seam after a handler has already accepted response bytes.
func TestRequestFailureDoesNotReplaceCommittedResponse(t *testing.T) {
	t.Parallel()

	handler := router.Handler(func(writer http.ResponseWriter, r *http.Request) error {
		if err := response.String(writer, http.StatusOK, "accepted"); err != nil {
			return err
		}

		_, err := request.JSON[protocolInput](r)

		return err
	})

	writer := httptest.NewRecorder()

	handler.ServeHTTP(writer, httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{`)))

	if writer.Code != http.StatusOK || writer.Body.String() != "accepted" {
		t.Fatalf("response = %d %q, want only committed bytes", writer.Code, writer.Body.String())
	}
}
