package request_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"arkoren.dev/core/http/auth"
	"arkoren.dev/core/http/request"
	"arkoren.dev/foundation/problem"
)

// TestUserReturnsStoredUser verifies the user is read from the context.
func TestUserReturnsStoredUser(t *testing.T) {
	t.Parallel()

	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r = r.WithContext(auth.NewContext(r.Context(), "ann"))

	if got, err := request.User[string](r); err != nil || got != "ann" {
		t.Fatalf("got %q, %v", got, err)
	}
}

// TestUserReturnsErrUnauthenticatedWhenAbsent verifies the missing case.
func TestUserReturnsErrUnauthenticatedWhenAbsent(t *testing.T) {
	t.Parallel()

	if _, err := request.User[string](httptest.NewRequest(http.MethodGet, "/", nil)); !errors.As(err, new(problem.Details)) {
		t.Fatalf("err = %v", err)
	}
}
