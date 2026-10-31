package problem_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"arkoren.dev/foundation/problem"
)

// TestBadRequestReturnsExpectedDetails verifies the documented problem behavior.
func TestBadRequestReturnsExpectedDetails(t *testing.T) {
	t.Parallel()

	assertProblemDetails(t, problem.BadRequest(), problem.New(func(details *problem.Builder) {
		details.Type("about:blank")
		details.Title("Bad Request")
		details.Status(http.StatusBadRequest)
	}))
}

// TestForbiddenReturnsExpectedDetails verifies the documented problem behavior.
func TestForbiddenReturnsExpectedDetails(t *testing.T) {
	t.Parallel()

	assertProblemDetails(t, problem.Forbidden(), problem.New(func(details *problem.Builder) {
		details.Type("about:blank")
		details.Title("Forbidden")
		details.Status(http.StatusForbidden)
	}))
}

// TestNotFoundReturnsExpectedDetails verifies the documented problem behavior.
func TestNotFoundReturnsExpectedDetails(t *testing.T) {
	t.Parallel()

	assertProblemDetails(t, problem.NotFound(), problem.New(func(details *problem.Builder) {
		details.Type("about:blank")
		details.Title("Not Found")
		details.Status(http.StatusNotFound)
	}))
}

// TestUnprocessableContentReturnsExpectedDetails verifies the documented problem behavior.
func TestUnprocessableContentReturnsExpectedDetails(t *testing.T) {
	t.Parallel()

	assertProblemDetails(t, problem.UnprocessableContent(), problem.New(func(details *problem.Builder) {
		details.Type("about:blank")
		details.Title("Unprocessable Content")
		details.Status(http.StatusUnprocessableEntity)
	}))
}

// TestInternalServerErrorReturnsExpectedDetails verifies the documented problem behavior.
func TestInternalServerErrorReturnsExpectedDetails(t *testing.T) {
	t.Parallel()

	assertProblemDetails(t, problem.InternalServerError(), problem.New(func(details *problem.Builder) {
		details.Type("about:blank")
		details.Title("Internal Server Error")
		details.Status(http.StatusInternalServerError)
	}))
}

// TestUnauthorizedServesAuthenticationChallenge verifies the documented problem behavior.
func TestUnauthorizedServesAuthenticationChallenge(t *testing.T) {
	t.Parallel()

	request := httptest.NewRequest(http.MethodGet, "/protected", nil)
	request.Header.Set("Accept", "application/problem+json")
	recorder := httptest.NewRecorder()

	problem.Unauthorized(`Basic realm="api"`).ServeHTTP(recorder, request)

	if value := recorder.Header().Get("WWW-Authenticate"); value != `Basic realm="api"` {
		t.Fatalf("expected challenge %q, got %q", `Basic realm="api"`, value)
	}

	assertProblemResponse(t, recorder, http.StatusUnauthorized, "application/problem+json", map[string]any{
		"type":     "about:blank",
		"instance": "/protected",
	})
}

// TestConflictReturnsExpectedDetails verifies the documented problem behavior.
func TestConflictReturnsExpectedDetails(t *testing.T) {
	t.Parallel()

	assertProblemDetails(t, problem.Conflict(), problem.New(func(details *problem.Builder) {
		details.Type("about:blank")
		details.Title("Conflict")
		details.Status(http.StatusConflict)
	}))
}

// TestUnauthorizedReturnsExpectedDetails checks stored identity and the owned
// challenge header before rendering can supply any missing standard members.
func TestUnauthorizedReturnsExpectedDetails(t *testing.T) {
	t.Parallel()

	assertProblemDetails(t, problem.Unauthorized(`Basic realm="api"`), problem.New(func(details *problem.Builder) {
		details.Type("about:blank")
		details.Title("Unauthorized")
		details.Status(http.StatusUnauthorized)
		details.Header("WWW-Authenticate", `Basic realm="api"`)
	}))
}

// TestStatusHelpersMatchEquivalentNew verifies every reusable helper stores
// constructor defaults and matches the same stored identity in both directions.
// Headers are occurrence metadata, not part of the Type/Status/Title tuple.
func TestStatusHelpersMatchEquivalentNew(t *testing.T) {
	t.Parallel()

	for _, template := range []problem.Details{
		problem.BadRequest(), problem.Unauthorized(`Basic realm="api"`),
		problem.Forbidden(), problem.NotFound(), problem.Conflict(),
		problem.UnprocessableContent(), problem.InternalServerError(),
	} {
		expected := problem.New(func(details *problem.Builder) {
			details.Status(template.Status())
			details.Title(template.Title())
		})

		if !errors.Is(template, expected) || !errors.Is(expected, template) {
			t.Fatalf("helper does not match equivalent New: %#v", template)
		}
	}
}
