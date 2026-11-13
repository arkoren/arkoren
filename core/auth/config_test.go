package auth

import (
	"net/http"
	"testing"

	"arkoren.dev/foundation/problem"
)

// TestCompletedSettingsAppliesCallbacksAfterReplacement verifies callback order.
func TestCompletedSettingsAppliesCallbacksAfterReplacement(t *testing.T) {
	t.Parallel()

	config := newMiddlewareSettings(
		func(authenticator *MiddlewareBuilder) {
			authenticator.settings = middlewareSettings{
				ErrorResponse: problem.Forbidden(),
			}
		},
		func(authenticator *MiddlewareBuilder) {
			authenticator.ErrorResponse(problem.NotFound())
		},
	)

	if config.ErrorResponse.Status() != http.StatusNotFound {
		t.Fatalf("status = %d", config.ErrorResponse.Status())
	}
}

// TestConstructionCallbacksRunOnceInOrder checks the public constructor, identical
// draft receivers, and replacement order without executing request-time hooks.
func TestConstructionCallbacksRunOnceInOrder(t *testing.T) {
	t.Parallel()

	var retained *MiddlewareBuilder
	order := 0
	Require(func(authenticator *MiddlewareBuilder) {
		retained = authenticator
		order++

		if authenticator.ErrorResponse(problem.Forbidden()) != authenticator {
			t.Fatal("setting returned another draft")
		}
	}, func(authenticator *MiddlewareBuilder) {
		if order != 1 || authenticator != retained {
			t.Fatal("callbacks were reordered or used different drafts")
		}

		order++
	})

	if order != 2 {
		t.Fatalf("callback count = %d, want 2", order)
	}
}

// TestConstructionOmissionAndEmptyExpansions supply zero callbacks. A supplied nil
// is instead misuse and must panic before a later callback can execute.
func TestConstructionOmissionAndEmptyExpansions(t *testing.T) {
	t.Parallel()

	var omitted []func(*MiddlewareBuilder)
	Require(omitted...)
	Require([]func(*MiddlewareBuilder){}...)

	reached := false
	defer func() {
		if recover() == nil || reached {
			t.Fatal("nil entry was filtered or later callback ran")
		}
	}()
	Require(nil, func(authenticator *MiddlewareBuilder) {
		reached = true
	})
}

// TestConstructionApplicationPanicPropagates keeps callback misuse separate from
// ordinary constructor validation and prevents later policies from executing.
func TestConstructionApplicationPanicPropagates(t *testing.T) {
	t.Parallel()

	reached := false
	defer func() {
		if value := recover(); value != "construction sentinel" || reached {
			t.Fatalf("panic = %v, later callback = %v", value, reached)
		}
	}()
	Require(func(authenticator *MiddlewareBuilder) {
		panic("construction sentinel")
	}, func(authenticator *MiddlewareBuilder) {
		reached = true
	})
}

// TestBuilderPublicationDetachesRoot checks completed settings do not retain the
// mutable construction root. Services and function captures remain shared.
func TestBuilderPublicationDetachesRoot(t *testing.T) {
	t.Parallel()

	var retained *MiddlewareBuilder
	config := newMiddlewareSettings(func(authenticator *MiddlewareBuilder) {
		retained = authenticator
		authenticator.ErrorResponse(problem.Forbidden())
	})
	retained.ErrorResponse(problem.NotFound())

	if config.ErrorResponse.Status() != 403 {
		t.Fatal("retained draft changed published settings")
	}
}
