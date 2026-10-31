package memorytelemetry_test

import (
	"testing"

	"arkoren.dev/core/service/telemetry"
)

// TestTraceparentFormat checks the exact W3C wire format.
func TestTraceparentFormat(t *testing.T) {
	t.Parallel()

	const header = "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"

	_, facade := start(t)
	carrier := telemetry.MapCarrier{"traceparent": header, "tracestate": "vendor=opaque"}

	ctx := facade.Extract(t.Context(), carrier)
	remote := telemetry.SpanFromContext(ctx).Context()

	if remote.TraceID.String() != "4bf92f3577b34da6a3ce929d0e0e4736" || remote.SpanID.String() != "00f067aa0ba902b7" ||
		!remote.Sampled || !remote.Remote || remote.TraceState != "vendor=opaque" {
		t.Fatalf("extracted %+v", remote)
	}

	// A child continues the trace, keeps the tracestate, and gets its own span.
	childCtx, child := facade.Tracer("t").Start(ctx, "child")
	defer child.End()

	out := telemetry.MapCarrier{}
	facade.Inject(childCtx, out)

	want := "00-4bf92f3577b34da6a3ce929d0e0e4736-" + child.Context().SpanID.String() + "-01"

	if out["traceparent"] != want || out["tracestate"] != "vendor=opaque" {
		t.Fatalf("injected %v, want traceparent %s and the tracestate", out, want)
	}
}

// TestUnsampledParentIsInherited checks the sampled flag propagation.
func TestUnsampledParentIsInherited(t *testing.T) {
	t.Parallel()

	_, facade := start(t)
	ctx := facade.Extract(t.Context(), telemetry.MapCarrier{
		"traceparent": "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-00",
	})

	_, child := facade.Tracer("t").Start(ctx, "child")
	defer child.End()

	if child.Context().Sampled {
		t.Fatal("child of an unsampled parent is sampled")
	}
}

// TestFutureVersion checks that a higher version with extra fields parses.
func TestFutureVersion(t *testing.T) {
	t.Parallel()

	_, facade := start(t)
	ctx := facade.Extract(t.Context(), telemetry.MapCarrier{
		"traceparent": "01-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01-future",
	})

	if remote := telemetry.SpanFromContext(ctx).Context(); remote.SpanID.String() != "00f067aa0ba902b7" {
		t.Fatalf("extracted %+v", remote)
	}
}

// checkMalformedTraceparent fails the test if strict parsing accepts header as
// a traceparent.
func checkMalformedTraceparent(t *testing.T, header string) {
	t.Helper()

	_, facade := start(t)

	ctx := facade.Extract(t.Context(), telemetry.MapCarrier{"traceparent": header})

	if telemetry.SpanFromContext(ctx).Context().IsValid() {
		t.Fatalf("accepted %q", header)
	}
}

// TestMalformedTraceparentEmpty checks that strict parsing rejects bad input.
func TestMalformedTraceparentEmpty(t *testing.T) {
	t.Parallel()

	checkMalformedTraceparent(t, "")
}

// TestMalformedTraceparentTooShort checks that strict parsing rejects bad input.
func TestMalformedTraceparentTooShort(t *testing.T) {
	t.Parallel()

	checkMalformedTraceparent(t, "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7")
}

// TestMalformedTraceparentVersionFf checks that strict parsing rejects bad input.
func TestMalformedTraceparentVersionFf(t *testing.T) {
	t.Parallel()

	checkMalformedTraceparent(t, "ff-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01")
}

// TestMalformedTraceparentUppercase checks that strict parsing rejects bad input.
func TestMalformedTraceparentUppercase(t *testing.T) {
	t.Parallel()

	checkMalformedTraceparent(t, "00-4BF92F3577B34DA6A3CE929D0E0E4736-00F067AA0BA902B7-01")
}

// TestMalformedTraceparentZeroTrace checks that strict parsing rejects bad input.
func TestMalformedTraceparentZeroTrace(t *testing.T) {
	t.Parallel()

	checkMalformedTraceparent(t, "00-00000000000000000000000000000000-00f067aa0ba902b7-01")
}

// TestMalformedTraceparentZeroSpan checks that strict parsing rejects bad input.
func TestMalformedTraceparentZeroSpan(t *testing.T) {
	t.Parallel()

	checkMalformedTraceparent(t, "00-4bf92f3577b34da6a3ce929d0e0e4736-0000000000000000-01")
}

// TestMalformedTraceparentBadSeparator checks that strict parsing rejects bad input.
func TestMalformedTraceparentBadSeparator(t *testing.T) {
	t.Parallel()

	checkMalformedTraceparent(t, "00_4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01")
}

// TestMalformedTraceparentNonHex checks that strict parsing rejects bad input.
func TestMalformedTraceparentNonHex(t *testing.T) {
	t.Parallel()

	checkMalformedTraceparent(t, "00-4bf92f3577b34da6a3ce929d0e0e47zz-00f067aa0ba902b7-01")
}

// TestMalformedTraceparentBadFlags checks that strict parsing rejects bad input.
func TestMalformedTraceparentBadFlags(t *testing.T) {
	t.Parallel()

	checkMalformedTraceparent(t, "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-0g")
}

// TestMalformedTraceparentV00ExtraField checks that strict parsing rejects bad input.
func TestMalformedTraceparentV00ExtraField(t *testing.T) {
	t.Parallel()

	checkMalformedTraceparent(t, "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01-extra")
}

// TestMalformedTraceparentV01NoDash checks that strict parsing rejects bad input.
func TestMalformedTraceparentV01NoDash(t *testing.T) {
	t.Parallel()

	checkMalformedTraceparent(t, "01-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01x")
}

// TestMalformedTraceparentMissingDashEnd checks that strict parsing rejects bad input.
func TestMalformedTraceparentMissingDashEnd(t *testing.T) {
	t.Parallel()

	checkMalformedTraceparent(t, "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b701-")
}
