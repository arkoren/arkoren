package telemetry_test

import (
	"fmt"
	"net/http"
	"slices"
	"testing"

	"arkoren.dev/core/service/telemetry"
)

// TestMapCarrier checks Get, Set, Keys, and case-insensitive lookup.
func TestMapCarrier(t *testing.T) {
	t.Parallel()

	carrier := telemetry.MapCarrier{}
	carrier.Set("traceparent", "value")

	if got := carrier.Get("traceparent"); got != "value" {
		t.Errorf("Get = %q", got)
	}

	if got := carrier.Get("TraceParent"); got != "value" {
		t.Errorf("case-insensitive Get = %q", got)
	}

	if got := carrier.Get("missing"); got != "" {
		t.Errorf("Get(missing) = %q", got)
	}

	if !slices.Equal(carrier.Keys(), []string{"traceparent"}) {
		t.Errorf("Keys = %v", carrier.Keys())
	}

	carrier.Set("Traceparent", "other")

	if got := carrier.Get("Traceparent"); got != "other" {
		t.Errorf("exact match must win, got %q", got)
	}
}

// TestHeaderCarrier checks that HeaderCarrier canonicalises names.
func TestHeaderCarrier(t *testing.T) {
	t.Parallel()

	header := http.Header{}
	carrier := telemetry.HeaderCarrier(header)
	carrier.Set("traceparent", "value")

	if got := header.Get("Traceparent"); got != "value" {
		t.Errorf("header = %q", got)
	}

	if got := carrier.Get("TRACEPARENT"); got != "value" {
		t.Errorf("Get = %q", got)
	}

	if !slices.Equal(carrier.Keys(), []string{"Traceparent"}) {
		t.Errorf("Keys = %v", carrier.Keys())
	}
}

// ExampleCarrier demonstrates both public carrier adapters without network
// access. Sorting keys avoids relying on map iteration order.
func ExampleCarrier() {
	var carrier telemetry.Carrier = telemetry.MapCarrier{}
	carrier.Set("baggage", "region=eu")
	fmt.Println(carrier.Get("Baggage"))

	keys := carrier.Keys()
	slices.Sort(keys)
	fmt.Println(keys)

	headers := telemetry.HeaderCarrier(http.Header{})
	headers.Set("baggage", carrier.Get("baggage"))
	fmt.Println(headers.Get("Baggage"))
	// Output:
	// region=eu
	// [baggage]
	// region=eu
}
