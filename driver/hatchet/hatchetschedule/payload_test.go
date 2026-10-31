package hatchetschedule

import (
	"context"
	"encoding/json/v2"
	"testing"
	"time"

	"arkoren.dev/core/service/schedule"
)

// deferredPayload simulates the SDK/server float64 number conversions after
// Defer and before handle. The v2 decoder's default generic-map numbers are
// float64, reproducing the precision loss in Hatchet v0.112.8's server and
// worker. It does not simulate every v1 JSON behavior or run engine code.
// Two generic-map round trips prevent a raw outbound value from concealing
// corruption on later worker reserialization.
func deferredPayload(t *testing.T, payload string) string {
	t.Helper()

	driver, _, schedules := newTestDriver(t)

	if _, err := driver.Defer(t.Context(), schedule.Job{
		Name:    "precision",
		Payload: []byte(payload),
		At:      time.Unix(2000000000, 0),
	}); err != nil {
		t.Fatalf("Defer() error = %v", err)
	}

	input := schedules.created[0].Input

	for range 2 {
		data, err := json.Marshal(input)

		if err != nil {
			t.Fatal(err)
		}

		input = nil

		if err := json.Unmarshal(data, &input); err != nil {
			t.Fatal(err)
		}
	}

	data, err := json.Marshal(input)

	if err != nil {
		t.Fatal(err)
	}

	var task taskInput

	if err := json.Unmarshal(data, &task); err != nil {
		t.Fatal(err)
	}

	var received string
	driver.dispatch = func(_ context.Context, name string, payload []byte) error {
		if name != "precision" {
			t.Errorf("name = %q, want precision", name)
		}

		received = string(payload)

		return nil
	}

	if _, err := driver.handle(t.Context(), task); err != nil {
		t.Fatalf("handle() error = %v", err)
	}

	return received
}

// TestGenericMapRoundTripUsesBinary64Numbers independently verifies the
// simulation's numeric seam. A nested legacy number must become float64 and
// serialize with the rounded digits; otherwise the preservation regressions
// could stop exercising SEC-014 without detecting a change in JSON defaults.
func TestGenericMapRoundTripUsesBinary64Numbers(t *testing.T) {
	t.Parallel()

	var input map[string]any

	if err := json.Unmarshal([]byte(`{"payload":{"id":9007199254740993}}`), &input); err != nil {
		t.Fatal(err)
	}

	payload, ok := input["payload"].(map[string]any)

	if !ok {
		t.Fatalf("payload type = %T, want map[string]any", input["payload"])
	}

	id, ok := payload["id"].(float64)

	if !ok || id != 9007199254740992 {
		t.Fatalf("id = %v (%T), want float64(9007199254740992)", payload["id"], payload["id"])
	}

	data, err := json.Marshal(input)

	if err != nil {
		t.Fatal(err)
	}

	if string(data) != `{"payload":{"id":9007199254740992}}` {
		t.Fatalf("round trip = %s, want rounded legacy ID", data)
	}
}

// TestDeferredTaskPreservesInt64AboveBinary64Precision reproduces SEC-014:
// the next integer after 2^53 must not silently select its neighboring ID.
func TestDeferredTaskPreservesInt64AboveBinary64Precision(t *testing.T) {
	t.Parallel()

	if got := deferredPayload(t, `{"id":9007199254740993}`); got != `{"id":9007199254740993}` {
		t.Fatalf("payload = %s, want {\"id\":9007199254740993}", got)
	}
}

// TestDeferredTaskPreservesInt64BelowBinary64Precision keeps the last
// consecutively representable integer before 2^53 unchanged.
func TestDeferredTaskPreservesInt64BelowBinary64Precision(t *testing.T) {
	t.Parallel()

	if got := deferredPayload(t, `9007199254740991`); got != `9007199254740991` {
		t.Fatalf("payload = %s", got)
	}
}

// TestDeferredTaskPreservesInt64AtBinary64Precision includes 2^53 itself;
// this exactly representable value alone would not expose SEC-014.
func TestDeferredTaskPreservesInt64AtBinary64Precision(t *testing.T) {
	t.Parallel()

	if got := deferredPayload(t, `9007199254740992`); got != `9007199254740992` {
		t.Fatalf("payload = %s", got)
	}
}

// TestDeferredTaskPreservesNegativePrecisionBoundaries verifies both exact
// negative neighbors and the first negative integer binary64 cannot retain.
func TestDeferredTaskPreservesNegativePrecisionBoundaries(t *testing.T) {
	t.Parallel()

	for _, payload := range []string{`-9007199254740991`, `-9007199254740992`, `-9007199254740993`} {
		if got := deferredPayload(t, payload); got != payload {
			t.Fatalf("payload = %s, want %s", got, payload)
		}
	}
}

// TestDeferredTaskPreservesInt64Maximum prevents rounding the maximum valid
// signed integer into an out-of-range value before typed dispatch.
func TestDeferredTaskPreservesInt64Maximum(t *testing.T) {
	t.Parallel()

	if got := deferredPayload(t, `9223372036854775807`); got != `9223372036854775807` {
		t.Fatalf("payload = %s", got)
	}
}

// TestDeferredTaskPreservesInt64Minimum checks the negative signed boundary
// without relying on its coincidental exact binary64 representation.
func TestDeferredTaskPreservesInt64Minimum(t *testing.T) {
	t.Parallel()

	if got := deferredPayload(t, `-9223372036854775808`); got != `-9223372036854775808` {
		t.Fatalf("payload = %s", got)
	}
}

// TestDeferredTaskPreservesUint64Maximum keeps all decimal digits of the
// unsigned boundary, which cannot pass losslessly through float64.
func TestDeferredTaskPreservesUint64Maximum(t *testing.T) {
	t.Parallel()

	if got := deferredPayload(t, `18446744073709551615`); got != `18446744073709551615` {
		t.Fatalf("payload = %s", got)
	}
}

// TestDeferredTaskPreservesSnowflakeID covers a realistic non-boundary ID,
// preventing a fix restricted to integer limits or powers of two.
func TestDeferredTaskPreservesSnowflakeID(t *testing.T) {
	t.Parallel()

	if got := deferredPayload(t, `1541815603606036481`); got != `1541815603606036481` {
		t.Fatalf("payload = %s", got)
	}
}

// TestDeferredTaskPreservesJSONValues checks that the opaque transport is
// general: objects, arrays, null, booleans, strings, and whitespace survive.
func TestDeferredTaskPreservesJSONValues(t *testing.T) {
	t.Parallel()

	for _, payload := range []string{
		` {"nested":[{"id":9007199254740993},[null,true,"line\n\"quote\"",18446744073709551615]]} `,
		`[1,null,false,"text"]`, `null`, `true`, `"{\"id\":9007199254740993}"`, `""`, `42`,
	} {
		if got := deferredPayload(t, payload); got != payload {
			t.Fatalf("payload = %q, want %q", got, payload)
		}
	}
}

// TestHandleLegacyPayloadsRemainBusinessJSON keeps pending-job compatibility
// explicit: a legacy string containing JSON is still a string, not an encoded
// envelope. Small numbers and already-rounded legacy data pass through as-is;
// the decoder cannot reconstruct digits lost before worker delivery.
func TestHandleLegacyPayloadsRemainBusinessJSON(t *testing.T) {
	t.Parallel()

	for _, payload := range []string{
		`"{\"id\":9007199254740993}"`, `"null"`, `""`,
		`{"id":42}`, `{"id":9007199254740992}`, `[null,true,"text"]`, `null`,
	} {
		driver, _, _ := newTestDriver(t)
		var input taskInput

		if err := json.Unmarshal([]byte(`{"name":"legacy","payload":`+payload+`}`), &input); err != nil {
			t.Fatal(err)
		}

		var received string
		driver.dispatch = func(_ context.Context, name string, data []byte) error {
			if name != "legacy" {
				t.Errorf("name = %q, want legacy", name)
			}

			received = string(data)

			return nil
		}

		if _, err := driver.handle(t.Context(), input); err != nil || received != payload {
			t.Fatalf("handle() = %q, %v; want %q", received, err, payload)
		}
	}
}

// TestHandleRejectsInvalidPayloadEncodings ensures malformed and ambiguous
// envelopes never invoke application code. Raw fields let null remain visible
// instead of being confused with an absent new-format field.
func TestHandleRejectsInvalidPayloadEncodings(t *testing.T) {
	t.Parallel()

	for _, wire := range []string{
		`{"payload_json_v1":null}`, `{"payload_json_v1":42}`,
		`{"payload_json_v1":{}}`, `{"payload_json_v1":[]}`, `{"payload_json_v1":true}`,
		`{"payload_json_v1":""}`, `{"payload_json_v1":"{"}`, `{"payload_json_v1":" "}`,
		`{"payload_json_v1":"{\"id\":1,\"id\":2}"}`,
		`{"payload":null,"payload_json_v1":"null"}`,
		`{"payload":"data","payload_json_v1":"null"}`,
	} {
		driver, _, _ := newTestDriver(t)
		var input taskInput

		if err := json.Unmarshal([]byte(wire), &input); err != nil {
			t.Fatal(err)
		}

		driver.dispatch = func(context.Context, string, []byte) error {
			t.Error("invalid payload reached dispatch")

			return nil
		}

		if _, err := driver.handle(t.Context(), input); err == nil {
			t.Fatalf("handle(%s) succeeded", wire)
		}
	}
}

// TestHandleRecurringNameOnly preserves the cron invocation contract:
// a name-only SDK input dispatches nil payload and returns the same name.
func TestHandleRecurringNameOnly(t *testing.T) {
	t.Parallel()

	driver, _, _ := newTestDriver(t)
	var input taskInput

	if err := json.Unmarshal([]byte(`{"name":"recurring"}`), &input); err != nil {
		t.Fatal(err)
	}

	called := false
	driver.dispatch = func(_ context.Context, name string, payload []byte) error {
		called = true

		if name != "recurring" || payload != nil {
			t.Errorf("dispatch = %q, %q; want recurring, nil", name, payload)
		}

		return nil
	}

	output, err := driver.handle(t.Context(), input)

	if err != nil || !called || output.Name != "recurring" {
		t.Fatalf("handle() = %+v, %v; called = %v", output, err, called)
	}
}

// TestDeferRejectsInvalidPayloadJSON prevents opaque encoding from hiding
// invalid business JSON that the old raw-JSON marshaling path rejected.
func TestDeferRejectsInvalidPayloadJSON(t *testing.T) {
	t.Parallel()

	for _, payload := range []string{`{`, ` `, `{"id":1,"id":2}`, "\xff"} {
		driver, _, schedules := newTestDriver(t)

		if _, err := driver.Defer(t.Context(), schedule.Job{
			Name:    "invalid",
			Payload: []byte(payload),
		}); err == nil {
			t.Fatalf("Defer(%q) succeeded", payload)
		}

		if len(schedules.created) != 0 {
			t.Fatal("invalid JSON submitted to Hatchet")
		}
	}
}

// TestDeferEmptyPayloadRemainsAbsent preserves raw Driver callers that submit
// no payload. Neither the legacy nor the opaque field is manufactured for them.
func TestDeferEmptyPayloadRemainsAbsent(t *testing.T) {
	t.Parallel()

	driver, _, schedules := newTestDriver(t)

	if _, err := driver.Defer(t.Context(), schedule.Job{
		Name: "empty",
	}); err != nil {
		t.Fatal(err)
	}

	if input := schedules.created[0].Input; len(input) != 1 || input["name"] != "empty" {
		t.Fatalf("input = %v, want name only", input)
	}
}
