package telemetry

import (
	"maps"
	"net/http"
	"slices"
	"strings"
)

// Carrier is a string-keyed text container that propagation headers are
// written to and read from: HTTP headers, message metadata, or workflow
// metadata.
// A nil underlying map can be read, but callers must allocate it before Set.
//
//	var carrier telemetry.Carrier = telemetry.MapCarrier{}
//	carrier.Set("baggage", "region=eu")
//	fmt.Println(carrier.Get("baggage"))
type Carrier interface {
	// Get returns the value for key, or "" when absent.
	//
	//	var carrier telemetry.Carrier = telemetry.MapCarrier{"baggage": "region=eu"}
	//	fmt.Println(carrier.Get("missing")) // Empty string.
	Get(key string) string

	// Set stores value for key, replacing any existing value.
	//
	//	var carrier telemetry.Carrier = telemetry.MapCarrier{}
	//	carrier.Set("baggage", "region=eu")
	//	fmt.Println(carrier.Get("baggage"))
	Set(key, value string)

	// Keys returns the keys present in the carrier in any order.
	//
	//	var carrier telemetry.Carrier = telemetry.MapCarrier{"baggage": "region=eu"}
	//	keys := carrier.Keys()
	//	slices.Sort(keys)
	//	fmt.Println(keys)
	Keys() []string
}

// HeaderCarrier adapts [http.Header] to a [Carrier]. Keys are canonicalised
// as HTTP header names are.
//
//	telemetry.Default().Inject(ctx, telemetry.HeaderCarrier(request.Header))
type HeaderCarrier http.Header

// Get returns the first value for key, or "".
// Header lookup uses net/http's case-insensitive canonicalization.
//
//	carrier := telemetry.HeaderCarrier(http.Header{})
//	carrier.Set("Baggage", "region=eu")
//	fmt.Println(carrier.Get("baggage"))
func (carrier HeaderCarrier) Get(key string) string {
	return http.Header(carrier).Get(key)
}

// Set replaces the values for key with value. The underlying header must not
// be nil.
//
//	carrier := telemetry.HeaderCarrier(http.Header{})
//	carrier.Set("baggage", "region=eu")
//	fmt.Println(carrier.Get("Baggage"))
func (carrier HeaderCarrier) Set(key, value string) {
	http.Header(carrier).Set(key, value)
}

// Keys returns the header names present.
// The returned slice is independent and its order is unspecified.
//
//	carrier := telemetry.HeaderCarrier(http.Header{"Baggage": {"region=eu"}})
//	keys := carrier.Keys()
//	slices.Sort(keys)
//	fmt.Println(keys)
func (carrier HeaderCarrier) Keys() []string {
	return slices.Collect(maps.Keys(carrier))
}

// MapCarrier adapts a map to a [Carrier], for message and workflow metadata.
// Keys are stored as given; Get also matches keys that differ only in case,
// because brokers may change the case of header names.
//
//	metadata := telemetry.MapCarrier{}
//	telemetry.Default().Inject(ctx, metadata)
type MapCarrier map[string]string

// Get returns the value for key, preferring an exact match over a
// case-insensitive one, or "" when absent.
//
//	carrier := telemetry.MapCarrier{"Baggage": "region=eu"}
//	fmt.Println(carrier.Get("baggage")) // region=eu
func (carrier MapCarrier) Get(key string) string {
	if value, ok := carrier[key]; ok {
		return value
	}

	for name, value := range carrier {
		if strings.EqualFold(name, key) {
			return value
		}
	}

	return ""
}

// Set stores value for key. The map must not be nil.
// It preserves key spelling; Get still supports a case-insensitive fallback.
//
//	carrier := telemetry.MapCarrier{}
//	carrier.Set("baggage", "region=eu")
//	fmt.Println(carrier.Get("Baggage"))
func (carrier MapCarrier) Set(key, value string) {
	carrier[key] = value
}

// Keys returns the keys present.
// Mutating the returned slice does not change the map; sort it for stable output.
//
//	carrier := telemetry.MapCarrier{"baggage": "region=eu"}
//	keys := carrier.Keys()
//	slices.Sort(keys)
//	fmt.Println(keys)
func (carrier MapCarrier) Keys() []string {
	return slices.Collect(maps.Keys(carrier))
}
