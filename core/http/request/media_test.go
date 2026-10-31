package request_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"arkoren.dev/core/http/middleware/contenttype"
	"arkoren.dev/core/http/request"
)

// TestErrUnsupportedMediaTypeMatchesMiddleware checks stable identity matching.
func TestErrUnsupportedMediaTypeMatchesMiddleware(t *testing.T) {
	t.Parallel()

	if !errors.Is(request.ErrUnsupportedMediaType, contenttype.ErrUnsupportedMediaType) {
		t.Error("request.ErrUnsupportedMediaType does not match contenttype.ErrUnsupportedMediaType")
	}

	if !errors.Is(contenttype.ErrUnsupportedMediaType, request.ErrUnsupportedMediaType) {
		t.Error("contenttype.ErrUnsupportedMediaType does not match request.ErrUnsupportedMediaType")
	}
}

// TestContentTypePolicyRejectsMislabeledJSON checks the explicit policy boundary.
func TestContentTypePolicyRejectsMislabeledJSON(t *testing.T) {
	t.Parallel()

	handler := contenttype.Middleware()(func(_ http.ResponseWriter, r *http.Request) error {
		_, err := request.JSON[protocolInput](r)

		return err
	})

	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"name":"rejected"}`))
	r.Header.Set("Content-Type", "text/plain")

	err := handler(httptest.NewRecorder(), r)

	if !errors.Is(err, contenttype.ErrUnsupportedMediaType) {
		t.Fatalf("error = %v, want media-type policy rejection", err)
	}
}
