package internal_test

import (
	"net/http/httptest"
	"slices"
	"testing"

	"arkoren.dev/foundation/problem/internal"
)

// TestOrderWithoutAcceptUsesSupportedOrder verifies the documented problem behavior.
func TestOrderWithoutAcceptUsesSupportedOrder(t *testing.T) {
	t.Parallel()

	request := httptest.NewRequest("GET", "/", nil)
	supported := []string{"application/problem+json", "application/json", "text/plain"}

	actual := internal.ParseAccept(request).Order(supported)

	if !slices.Equal(actual, supported) {
		t.Fatalf("expected supported order %#v, got %#v", supported, actual)
	}
}

// TestOrderWithoutValidAcceptUsesSupportedOrder verifies the documented problem behavior.
func TestOrderWithoutValidAcceptUsesSupportedOrder(t *testing.T) {
	t.Parallel()

	request := httptest.NewRequest("GET", "/", nil)
	request.Header.Set("Accept", "invalid media range")
	supported := []string{"application/problem+json", "application/json", "text/plain"}

	actual := internal.ParseAccept(request).Order(supported)

	if !slices.Equal(actual, supported) {
		t.Fatalf("expected supported order %#v, got %#v", supported, actual)
	}
}

// TestOrderExactExclusionOverridesFullWildcard verifies the documented problem behavior.
func TestOrderExactExclusionOverridesFullWildcard(t *testing.T) {
	t.Parallel()

	request := httptest.NewRequest("GET", "/", nil)
	request.Header.Set("Accept", "application/json;q=0, */*;q=1")

	actual := internal.ParseAccept(request).Order([]string{
		"application/problem+json", "application/json", "text/plain",
	})
	expected := []string{"application/problem+json", "text/plain"}

	if !slices.Equal(actual, expected) {
		t.Fatalf("expected order %#v, got %#v", expected, actual)
	}
}

// TestOrderExactRangeOutranksTypeWildcard verifies the documented problem behavior.
func TestOrderExactRangeOutranksTypeWildcard(t *testing.T) {
	t.Parallel()

	request := httptest.NewRequest("GET", "/", nil)
	request.Header.Set("Accept", "application/*;q=1, application/problem+json;q=0.1")

	actual := internal.ParseAccept(request).Order([]string{
		"application/problem+json", "application/json", "text/plain",
	})
	expected := []string{"application/json", "application/problem+json"}

	if !slices.Equal(actual, expected) {
		t.Fatalf("expected order %#v, got %#v", expected, actual)
	}
}

// TestOrderUsesHighestQualityAtSameSpecificity verifies the documented problem behavior.
func TestOrderUsesHighestQualityAtSameSpecificity(t *testing.T) {
	t.Parallel()

	request := httptest.NewRequest("GET", "/", nil)
	request.Header.Set("Accept", "application/json;q=0.2, application/json;q=0.8")

	actual := internal.ParseAccept(request).Order([]string{
		"application/problem+json", "application/json", "text/plain",
	})
	expected := []string{"application/json"}

	if !slices.Equal(actual, expected) {
		t.Fatalf("expected order %#v, got %#v", expected, actual)
	}
}

// TestOrderEqualRangesUseHeaderOrder verifies the documented problem behavior.
func TestOrderEqualRangesUseHeaderOrder(t *testing.T) {
	t.Parallel()

	request := httptest.NewRequest("GET", "/", nil)
	request.Header.Set("Accept", "application/json;q=0.5, application/problem+json;q=0.5, application/json;q=0.5")

	actual := internal.ParseAccept(request).Order([]string{
		"application/problem+json", "application/json", "text/plain",
	})
	expected := []string{"application/json", "application/problem+json"}

	if !slices.Equal(actual, expected) {
		t.Fatalf("expected order %#v, got %#v", expected, actual)
	}
}

// TestOrderFullWildcardUsesServerOrder verifies the documented problem behavior.
func TestOrderFullWildcardUsesServerOrder(t *testing.T) {
	t.Parallel()

	request := httptest.NewRequest("GET", "/", nil)
	request.Header.Set("Accept", "*/*;q=0.5")
	supported := []string{"application/problem+json", "application/json", "text/plain"}

	actual := internal.ParseAccept(request).Order(supported)

	if !slices.Equal(actual, supported) {
		t.Fatalf("expected supported order %#v, got %#v", supported, actual)
	}
}

// TestOrderUnsupportedValidMediaReturnsNoRepresentation verifies the documented problem behavior.
func TestOrderUnsupportedValidMediaReturnsNoRepresentation(t *testing.T) {
	t.Parallel()

	request := httptest.NewRequest("GET", "/", nil)
	request.Header.Set("Accept", "text/html")

	actual := internal.ParseAccept(request).Order([]string{
		"application/problem+json", "application/json", "text/plain",
	})

	if len(actual) != 0 {
		t.Fatalf("expected no representation, got %#v", actual)
	}
}
