package telemetry_test

import (
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"testing"

	"arkoren.dev/core/service/telemetry"
	"arkoren.dev/core/service/telemetry/memorytelemetry"
)

// TestEnumJSONNames pins the boundary representation without coupling it to
// any native driver's numeric SDK constants.
func TestEnumJSONNames(t *testing.T) {
	t.Parallel()

	data, err := json.Marshal([]any{
		telemetry.AttrInvalid, telemetry.AttrString, telemetry.AttrInt64,
		telemetry.AttrFloat64, telemetry.AttrBool, telemetry.AttrStrings,
		telemetry.CounterKind, telemetry.UpDownCounterKind,
		telemetry.HistogramKind, telemetry.GaugeKind,
		telemetry.Internal, telemetry.Server, telemetry.Client,
		telemetry.Producer, telemetry.Consumer,
		telemetry.Unset, telemetry.OK, telemetry.Error,
	})

	if err != nil {
		t.Fatal(err)
	}

	want := `["","string","int64","float64","bool","strings","counter","up_down_counter","histogram","gauge","internal","server","client","producer","consumer","unset","ok","error"]`

	if string(data) != want {
		t.Fatalf("JSON = %s, want %s", data, want)
	}
}

// TestZeroEnumSentinels preserves invalid attribute and instrument zero values;
// neither becomes an accidental concrete kind during the string migration.
func TestZeroEnumSentinels(t *testing.T) {
	t.Parallel()

	if attr := (telemetry.Attr{}); attr.Valid() || attr.Kind() != telemetry.AttrInvalid {
		t.Fatal("zero attribute must remain invalid")
	}

	var kind telemetry.InstrumentKind

	if kind != "" {
		t.Fatalf("zero instrument kind = %q, want empty", kind)
	}
}

// ExampleSpanRecorder exercises the complete raw protocol, including the
// required actual-parent accessor, with an in-process backend and fixed parent.
func ExampleSpanRecorder() {
	ctx := context.Background()
	driver := memorytelemetry.New()

	if err := driver.Start(ctx, telemetry.Setup{}); err != nil {
		fmt.Println(err)

		return
	}

	parent := telemetry.SpanContext{
		TraceID: telemetry.TraceID{1}, SpanID: telemetry.SpanID{2}, Remote: true,
	}
	ctx = telemetry.ContextWithSpanContext(ctx, parent)
	_, recorder := driver.StartSpan(ctx, telemetry.SpanStart{Name: "cache get", Kind: telemetry.Client})
	fmt.Println(recorder.Context().IsValid(), recorder.Parent() == parent)

	if recorder.Recording() {
		recorder.SetAttributes([]telemetry.Attr{telemetry.String("region", "eu")})
		recorder.AddEvent("retry", []telemetry.Attr{telemetry.Int("attempt", 2)})
		recorder.RecordError(errors.New("backend unavailable"))
		recorder.SetStatus(telemetry.Error, "operation failed")
	}

	recorder.End()

	if err := driver.Stop(ctx); err != nil {
		fmt.Println(err)

		return
	}

	span := driver.Spans()[0]
	fmt.Println(span.Parent == parent, span.Status, len(span.Events))
	// Output:
	// true true
	// true error 2
}
