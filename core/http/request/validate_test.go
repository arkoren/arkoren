package request_test

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"arkoren.dev/core/http/request"
	"arkoren.dev/core/http/router"
	"arkoren.dev/foundation/problem"
	"arkoren.dev/foundation/validate"
)

// ExampleValidateJSON reads a qualified default-representation hint.
func ExampleValidateJSON() {
	type input struct {
		// Data uses the default base64-encoded JSON string representation.
		Data []byte `json:"data"`
	}
	incoming := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"data":42}`))

	_, err := request.ValidateJSON(incoming, validate.Struct[input]())
	errs, ok := errors.AsType[validate.Errors](err)

	if !ok {
		fmt.Println(err)

		return
	}

	fmt.Println(errs["/data"][0].Params["default_representation"])
	// Output: string
}

// signup is the decoded shape shared by the validation helper tests.
type signup struct {
	// XMLName selects the signup XML root without contributing a JSON member.
	XMLName xml.Name `json:"-" xml:"signup"`
	// Email exercises required-field and minimum-length validation.
	Email string `json:"email" xml:"email"`
	// Age exercises integer decoding and the minimum-age rule.
	Age int `json:"age" xml:"age"`
	// Plan receives the schema default when normalization is requested.
	Plan string `json:"plan" xml:"plan"`
}

// signupSchema requires an email and defaults the plan to "free".
var signupSchema = validate.Struct(func(object *validate.StructBuilder[signup]) {
	object.Field("Email", func(email *validate.StringBuilder[string]) {
		email.Required()
		email.Min(5)
	})
	object.Field("Age", func(age *validate.IntBuilder[int]) {
		age.Min(18)
	})
	object.Field("Plan", func(plan *validate.StringBuilder[string]) {
		plan.OneOf("free", "pro")
		plan.Default("free")
	})
})

const (
	// validJSON is a valid signup payload with no plan, allowing normalization
	// tests to observe its default.
	validJSON = `{"email":"a@example.com","age":30}`
	// validXML is the XML equivalent of validJSON, exercising the same validation
	// rules through a different decoder.
	validXML = `<signup><email>a@example.com</email><age>30</age></signup>`
)

// newBody returns a POST request carrying body.
func newBody(body string) *http.Request {
	return httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
}

// requireErrors returns the validate.Errors in err or fails the test.
func requireErrors(t *testing.T, err error) validate.Errors {
	t.Helper()

	errs, ok := errors.AsType[validate.Errors](err)

	if !ok {
		t.Fatalf("expected validate.Errors, got %v", err)
	}

	return errs
}

// requireCodes fails unless the violations at pointer have exactly codes.
func requireCodes(t *testing.T, errs validate.Errors, pointer string, codes ...string) {
	t.Helper()

	violations := errs[pointer]

	if len(violations) != len(codes) {
		t.Fatalf("expected codes %v at %q, got %v", codes, pointer, errs)
	}

	for index, code := range codes {
		if violations[index].Code != code {
			t.Fatalf("expected codes %v at %q, got %v", codes, pointer, errs)
		}
	}
}

// requireStatus fails unless err is a problem with status.
func requireStatus(t *testing.T, err error, status int) {
	t.Helper()

	details, ok := errors.AsType[problem.Details](err)

	if !ok || details.Status() != status {
		t.Fatalf("expected status %d, got %v", status, err)
	}
}

// TestValidateJSON verifies decoding, validation and the absence of defaults.
func TestValidateJSON(t *testing.T) {
	t.Parallel()

	value, err := request.ValidateJSON(newBody(validJSON), signupSchema)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if value.Email != "a@example.com" || value.Plan != "" {
		t.Fatalf("unexpected value %+v", value)
	}

	_, err = request.ValidateJSON(newBody(`{"email":"a","age":3}`), signupSchema)
	errs := requireErrors(t, err)

	requireCodes(t, errs, "/email", "string.min")
	requireCodes(t, errs, "/age", "number.min")
}

// TestParseJSON verifies defaults are applied.
func TestParseJSON(t *testing.T) {
	t.Parallel()

	value, err := request.ParseJSON(newBody(validJSON), signupSchema)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if value.Plan != "free" {
		t.Fatalf("expected default plan, got %+v", value)
	}

	_, err = request.ParseJSON(newBody(`{"age":30}`), signupSchema)

	requireCodes(t, requireErrors(t, err), "/email", "required")
}

// TestValidateStrictJSON verifies unknown members become violations.
func TestValidateStrictJSON(t *testing.T) {
	t.Parallel()

	_, err := request.ValidateJSON(newBody(validJSON), signupSchema, request.Strict())

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	_, err = request.ValidateJSON(newBody(`{"email":"a@example.com","extra":1}`), signupSchema, request.Strict())
	errs := requireErrors(t, err)

	requireCodes(t, errs, "/extra", "unknown")

	if errs["/extra"][0].Message != "is not a known field" {
		t.Fatalf("unexpected message %q", errs["/extra"][0].Message)
	}
}

// TestParseStrictJSON verifies defaults and unknown member rejection.
func TestParseStrictJSON(t *testing.T) {
	t.Parallel()

	value, err := request.ParseJSON(newBody(validJSON), signupSchema, request.Strict())

	if err != nil || value.Plan != "free" {
		t.Fatalf("unexpected result %+v, %v", value, err)
	}

	_, err = request.ParseJSON(newBody(`{"extra":1}`), signupSchema, request.Strict())

	requireCodes(t, requireErrors(t, err), "/extra", "unknown")
}

// TestValidateLimitedJSON verifies the size limit and validation.
func TestValidateLimitedJSON(t *testing.T) {
	t.Parallel()

	value, err := request.ValidateJSON(newBody(validJSON), signupSchema, request.MaxSize(-1))

	if err != nil || value.Plan != "" {
		t.Fatalf("unexpected result %+v, %v", value, err)
	}

	_, err = request.ValidateJSON(newBody(validJSON), signupSchema, request.MaxSize(5))

	requireStatus(t, err, http.StatusRequestEntityTooLarge)
}

// TestParseLimitedJSON verifies the size limit and defaults.
func TestParseLimitedJSON(t *testing.T) {
	t.Parallel()

	value, err := request.ParseJSON(newBody(validJSON), signupSchema, request.MaxSize(1024))

	if err != nil || value.Plan != "free" {
		t.Fatalf("unexpected result %+v, %v", value, err)
	}

	_, err = request.ParseJSON(newBody(validJSON), signupSchema, request.MaxSize(5))

	requireStatus(t, err, http.StatusRequestEntityTooLarge)
}

// TestValidateStrictLimitedJSON verifies the limit and unknown members.
func TestValidateStrictLimitedJSON(t *testing.T) {
	t.Parallel()

	_, err := request.ValidateJSON(newBody(validJSON), signupSchema, request.Strict(), request.MaxSize(1024))

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	_, err = request.ValidateJSON(newBody(`{"extra":1}`), signupSchema, request.Strict(), request.MaxSize(1024))

	requireCodes(t, requireErrors(t, err), "/extra", "unknown")

	_, err = request.ValidateJSON(newBody(validJSON), signupSchema, request.Strict(), request.MaxSize(5))

	requireStatus(t, err, http.StatusRequestEntityTooLarge)
}

// TestParseStrictLimitedJSON verifies the limit, defaults and unknown members.
func TestParseStrictLimitedJSON(t *testing.T) {
	t.Parallel()

	value, err := request.ParseJSON(newBody(validJSON), signupSchema, request.Strict(), request.MaxSize(1024))

	if err != nil || value.Plan != "free" {
		t.Fatalf("unexpected result %+v, %v", value, err)
	}

	_, err = request.ParseJSON(newBody(`{"extra":1}`), signupSchema, request.Strict(), request.MaxSize(1024))

	requireCodes(t, requireErrors(t, err), "/extra", "unknown")

	_, err = request.ParseJSON(newBody(validJSON), signupSchema, request.Strict(), request.MaxSize(5))

	requireStatus(t, err, http.StatusRequestEntityTooLarge)
}

// TestValidateXML verifies XML validation and that decode errors stay 400.
func TestValidateXML(t *testing.T) {
	t.Parallel()

	value, err := request.ValidateXML(newBody(validXML), signupSchema)

	if err != nil || value.Plan != "" {
		t.Fatalf("unexpected result %+v, %v", value, err)
	}

	_, err = request.ValidateXML(newBody(`<signup><email>a</email></signup>`), signupSchema)

	requireCodes(t, requireErrors(t, err), "/email", "string.min")

	_, err = request.ValidateXML(newBody("<broken>"), signupSchema)

	requireStatus(t, err, http.StatusBadRequest)
}

// TestParseXML verifies defaults for XML bodies.
func TestParseXML(t *testing.T) {
	t.Parallel()

	value, err := request.ParseXML(newBody(validXML), signupSchema)

	if err != nil || value.Plan != "free" {
		t.Fatalf("unexpected result %+v, %v", value, err)
	}

	_, err = request.ParseXML(newBody("<broken>"), signupSchema)

	requireStatus(t, err, http.StatusBadRequest)
}

// TestValidateLimitedXML verifies the XML size limit.
func TestValidateLimitedXML(t *testing.T) {
	t.Parallel()

	value, err := request.ValidateXML(newBody(validXML), signupSchema, request.MaxSize(-1))

	if err != nil || value.Plan != "" {
		t.Fatalf("unexpected result %+v, %v", value, err)
	}

	_, err = request.ValidateXML(newBody(validXML), signupSchema, request.MaxSize(5))

	requireStatus(t, err, http.StatusRequestEntityTooLarge)
}

// TestParseLimitedXML verifies defaults and the XML size limit.
func TestParseLimitedXML(t *testing.T) {
	t.Parallel()

	value, err := request.ParseXML(newBody(validXML), signupSchema, request.MaxSize(1024))

	if err != nil || value.Plan != "free" {
		t.Fatalf("unexpected result %+v, %v", value, err)
	}

	_, err = request.ParseXML(newBody(validXML), signupSchema, request.MaxSize(5))

	requireStatus(t, err, http.StatusRequestEntityTooLarge)
}

// TestValidateJSONTypeViolation verifies native decode feedback merges with
// schema violations and retains a qualified default, not a wire-kind demand.
func TestValidateJSONTypeViolation(t *testing.T) {
	t.Parallel()

	_, err := request.ValidateJSON(newBody(`{"email":"a","age":"old"}`), signupSchema)
	errs := requireErrors(t, err)

	requireCodes(t, errs, "/age", "decode")
	requireCodes(t, errs, "/email", "string.min")

	violation := errs["/age"][0]

	if violation.Params["default_representation"] != "integer" {
		t.Fatalf("unexpected default representation %+v", violation)
	}

	if violation.Message != "cannot decode value using the configured JSON representation" {
		t.Fatalf("unexpected violation %+v", violation)
	}

	_, err = request.ValidateJSON(newBody(`{"age":"old","email":"a@example.com"}`), signupSchema)

	requireCodes(t, requireErrors(t, err), "/age", "decode")

	_, err = request.ValidateJSON(newBody(`{"age":"old"}`), signupSchema)
	errs = requireErrors(t, err)

	if len(errs) != 1 {
		t.Fatalf("expected only the decode violation, got %v", errs)
	}
}

// TestValidateJSONLocalizesViolations verifies the context catalog is used.
func TestValidateJSONLocalizesViolations(t *testing.T) {
	t.Parallel()

	catalog := validate.Catalog{
		Default: "es",
		Locales: map[string]validate.Messages{
			"es": {
				"decode": "no se puede decodificar con la representación JSON configurada",
			},
		},
	}
	ctx := validate.NewCatalogContext(context.Background(), catalog)
	req := newBody(`{"age":"old"}`).WithContext(ctx)

	_, err := request.ValidateJSON(req, signupSchema)
	errs := requireErrors(t, err)

	if errs["/age"][0].Message != "no se puede decodificar con la representación JSON configurada" {
		t.Fatalf("unexpected message %q", errs["/age"][0].Message)
	}
}

// TestValidateJSONMalformed verifies malformed JSON stays a 400.
func TestValidateJSONMalformed(t *testing.T) {
	t.Parallel()

	_, err := request.ValidateJSON(newBody(`{"email":`), signupSchema)

	requireStatus(t, err, http.StatusBadRequest)
}

// TestValidateJSONCheckError verifies a Check infrastructure error passes through.
func TestValidateJSONCheckError(t *testing.T) {
	t.Parallel()

	failure := errors.New("database down")
	schema := validate.Struct(func(object *validate.StructBuilder[signup]) {
		object.Field("Email", func(email *validate.StringBuilder[string]) {
			email.Check(func(context.Context, string) error {
				return failure
			})
		})
	})

	_, err := request.ValidateJSON(newBody(validJSON), schema)

	if !errors.Is(err, failure) {
		t.Fatalf("expected check error, got %v", err)
	}
}

// checkValidationProblem fails the test unless request.ValidationProblem(err)
// returns nil (when want is nil) or an error matching want, with a
// validate.Errors in its chain exactly when wantErrors is true.
func checkValidationProblem(t *testing.T, err, want error, wantErrors bool) {
	t.Helper()

	got := request.ValidationProblem(err)

	if want == nil {
		if got != nil {
			t.Fatalf("got %v, want nil", got)
		}

		return
	}

	if !errors.Is(got, want) {
		t.Fatalf("got %v, want errors.Is %v", got, want)
	}

	if _, ok := errors.AsType[validate.Errors](got); ok != wantErrors {
		t.Fatalf("validate.Errors in chain = %v, want %v", ok, wantErrors)
	}
}

// violationErrors returns validate.Errors with one violation at /email.
func violationErrors() validate.Errors {
	errs := validate.Errors{}
	errs.Add("/email", validate.Violation{
		Code: "required",
	})

	return errs
}

// TestValidationProblemNil verifies nil passes through as nil.
func TestValidationProblemNil(t *testing.T) {
	t.Parallel()

	checkValidationProblem(t, nil, nil, false)
}

// TestValidationProblemOtherErrorUnchanged verifies other errors pass through.
func TestValidationProblemOtherErrorUnchanged(t *testing.T) {
	t.Parallel()

	other := errors.New("boom")

	checkValidationProblem(t, other, other, false)
}

// TestValidationProblemProblemUnchanged verifies problems pass through.
func TestValidationProblemProblemUnchanged(t *testing.T) {
	t.Parallel()

	checkValidationProblem(t, request.ErrInvalidJSON, request.ErrInvalidJSON, false)
}

// TestValidationProblemBareValidateErrors verifies the conversion of bare
// validate.Errors to ErrValidationFailed.
func TestValidationProblemBareValidateErrors(t *testing.T) {
	t.Parallel()

	checkValidationProblem(t, violationErrors(), request.ErrValidationFailed, true)
}

// TestValidationProblemWrappedValidateErrors verifies the conversion of wrapped
// validate.Errors to ErrValidationFailed.
func TestValidationProblemWrappedValidateErrors(t *testing.T) {
	t.Parallel()

	checkValidationProblem(t, fmt.Errorf("create: %w", violationErrors()), request.ErrValidationFailed, true)
}

// TestValidationProblemResponseBytes verifies the rendered 422 response is
// byte-for-byte what the router produced before it stopped special-casing
// validate.Errors.
func TestValidationProblemResponseBytes(t *testing.T) {
	t.Parallel()

	const want = `{"type":"tag:arkoren.dev,2026-09:core:http/request:validation-failed","title":"Validation failed","status":422,"instance":"/users","errors":{"/email":[{"code":"string.min","params":{"min":3},"message":"must be at least 3 characters"}]}}` + "\n"

	errs := validate.Errors{}
	errs.Add("/email", validate.Violation{
		Code: "string.min",
		Params: map[string]any{
			"min": 3,
		},
		Message: "must be at least 3 characters",
	})

	for _, err := range []error{errs, fmt.Errorf("create user: %w", errs)} {
		handler := router.Handler(func(http.ResponseWriter, *http.Request) error {
			return request.ValidationProblem(err)
		})

		httpRequest := httptest.NewRequest(http.MethodPost, "/users", nil)
		httpRequest.Header.Set("Accept", "application/problem+json")

		response := handler.Record(httpRequest)

		body, readErr := io.ReadAll(response.Body)

		if readErr != nil {
			t.Fatalf("read body: %v", readErr)
		}

		if response.StatusCode != http.StatusUnprocessableEntity || response.Header.Get("Content-Type") != "application/problem+json" {
			t.Fatalf("response = %d %q", response.StatusCode, response.Header.Get("Content-Type"))
		}

		if string(body) != want {
			t.Fatalf("body = %q, want %q", body, want)
		}
	}
}
