package router_test

import (
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"arkoren.dev/core/http/router"
	"arkoren.dev/foundation/validate"
)

// validationBody serves err through a handler and returns the response and decoded body.
func validationBody(t *testing.T, err error) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()

	handler := router.Handler(func(http.ResponseWriter, *http.Request) error {
		return err
	})

	request := httptest.NewRequest(http.MethodPost, "/users", nil)
	request.Header.Set("Accept", "application/problem+json")

	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, request)

	var body map[string]any

	if err := json.UnmarshalRead(recorder.Body, &body); err != nil {
		t.Fatalf("failed to decode response: %s", err)
	}

	return recorder, body
}

// newValidationErrors returns errors with one violation at /email.
func newValidationErrors() validate.Errors {
	errs := validate.Errors{}
	errs.Add("/email", validate.Violation{
		Code: "string.min",
		Params: map[string]any{
			"min": 3,
		},
		Message: "must be at least 3 characters",
	})

	return errs
}

// checkRawValidationErrorIs500 fails the test unless err renders as a 500
// without an errors member.
func checkRawValidationErrorIs500(t *testing.T, err error) {
	t.Helper()

	recorder, body := validationBody(t, err)

	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusInternalServerError)
	}

	if _, ok := body["errors"]; ok {
		t.Fatalf("body = %v, want no errors member", body)
	}
}

// TestHandlerRendersBareValidationErrorsAs500 verifies the router no longer
// special-cases validate.Errors: a raw one is an unmapped error and renders 500.
func TestHandlerRendersBareValidationErrorsAs500(t *testing.T) {
	t.Parallel()

	checkRawValidationErrorIs500(t, newValidationErrors())
}

// TestHandlerRendersWrappedValidationErrorsAs500 verifies a wrapped
// validate.Errors is likewise an unmapped error and renders 500.
func TestHandlerRendersWrappedValidationErrorsAs500(t *testing.T) {
	t.Parallel()

	checkRawValidationErrorIs500(t, fmt.Errorf("create user: %w", newValidationErrors()))
}

// TestHandlerDeadlineExceededIsGatewayTimeout checks that a wrapped operation
// deadline error maps to HTTP 504 when the client has not canceled.
func TestHandlerDeadlineExceededIsGatewayTimeout(t *testing.T) {
	t.Parallel()

	handler := router.Handler(func(http.ResponseWriter, *http.Request) error {
		return fmt.Errorf("query: %w", context.DeadlineExceeded)
	})

	response := handler.Record(httptest.NewRequest(http.MethodGet, "/", nil))

	if response.StatusCode != http.StatusGatewayTimeout {
		t.Fatalf("status = %d, want 504", response.StatusCode)
	}
}

// TestHandlerClientGoneIsClientClosedRequest checks that an already canceled
// request maps to the client-closed status rather than an internal server
// error.
func TestHandlerClientGoneIsClientClosedRequest(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	handler := router.Handler(func(_ http.ResponseWriter, httpRequest *http.Request) error {
		return httpRequest.Context().Err()
	})

	response := handler.Record(httptest.NewRequestWithContext(ctx, http.MethodGet, "/", nil))

	if response.StatusCode != router.StatusClientClosedRequest {
		t.Fatalf("status = %d, want 499", response.StatusCode)
	}
}

// TestHandlerErrorDropsRepresentationHeaders checks that rendering a problem
// removes stale length and encoding headers from the abandoned representation.
func TestHandlerErrorDropsRepresentationHeaders(t *testing.T) {
	t.Parallel()

	handler := router.Handler(func(writer http.ResponseWriter, _ *http.Request) error {
		writer.Header().Set("Content-Length", "5")
		writer.Header().Set("Content-Encoding", "gzip")

		return errors.New("failed")
	})

	response := handler.Record(httptest.NewRequest(http.MethodGet, "/", nil))

	if got := response.Header.Get("Content-Encoding"); got != "" {
		t.Fatalf("content-encoding = %q, want none", got)
	}

	if got := response.Header.Get("Content-Length"); got == "5" {
		t.Fatal("stale content-length kept")
	}
}
