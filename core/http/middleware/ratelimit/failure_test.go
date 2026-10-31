package ratelimit_test

import (
	"encoding/json/v2"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/synctest"

	"arkoren.dev/core/http/middleware/ratelimit"
	"arkoren.dev/core/http/router"
	"arkoren.dev/foundation/problem"
)

// rateProblemResponse reaches a denial through public middleware. Callers run
// inside synctest so the two requests share a fixed instant and window policy.
// Both response bodies remain owned by the test and are closed at cleanup.
func rateProblemResponse(t *testing.T, failure problem.Details) *http.Response {
	t.Helper()

	handler := ratelimit.Middleware(newLimiter(), func(policy *ratelimit.Builder) {
		policy.Limit(1)
		policy.ErrorResponse(failure)
	})(func(writer http.ResponseWriter, _ *http.Request) error {
		writer.WriteHeader(http.StatusNoContent)

		return nil
	})

	request := httptest.NewRequest(http.MethodGet, "/limited", nil)
	request.Header.Set("Accept", "application/problem+json")

	first := router.Handler(handler).Record(request)
	t.Cleanup(func() {
		if err := first.Body.Close(); err != nil {
			t.Error(err)
		}
	})

	if first.StatusCode != http.StatusNoContent {
		t.Fatalf("first request status = %d, want 204", first.StatusCode)
	}

	response := router.Handler(handler).Record(request)
	t.Cleanup(func() {
		if err := response.Body.Close(); err != nil {
			t.Error(err)
		}
	})

	return response
}

// checkRateProblem checks observable rendered members and returns the decoded
// body for scenario-specific extension assertions. Expected status and members
// are explicit caller fixtures, not inferred from private middleware settings.
func checkRateProblem(t *testing.T, response *http.Response, expected problem.Details) problem.Details {
	t.Helper()

	if response.StatusCode != expected.Status() {
		t.Fatalf("status = %d, want %d", response.StatusCode, expected.Status())
	}

	var rendered problem.Details

	if err := json.UnmarshalRead(response.Body, &rendered); err != nil {
		t.Fatal(err)
	}

	if rendered.Type() != expected.Type() || rendered.Title() != expected.Title() {
		t.Fatalf("identity = %q / %q, want %q / %q",
			rendered.Type(), rendered.Title(), expected.Type(), expected.Title())
	}

	if rendered.Status() != expected.Status() || rendered.Detail() != expected.Detail() {
		t.Fatalf("status/detail = %d / %q, want %d / %q",
			rendered.Status(), rendered.Detail(), expected.Status(), expected.Detail())
	}

	return rendered
}

// TestRatePolicyStatuslessProblemSurvives preserves meaningful custom members
// and headers. The renderer, not rate-limit defaults, completes absent status.
func TestRatePolicyStatuslessProblemSurvives(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		failure := (problem.Details{}).Derive(func(details *problem.Builder) {
			details.Type("urn:example:budget-exhausted")
			details.Title("Budget exhausted")
			details.Detail("The account's request budget is exhausted.")
			details.Header("X-Rate-Policy", "account-budget")
		})

		response := rateProblemResponse(t, failure)
		expected := failure.Derive(func(details *problem.Builder) {
			details.Status(http.StatusInternalServerError)
		})

		checkRateProblem(t, response, expected)

		if got := response.Header.Get("X-Rate-Policy"); got != "account-budget" {
			t.Fatalf("custom problem header = %q, want account-budget", got)
		}

		if failure.Status() != 0 {
			t.Fatal("HTTP rendering mutated the caller's problem status")
		}
	})
}

// TestRatePolicyHeaderOnlyProblemSurvives ensures zero detection includes
// non-wire headers even when every standard problem member is unspecified.
func TestRatePolicyHeaderOnlyProblemSurvives(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		failure := (problem.Details{}).Derive(func(details *problem.Builder) {
			details.Header("X-Rate-Policy", "header-only")
		})

		response := rateProblemResponse(t, failure)

		checkRateProblem(t, response, problem.New())

		if got := response.Header.Get("X-Rate-Policy"); got != "header-only" {
			t.Fatalf("custom problem header = %q, want header-only", got)
		}
	})
}

// TestRatePolicyExtensionOnlyProblemSurvives prevents an extension-only custom
// problem from being confused with the completely zero default selector.
func TestRatePolicyExtensionOnlyProblemSurvives(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		failure := (problem.Details{}).Derive(func(details *problem.Builder) {
			details.Extension("budget", "exhausted")
		})

		response := rateProblemResponse(t, failure)
		rendered := checkRateProblem(t, response, problem.New())
		value, found := rendered.Extension("budget")

		if !found || value != "exhausted" {
			t.Fatalf("budget extension = %v, %t", value, found)
		}
	})
}

// TestRatePolicyCauseOnlyProblemSurvives covers non-wire causes as meaningful
// state. They retain normal renderer completion without leaking cause text.
func TestRatePolicyCauseOnlyProblemSurvives(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		failure := (problem.Details{}).Derive(func(details *problem.Builder) {
			details.Error(errors.New("private budget backend diagnostic"))
		})

		response := rateProblemResponse(t, failure)

		checkRateProblem(t, response, problem.New())
	})
}

// TestRatePolicyCompletelyZeroProblemSelectsDefault preserves the established
// explicit-zero setting: denials render the standard 429 problem unchanged.
func TestRatePolicyCompletelyZeroProblemSelectsDefault(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		response := rateProblemResponse(t, problem.Details{})

		checkRateProblem(t, response, ratelimit.ErrRateLimited)
	})
}
