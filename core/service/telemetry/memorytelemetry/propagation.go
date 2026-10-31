package memorytelemetry

import (
	"encoding/hex"
	"strings"

	"arkoren.dev/core/service/telemetry"
)

const (
	// traceparentKey is the W3C Trace Context header carrying the identity.
	traceparentKey = "traceparent"

	// tracestateKey is the W3C Trace Context header carrying vendor state.
	tracestateKey = "tracestate"

	// traceparentLength is the length of the known traceparent fields:
	// "00-" + 32 + "-" + 16 + "-" + 2.
	traceparentLength = 55

	// sampledFlag is the traceparent flag bit for sampled traces.
	// W3C traceparent defines this numeric bit mask; it is not an enum ordinal.
	sampledFlag = 0x01
)

// formatTraceparent renders spanContext as a version 00 traceparent value.
func formatTraceparent(spanContext telemetry.SpanContext) string {
	flags := byte(0)

	if spanContext.Sampled {
		flags = sampledFlag
	}

	return "00-" + spanContext.TraceID.String() + "-" + spanContext.SpanID.String() + "-" +
		hex.EncodeToString([]byte{flags})
}

// parseTraceparent parses a traceparent value. Version 00 must have exactly
// the known fields; a higher version may append fields after a dash, which
// are ignored as the W3C specification requires. It reports false for
// malformed values, all-zero IDs, and version ff.
func parseTraceparent(value string) (telemetry.SpanContext, bool) {
	var spanContext telemetry.SpanContext

	if !validEnvelope(value) {
		return spanContext, false
	}

	if !decodeLowerHex(spanContext.TraceID[:], value[3:35]) ||
		!decodeLowerHex(spanContext.SpanID[:], value[36:52]) {
		return spanContext, false
	}

	var flags [1]byte

	if !decodeLowerHex(flags[:], value[53:traceparentLength]) || !spanContext.IsValid() {
		return telemetry.SpanContext{}, false
	}

	spanContext.Sampled = flags[0]&sampledFlag != 0

	return spanContext, true
}

// validEnvelope checks the version, the length, and the dash positions of a
// traceparent value.
func validEnvelope(value string) bool {
	if len(value) < traceparentLength || value[2] != '-' || value[35] != '-' || value[52] != '-' {
		return false
	}

	var version [1]byte

	if !decodeLowerHex(version[:], value[:2]) || version[0] == 0xff {
		return false
	}

	if version[0] == 0 {
		return len(value) == traceparentLength
	}

	return len(value) == traceparentLength || value[traceparentLength] == '-'
}

// decodeLowerHex decodes lowercase hexadecimal text into dst, which must have
// exactly half the length of text. W3C forbids uppercase.
func decodeLowerHex(dst []byte, text string) bool {
	if text != strings.ToLower(text) {
		return false
	}

	_, err := hex.Decode(dst, []byte(text))

	return err == nil
}
