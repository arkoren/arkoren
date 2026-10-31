package request_test

import (
	"context"
	"encoding/json/v2"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"arkoren.dev/core/http/request"
	"arkoren.dev/core/http/router"
	"arkoren.dev/foundation/validate"
)

// TestParseJSONQuotedNumberDoesNotGuessWireKind proves the string format is
// supported, then rejects an unquoted number without prescribing a false kind.
func TestParseJSONQuotedNumberDoesNotGuessWireKind(t *testing.T) {
	t.Parallel()

	type input struct {
		// Count requires a JSON string even though its Go output is an integer.
		Count int8 `json:"count,string"`
	}
	schema := validate.Struct[input]()
	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"count":"42"}`))

	value, err := request.ParseJSON(r, schema)

	if err != nil || value.Count != 42 {
		t.Fatalf("value = %+v, error = %v, want supported quoted-number decoding", value, err)
	}

	r = httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"count":42}`))

	_, err = request.ParseJSON(r, schema)

	if !errors.Is(err, request.ErrValidationFailed) {
		t.Fatalf("error = %v, want the stable validation problem", err)
	}

	errs := requireErrors(t, err)

	requireCodes(t, errs, "/count", "decode")

	if _, present := errs["/count"][0].Params["expected"]; present {
		t.Fatalf("diagnostic incorrectly prescribes a wire kind: %+v", errs["/count"][0])
	}

	if got := errs["/count"][0].Params["default_representation"]; got != "integer" {
		t.Fatalf("default representation = %v, want a qualified integer hint", got)
	}

	semantic, ok := errors.AsType[*json.SemanticError](err)

	if !ok || semantic.JSONPointer != "/count" {
		t.Fatal("the native decoding cause or its path was lost")
	}

	if errors.Is(err, request.ErrInvalidJSON) {
		t.Fatal("the intermediate 400 problem identity leaked into the 422 result")
	}
}

// TestValidateJSONQuotedNumberInvalidContentsDoesNotGuessWireKind rejects
// invalid contents inside the correct wire kind without demanding an integer.
// Retaining the native cause must not expose its values or Go types over HTTP.
func TestValidateJSONQuotedNumberInvalidContentsDoesNotGuessWireKind(t *testing.T) {
	t.Parallel()

	type input struct {
		// Count still requires a JSON string when its contents are invalid.
		Count int8 `json:"count,string"`
	}
	var err error
	handler := router.Handler(func(_ http.ResponseWriter, r *http.Request) error {
		_, err = request.ValidateJSON(r, validate.Struct[input]())

		return err
	})

	writer := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"count":"not-a-number"}`))

	handler.ServeHTTP(writer, r)

	errs := requireErrors(t, err)

	requireCodes(t, errs, "/count", "decode")

	violation := errs["/count"][0]

	if _, present := violation.Params["expected"]; present {
		t.Fatalf("diagnostic prescribes a wire kind despite a field format: %+v", violation)
	}

	if strings.Contains(violation.Message, "not-a-number") {
		t.Fatalf("message echoed input instead of using safe generic feedback: %q", violation.Message)
	}

	if writer.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want validation feedback", writer.Code)
	}

	body := writer.Body.String()

	if strings.Contains(body, "not-a-number") || strings.Contains(body, "int8") {
		t.Fatalf("native decoding details leaked over HTTP: %s", body)
	}
}

// TestValidateJSONDefaultStringRetainsQualifiedHint protects useful diagnostics
// for a plain field without treating GoType as a field-annotation oracle.
func TestValidateJSONDefaultStringRetainsQualifiedHint(t *testing.T) {
	t.Parallel()

	type input struct {
		// Name has an ordinary string representation without special options.
		Name string `json:"name"`
	}
	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"name":42}`))

	_, err := request.ValidateJSON(r, validate.Struct[input]())
	violation := requireErrors(t, err)["/name"][0]

	if violation.Code != "decode" || violation.Params["default_representation"] != "string" {
		t.Fatalf("plain string hint was lost: %+v", violation)
	}

	if _, present := violation.Params["expected"]; present {
		t.Fatalf("default hint was presented as a wire expectation: %+v", violation)
	}
}

// TestValidateJSONSchemaTypeCodeIsUnchanged distinguishes a schema's explicit
// wire-kind rule from the native decoder's output-type-only diagnostic metadata.
func TestValidateJSONSchemaTypeCodeIsUnchanged(t *testing.T) {
	t.Parallel()

	schema := validate.Any(func(value *validate.AnyBuilder) {
		value.Check(func(_ context.Context, decoded any) error {
			if _, ok := decoded.(string); !ok {
				return validate.Fail("type", map[string]any{"expected": "string"})
			}

			return nil
		})
	})

	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`42`))

	_, err := request.ValidateJSON(r, schema)
	errs := requireErrors(t, err)

	requireCodes(t, errs, "", "type")

	violation := errs[""][0]

	if violation.Params["expected"] != "string" || violation.Message != "must be of type string" {
		t.Fatalf("schema-authored type feedback changed: %+v", violation)
	}

	if _, ok := errors.AsType[*json.SemanticError](err); ok {
		t.Fatal("schema-only failure acquired a native decoding cause")
	}
}

// TestValidateJSONQuotedNumberKeepsEscapedPath protects field-format errors
// below a renamed field whose JSON Pointer needs slash and tilde escaping.
func TestValidateJSONQuotedNumberKeepsEscapedPath(t *testing.T) {
	t.Parallel()

	type input struct {
		// Count's Go name differs from its escaped JSON member name.
		Count int8 `json:"n/count~value,string"`
	}
	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"n/count~value":42}`))

	_, err := request.ValidateJSON(r, validate.Struct[input]())

	if !errors.Is(err, request.ErrValidationFailed) {
		t.Fatalf("error = %v, want the stable 422 problem", err)
	}

	errs := requireErrors(t, err)

	requireCodes(t, errs, "/n~1count~0value", "decode")

	if len(errs) != 1 {
		t.Fatalf("unexpected error paths: %v", errs)
	}
}

// errPrivateJSON is synthetic operational detail that must remain available to
// server-side error handling but never become client-facing decoding advice.
var errPrivateJSON = errors.New("private decoder detail: synthetic credential")

// privateJSONDecoder models an application decoder with an operational error;
// the diagnostic adapter must not infer client feedback from that error text.
type privateJSONDecoder struct{}

// UnmarshalJSON returns the private operational failure without reading data.
// Its caller must retain the cause rather than serializing it as a violation.
//
// Example:
//
//	var value privateJSONDecoder
//	if err := value.UnmarshalJSON([]byte(`{}`)); !errors.Is(err, errPrivateJSON) {
//		t.Fatal(err)
//	}
func (*privateJSONDecoder) UnmarshalJSON([]byte) error {
	return errPrivateJSON
}

// TestParseJSONCustomDecoderFailureStaysPrivate exercises the exported helper
// and router boundary together: a server failure is not a decoding violation.
func TestParseJSONCustomDecoderFailureStaysPrivate(t *testing.T) {
	t.Parallel()

	var err error
	handler := router.Handler(func(_ http.ResponseWriter, r *http.Request) error {
		_, err = request.ParseJSON(r, validate.Struct[privateJSONDecoder]())

		return err
	})

	writer := httptest.NewRecorder()

	handler.ServeHTTP(writer, httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{}`)))

	if !errors.Is(err, errPrivateJSON) || errors.Is(err, request.ErrValidationFailed) {
		t.Fatalf("error = %v, want the operational cause without input feedback", err)
	}

	if writer.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want a server failure", writer.Code)
	}

	if strings.Contains(writer.Body.String(), "private decoder detail") {
		t.Fatalf("response exposed decoder detail: %q", writer.Body.String())
	}
}

// opaqueJSONDecoder explicitly reports a decoding failure, but has an arbitrary
// representation that cannot be inferred from its underlying Go struct.
type opaqueJSONDecoder struct{}

// UnmarshalJSON reports a semantic failure without claiming a concrete wire
// expectation. The native decoder supplies its type and pointer metadata.
//
// Example:
//
//	var value opaqueJSONDecoder
//	if _, ok := errors.AsType[*json.SemanticError](value.UnmarshalJSON([]byte(`{}`))); !ok {
//		t.Fatal("expected a semantic decoding failure")
//	}
func (*opaqueJSONDecoder) UnmarshalJSON([]byte) error {
	return &json.SemanticError{}
}

// TestValidateJSONOpaqueDecoderHasNoInventedRepresentation prevents both Go
// type-name disclosure and a guessed object hint for arbitrary JSON methods.
func TestValidateJSONOpaqueDecoderHasNoInventedRepresentation(t *testing.T) {
	t.Parallel()

	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{}`))

	_, err := request.ValidateJSON(r, validate.Struct[opaqueJSONDecoder]())
	errs := requireErrors(t, err)

	requireCodes(t, errs, "", "decode")

	violation := errs[""][0]

	if len(violation.Params) != 0 {
		t.Fatalf("opaque decoder acquired invented representation metadata: %+v", violation)
	}

	if strings.Contains(violation.Message, "opaqueJSONDecoder") {
		t.Fatalf("message exposed a Go implementation type: %q", violation.Message)
	}
}

// TestJSONUnsupportedFormatTagStaysRejected protects the installed JSON v2
// contract: diagnostic qualification does not add a field-format parser.
func TestJSONUnsupportedFormatTagStaysRejected(t *testing.T) {
	t.Parallel()

	type input struct {
		// Count deliberately uses an option unsupported by this toolchain.
		Count int8 `json:"count,format:array"`
	}
	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"count":42}`))

	_, err := request.JSON[input](r)

	if !errors.Is(err, request.ErrInvalidJSON) {
		t.Fatalf("error = %v, want native rejection of the unsupported tag", err)
	}

	if _, ok := errors.AsType[*json.SemanticError](err); !ok {
		t.Fatal("native unsupported-tag cause was lost")
	}
}

// TestValidateJSONDurationHasNoInventedDefault checks a native type that has no
// default JSON representation, rather than labeling its underlying int64 kind.
func TestValidateJSONDurationHasNoInventedDefault(t *testing.T) {
	t.Parallel()

	type input struct {
		// Delay has no native default; applications must choose a representation.
		Delay time.Duration `json:"delay"`
	}
	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"delay":1}`))

	_, err := request.ValidateJSON(r, validate.Struct[input]())
	errs := requireErrors(t, err)

	requireCodes(t, errs, "/delay", "decode")

	if len(errs["/delay"][0].Params) != 0 {
		t.Fatalf("duration acquired an invented default representation: %+v", errs["/delay"][0])
	}
}
