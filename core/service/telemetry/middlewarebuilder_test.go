package telemetry

import (
	"net/http/httptest"
	"net/netip"
	"testing"
)

// TestMiddlewareCallbacksRunOnceInOrder checks the public constructor, identical
// draft receivers, and replacement order without executing request-time hooks.
func TestMiddlewareCallbacksRunOnceInOrder(t *testing.T) {
	t.Parallel()

	var retained *MiddlewareBuilder
	order := 0
	Middleware(func(middleware *MiddlewareBuilder) {
		retained = middleware
		order++

		if middleware.TraceResponse(false) != middleware {
			t.Fatal("setting returned another draft")
		}
	}, func(middleware *MiddlewareBuilder) {
		if order != 1 || middleware != retained {
			t.Fatal("callbacks were reordered or used different drafts")
		}

		order++
	})

	if order != 2 {
		t.Fatalf("callback count = %d, want 2", order)
	}
}

// TestMiddlewareOmissionAndEmptyExpansions supply zero callbacks. A supplied nil
// is instead misuse and must panic before a later callback can execute.
func TestMiddlewareOmissionAndEmptyExpansions(t *testing.T) {
	t.Parallel()

	var omitted []func(*MiddlewareBuilder)
	Middleware(omitted...)
	Middleware([]func(*MiddlewareBuilder){}...)

	reached := false
	defer func() {
		if recover() == nil || reached {
			t.Fatal("nil entry was filtered or later callback ran")
		}
	}()
	Middleware(nil, func(middleware *MiddlewareBuilder) {
		reached = true
	})
}

// TestMiddlewareApplicationPanicPropagates keeps callback misuse separate from
// ordinary constructor validation and prevents later policies from executing.
func TestMiddlewareApplicationPanicPropagates(t *testing.T) {
	t.Parallel()

	reached := false
	defer func() {
		if value := recover(); value != "construction sentinel" || reached {
			t.Fatalf("panic = %v, later callback = %v", value, reached)
		}
	}()
	Middleware(func(middleware *MiddlewareBuilder) {
		panic("construction sentinel")
	}, func(middleware *MiddlewareBuilder) {
		reached = true
	})
}

// TestMiddlewareInstanceCallbacksRunOnce checks the instance path independently
// from package defaults; configuring an inactive instance performs no I/O.
func TestMiddlewareInstanceCallbacksRunOnce(t *testing.T) {
	t.Parallel()

	instance := New(nil)
	calls := 0
	instance.Middleware(func(middleware *MiddlewareBuilder) {
		calls++
		middleware.TraceResponse(false)
	})

	if calls != 1 {
		t.Fatalf("callback count = %d, want 1", calls)
	}
}

// TestMiddlewareBuilderPublicationDetachesRoot checks completed settings do not retain the
// mutable construction root. Services and function captures remain shared.
func TestMiddlewareBuilderPublicationDetachesRoot(t *testing.T) {
	t.Parallel()

	var retained *MiddlewareBuilder
	config := newMiddlewareSettings(func(middleware *MiddlewareBuilder) {
		retained = middleware
		middleware.TraceHeader("X-Trace-ID")
	})
	retained.TraceHeader("Changed")

	if config.TraceHeader != "X-Trace-ID" {
		t.Fatal("retained draft changed published settings")
	}
}

// TestTrustedNetworksCopiesInputs fixes the inbound trust policy independently
// of a retained draft and input slice, without trusting forwarded client headers.
func TestTrustedNetworksCopiesInputs(t *testing.T) {
	t.Parallel()

	prefixes := []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")}
	var retained *MiddlewareBuilder
	config := newMiddlewareSettings(func(middleware *MiddlewareBuilder) {
		retained = middleware
		middleware.TrustedNetworks(prefixes...)

		prefixes[0] = netip.MustParsePrefix("192.0.2.0/24")
	})
	retained.TrustRemote(TrustAll)

	request := httptest.NewRequest("GET", "/", nil)
	request.RemoteAddr = "10.1.2.3:80"

	if !config.TrustRemote(request) {
		t.Fatal("published policy lost the captured prefix")
	}

	request.RemoteAddr = "192.0.2.1:80"
	request.Header.Set("X-Forwarded-For", "10.1.2.3")

	if config.TrustRemote(request) {
		t.Fatal("caller edits, root edits or forwarded headers changed trust")
	}
}
