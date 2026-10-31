package errormap_test

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"arkoren.dev/core/http/middleware/errormap"
	"arkoren.dev/core/http/request"
	"arkoren.dev/core/http/router"
	"arkoren.dev/foundation/problem"
	"arkoren.dev/foundation/validate"
)

// interfaceError stores arbitrary diagnostic data in an interface field. Its
// type is comparable, but values holding slices cannot be compared with ==.
type interfaceError struct {
	// data is retained without conversion so tests exercise dynamic comparability.
	data any
}

// Error supplies a stable message independent of data; unchanged detection must
// inspect the value rather than infer identity from the rendered message.
//
// Example:
//
//	message := (interfaceError{data: "orders"}).Error()
//	_ = message
func (interfaceError) Error() string {
	return "diagnostic failure"
}

// valueError is a non-comparable value error without a custom Is method. Returning
// it unchanged must still let a later mapping handle the original failure.
type valueError struct {
	// labels carries diagnostic context and makes the whole value non-comparable.
	labels []string
}

// Error supplies the error contract without adding category matching, allowing
// tests to distinguish unchanged-value detection from errors.Is behavior.
//
// Example:
//
//	message := (valueError{labels: []string{"orders"}}).Error()
//	_ = message
func (valueError) Error() string {
	return "context failure"
}

var (
	// errNoRows simulates a storage miss that the mapping middleware translates to
	// an HTTP problem.
	errNoRows = errors.New("no rows")
	// errForbidden simulates a separate domain failure so mapping selection can be
	// checked.
	errForbidden = errors.New("forbidden")
	// errOther remains unmapped, exercising propagation of unrelated errors.
	errOther = errors.New("other")
)

// serve runs a handler returning err through the middleware and returns the
// response and the error the middleware passed on to the router. The response
// body remains usable until test cleanup closes it.
func serve(t *testing.T, err error, mappings ...errormap.Mapping) (*http.Response, error) {
	t.Helper()

	var got error

	handler := errormap.Middleware(mappings...)(func(http.ResponseWriter, *http.Request) error {
		return err
	})

	capture := func(next router.Handler) router.Handler {
		return func(writer http.ResponseWriter, httpRequest *http.Request) error {
			got = next(writer, httpRequest)

			return got
		}
	}

	response := capture(handler).Record(httptest.NewRequest(http.MethodGet, "/", nil))
	t.Cleanup(func() {
		response.Body.Close()
	})

	return response, got
}

// Mappings shared by the middleware tests.
var (
	notFound  = errormap.Map(errNoRows, problem.NotFound())
	forbidden = errormap.Map(errForbidden, problem.Forbidden())
	always    = errormap.Func(func(err error) error {
		return problem.Conflict().Derive(func(details *problem.Builder) {
			details.Error(err)
		})
	})
	never = errormap.Func(func(error) error {
		return nil
	})
)

// checkStatus runs err through the middleware with mappings and fails the test
// unless the response status is want.
func checkStatus(t *testing.T, err error, want int, mappings ...errormap.Mapping) {
	t.Helper()

	response, _ := serve(t, err, mappings...)

	if response.StatusCode != want {
		t.Fatalf("status = %d, want %d", response.StatusCode, want)
	}
}

// TestMiddlewareNilPassesThrough verifies a nil error passes through.
func TestMiddlewareNilPassesThrough(t *testing.T) {
	t.Parallel()

	checkStatus(t, nil, http.StatusNoContent, notFound)
}

// TestMiddlewareMatch verifies a matching error is mapped.
func TestMiddlewareMatch(t *testing.T) {
	t.Parallel()

	checkStatus(t, errNoRows, http.StatusNotFound, notFound)
}

// TestMiddlewareWrappedMatch verifies a wrapped error still matches.
func TestMiddlewareWrappedMatch(t *testing.T) {
	t.Parallel()

	checkStatus(t, fmt.Errorf("load user: %w", errNoRows), http.StatusNotFound, notFound)
}

// TestMiddlewareSecondMappingMatches verifies a later mapping can match.
func TestMiddlewareSecondMappingMatches(t *testing.T) {
	t.Parallel()

	checkStatus(t, errForbidden, http.StatusForbidden, notFound, forbidden)
}

// TestMiddlewareFirstMatchWins verifies the first matching mapping wins.
func TestMiddlewareFirstMatchWins(t *testing.T) {
	t.Parallel()

	checkStatus(t, errNoRows, http.StatusNotFound, notFound, always)
}

// TestMiddlewareFuncDecliningFallsThrough verifies a declining Func falls through to the next mapping.
func TestMiddlewareFuncDecliningFallsThrough(t *testing.T) {
	t.Parallel()

	checkStatus(t, errNoRows, http.StatusNotFound, never, notFound)
}

// TestMiddlewareUnmatchedStays500 verifies an unmatched error stays a 500.
func TestMiddlewareUnmatchedStays500(t *testing.T) {
	t.Parallel()

	checkStatus(t, errOther, http.StatusInternalServerError, notFound, forbidden, never)
}

// TestMiddlewareNoMappings verifies no mappings leaves the error unmapped.
func TestMiddlewareNoMappings(t *testing.T) {
	t.Parallel()

	checkStatus(t, errNoRows, http.StatusInternalServerError)
}

// TestMapPreservesOriginalError verifies the mapped error wraps the cause.
func TestMapPreservesOriginalError(t *testing.T) {
	t.Parallel()

	original := fmt.Errorf("load user 7: %w", errNoRows)
	_, got := serve(t, original, errormap.Map(errNoRows, problem.NotFound()))

	if !errors.Is(got, errNoRows) || !errors.Is(got, problem.NotFound()) {
		t.Fatalf("error = %v, want match for both errNoRows and a 404 problem", got)
	}

	if !errors.Is(got, original) {
		t.Fatalf("error = %v, want original error in chain", got)
	}
}

// TestFuncUnhandledErrorIsReturnedUnchanged verifies identity on no match.
func TestFuncUnhandledErrorIsReturnedUnchanged(t *testing.T) {
	t.Parallel()

	_, got := serve(t, errOther, errormap.Func(func(error) error {
		return nil
	}))

	if got != errOther { //nolint:errorlint // identity is the point of this test.
		t.Fatalf("error = %v, want the original error", got)
	}
}

// TestValidationProblemFunc verifies request.ValidationProblem works as a Func.
func TestValidationProblemFunc(t *testing.T) {
	t.Parallel()

	errs := validate.Errors{}
	errs.Add("/email", validate.Violation{
		Code: "required",
	})

	response, _ := serve(t, errs, errormap.Func(request.ValidationProblem))

	if response.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", response.StatusCode)
	}

	response, _ = serve(t, errOther, errormap.Func(request.ValidationProblem))

	if response.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", response.StatusCode)
	}
}

// checkUnchangedPassesOn runs err through a ValidationProblem Func followed by a
// 404 Map and fails the test unless the response status is want.
func checkUnchangedPassesOn(t *testing.T, err error, want int) {
	t.Helper()

	checkStatus(t, err, want,
		errormap.Func(request.ValidationProblem),
		errormap.Map(errNoRows, problem.NotFound()),
	)
}

// TestUnchangedResultOtherErrorReachesLaterMap verifies a mapping that returns its input unchanged
// does not end the search, whatever its position.
func TestUnchangedResultOtherErrorReachesLaterMap(t *testing.T) {
	t.Parallel()

	checkUnchangedPassesOn(t, errNoRows, http.StatusNotFound)
}

// TestUnchangedResultWrappedErrorReachesLaterMap verifies a mapping that returns its input unchanged
// does not end the search, whatever its position.
func TestUnchangedResultWrappedErrorReachesLaterMap(t *testing.T) {
	t.Parallel()

	checkUnchangedPassesOn(t, fmt.Errorf("load: %w", errNoRows), http.StatusNotFound)
}

// TestUnchangedResultUnmatchedErrorPassesThrough verifies a mapping that returns its input unchanged
// does not end the search, whatever its position.
func TestUnchangedResultUnmatchedErrorPassesThrough(t *testing.T) {
	t.Parallel()

	checkUnchangedPassesOn(t, errOther, http.StatusInternalServerError)
}

// TestUnchangedResultProblemPassesThrough verifies a mapping that returns its input unchanged
// does not end the search, whatever its position.
func TestUnchangedResultProblemPassesThrough(t *testing.T) {
	t.Parallel()

	checkUnchangedPassesOn(t, problem.Forbidden(), http.StatusForbidden)
}

// TestUnchangedNonComparableError verifies that returning a non-comparable
// error unchanged passes it on without panicking.
func TestUnchangedNonComparableError(t *testing.T) {
	t.Parallel()

	errs := validate.Errors{}
	errs.Add("/email", validate.Violation{
		Code: "required",
	})

	identity := errormap.Func(func(err error) error {
		return err
	})

	response, _ := serve(t, errs, identity, errormap.Func(request.ValidationProblem))

	if response.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", response.StatusCode)
	}
}

// TestUnchangedInterfaceContainingSliceReachesLaterMapping verifies that a
// comparable type with non-comparable dynamic data neither panics nor wins.
func TestUnchangedInterfaceContainingSliceReachesLaterMapping(t *testing.T) {
	t.Parallel()

	original := interfaceError{data: []string{"orders"}}
	identity := errormap.Func(func(err error) error {
		return err
	})

	checkStatus(t, original, http.StatusConflict, identity, always)
}

// TestUnchangedValueErrorReachesLaterMapping verifies that a non-comparable
// struct needs no Is method to decline a mapping by returning its input.
func TestUnchangedValueErrorReachesLaterMapping(t *testing.T) {
	t.Parallel()

	original := valueError{labels: []string{"orders"}}
	identity := errormap.Func(func(err error) error {
		return err
	})

	checkStatus(t, original, http.StatusConflict, identity, always)
}

// TestUnchangedProblemReachesLaterMapping verifies that returning the complete
// problem value unchanged does not prevent a later mapping from winning.
func TestUnchangedProblemReachesLaterMapping(t *testing.T) {
	t.Parallel()

	original := problem.Forbidden().Derive(func(details *problem.Builder) {
		details.Detail("Access denied.")
		details.Header("X-Reason", "policy")
		details.Extension("roles", []string{"reader"})
	})

	checkStatus(t, original, http.StatusConflict, errormap.Func(request.ValidationProblem), always)
}

// TestDerivedProblemDetailWins verifies that matching problem categories do not
// discard a replacement with a different occurrence detail or run later maps.
func TestDerivedProblemDetailWins(t *testing.T) {
	t.Parallel()

	original := problem.Forbidden()
	response, got := serve(t, original, errormap.Func(func(error) error {
		return original.Derive(func(details *problem.Builder) {
			details.Detail("Your account cannot access this order.")
		})
	}), always)

	details, ok := errors.AsType[problem.Details](got)

	if !ok {
		t.Fatalf("error = %T, want problem.Details", got)
	}

	if details.Detail() != "Your account cannot access this order." {
		t.Fatalf("detail = %q, want mapped occurrence detail", details.Detail())
	}

	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", response.StatusCode)
	}
}

// TestDerivedProblemHeaderWins verifies that even a header-only derivation is
// handled, despite retaining the original type, status, title, and detail.
func TestDerivedProblemHeaderWins(t *testing.T) {
	t.Parallel()

	original := problem.New(func(details *problem.Builder) {
		details.Status(http.StatusTooManyRequests)
	})

	response, _ := serve(t, original, errormap.Func(func(error) error {
		return original.Derive(func(details *problem.Builder) {
			details.Header("Retry-After", "30")
		})
	}), always)

	if response.Header.Get("Retry-After") != "30" {
		t.Fatalf("Retry-After = %q, want 30", response.Header.Get("Retry-After"))
	}

	if response.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429", response.StatusCode)
	}
}

// TestProblemWrappingSameCategoryWins verifies that adding a cause is a handled
// replacement even when the outer problem matches the input category both ways.
func TestProblemWrappingSameCategoryWins(t *testing.T) {
	t.Parallel()

	original := problem.Forbidden()
	_, got := serve(t, original, errormap.Map(original, original), always)
	details, ok := errors.AsType[problem.Details](got)

	if !ok {
		t.Fatalf("error = %T, want problem.Details", got)
	}

	if details.Status() != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", details.Status())
	}

	if details.Unwrap() == nil {
		t.Fatal("mapped problem discarded its original cause")
	}

	if !errors.Is(details.Unwrap(), original) {
		t.Fatalf("cause = %v, want the original problem category", details.Unwrap())
	}
}

// TestWrapperReplacementWins verifies that a custom mapping may add a standard
// wrapper without being mistaken for the error it now unwraps to.
func TestWrapperReplacementWins(t *testing.T) {
	t.Parallel()

	replacement := fmt.Errorf("load order: %w", errOther)
	_, got := serve(t, errOther, errormap.Func(func(error) error {
		return replacement
	}), always)

	if got.Error() != replacement.Error() {
		t.Fatalf("error = %v, want the replacement wrapper", got)
	}

	if !errors.Is(got, errOther) {
		t.Fatalf("error = %v, want the original cause", got)
	}
}

// TestMiddlewareSnapshotsMappings verifies that publishing a middleware policy
// detaches its ordered mapping list from subsequent edits to the caller's slice.
func TestMiddlewareSnapshotsMappings(t *testing.T) {
	t.Parallel()

	mappings := []errormap.Mapping{notFound}
	middleware := errormap.Middleware(mappings...)
	mappings[0] = always
	handler := middleware(func(http.ResponseWriter, *http.Request) error {
		return errNoRows
	})

	response := handler.Record(httptest.NewRequest(http.MethodGet, "/", nil))
	t.Cleanup(func() {
		response.Body.Close()
	})

	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want the published 404 mapping", response.StatusCode)
	}
}

// TestValueErrorReplacementKeepsStorageIdentity verifies that independently
// allocated diagnostic data is a replacement even when its contents are equal.
func TestValueErrorReplacementKeepsStorageIdentity(t *testing.T) {
	t.Parallel()

	original := valueError{labels: []string{"orders"}}
	replacement := valueError{labels: []string{"orders"}}
	_, got := serve(t, original, errormap.Func(func(error) error {
		return replacement
	}), always)

	mapped, ok := errors.AsType[valueError](got)

	if !ok {
		t.Fatalf("error = %T, want valueError replacement", got)
	}

	if &mapped.labels[0] != &replacement.labels[0] {
		t.Fatal("mapped error did not retain the replacement's backing storage")
	}
}

// TestUnchangedArrayContainingSliceReachesLaterMapping verifies recursive
// equality across an interface, a fixed-size array, and its slice element.
func TestUnchangedArrayContainingSliceReachesLaterMapping(t *testing.T) {
	t.Parallel()

	original := interfaceError{data: [1][]string{{"orders"}}}
	identity := errormap.Func(func(err error) error {
		return err
	})

	checkStatus(t, original, http.StatusConflict, identity, always)
}

// TestUnchangedNilFunctionReachesLaterMapping verifies that nil functions have
// a detectable unchanged value even though their type is non-comparable.
func TestUnchangedNilFunctionReachesLaterMapping(t *testing.T) {
	t.Parallel()

	original := interfaceError{data: (func())(nil)}
	identity := errormap.Func(func(err error) error {
		return err
	})

	checkStatus(t, original, http.StatusConflict, identity, always)
}

// TestFunctionContainingErrorDeclinesWithNil verifies the explicit nil protocol
// remains reliable when function identity cannot be inspected safely.
func TestFunctionContainingErrorDeclinesWithNil(t *testing.T) {
	t.Parallel()

	original := interfaceError{data: func() string {
		return "orders"
	}}

	checkStatus(t, original, http.StatusConflict, never, always)
}

// TestUnchangedPointerWithFunctionReachesLaterMapping verifies that pointer
// identity supports unchanged errors even when their contents cannot be equal.
func TestUnchangedPointerWithFunctionReachesLaterMapping(t *testing.T) {
	t.Parallel()

	original := &interfaceError{data: func() string {
		return "orders"
	}}

	identity := errormap.Func(func(err error) error {
		return err
	})

	checkStatus(t, original, http.StatusConflict, identity, always)
}
