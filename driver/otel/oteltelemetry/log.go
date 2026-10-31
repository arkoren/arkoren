package oteltelemetry

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"strconv"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/log"
)

// slogToSeverityOffset maps a slog level onto the OpenTelemetry severity
// scale: Debug (-4) becomes 5, Info (0) 9, Warn (4) 13, and Error (8) 17.
const slogToSeverityOffset = 9

// Enabled reports whether the logger would accept a record at level. It is
// false while the driver is inactive or has no log exporter.
//
// With a started driver and operation context:
//
//	if driver.Enabled(ctx, slog.LevelInfo) {
//		err := driver.Export(ctx, slog.NewRecord(time.Unix(1, 0), slog.LevelInfo, "completed", 0))
//		if err != nil {
//			return err
//		}
//	}
func (driver *Driver) Enabled(ctx context.Context, level slog.Level) bool {
	run := driver.state.Load()

	if run == nil || run.Logger == nil {
		return false
	}

	return run.Logger.Enabled(nativeContext(ctx), log.EnabledParameters{
		Severity: severityOf(level),
	})
}

// Export converts record to an OpenTelemetry log record and emits it. The span
// of ctx correlates the record with its trace. Groups are flattened into
// dotted attribute keys. Export preserves explicit message and attribute text,
// including error values; it is not a redaction boundary. Keep sensitive data out
// of supplied records. Native emission is asynchronous and returns nil here;
// exporter failures reach the configured SDK error handler instead.
//
//	record := slog.NewRecord(time.Unix(1, 0), slog.LevelInfo, "completed", 0)
//	if err := driver.Export(ctx, record); err != nil {
//		return err
//	}
func (driver *Driver) Export(ctx context.Context, record slog.Record) error {
	run := driver.state.Load()

	if run == nil || run.Logger == nil {
		return nil
	}

	var emitted log.Record

	if !record.Time.IsZero() {
		emitted.SetTimestamp(record.Time)
	}

	emitted.SetObservedTimestamp(time.Now())
	emitted.SetSeverity(severityOf(record.Level))
	emitted.SetSeverityText(record.Level.String())
	emitted.SetBody(attribute.StringValue(record.Message))

	record.Attrs(func(attr slog.Attr) bool {
		emitted.AddAttributes(logAttrs("", attr)...)

		return true
	})

	run.Logger.Emit(nativeContext(ctx), emitted)

	return nil
}

// severityOf maps a slog level onto an OpenTelemetry severity, clamped to the
// defined range.
func severityOf(level slog.Level) log.Severity {
	return min(max(log.Severity(level+slogToSeverityOffset), log.SeverityTrace1), log.SeverityFatal4)
}

// logAttrs converts attr, whose key is prefixed with prefix, to OpenTelemetry
// log attributes. Groups expand to their members with dotted keys; empty
// groups produce nothing.
func logAttrs(prefix string, attr slog.Attr) []attribute.KeyValue {
	value := attr.Value.Resolve()

	if value.Kind() != slog.KindGroup {
		if attr.Equal(slog.Attr{}) {
			return nil
		}

		return []attribute.KeyValue{{
			Key:   attribute.Key(joinKey(prefix, attr.Key)),
			Value: logValue(value),
		}}
	}

	groupPrefix := joinKey(prefix, attr.Key)

	var members []attribute.KeyValue

	for _, member := range value.Group() {
		members = append(members, logAttrs(groupPrefix, member)...)
	}

	return members
}

// joinKey joins a group prefix and a key with a dot, omitting empty parts.
func joinKey(prefix, key string) string {
	if prefix == "" {
		return key
	}

	if key == "" {
		return prefix
	}

	return prefix + "." + key
}

// logValue converts a resolved, non-group slog value.
func logValue(value slog.Value) attribute.Value {
	switch value.Kind() {
	case slog.KindString:
		return attribute.StringValue(value.String())
	case slog.KindInt64:
		return attribute.Int64Value(value.Int64())
	case slog.KindUint64:
		return uint64LogValue(value.Uint64())
	case slog.KindFloat64:
		return attribute.Float64Value(value.Float64())
	case slog.KindBool:
		return attribute.BoolValue(value.Bool())
	case slog.KindDuration:
		return attribute.Int64Value(value.Duration().Nanoseconds())
	case slog.KindTime:
		return attribute.Int64Value(value.Time().UnixNano())
	default:
		return anyLogValue(value.Any())
	}
}

// uint64LogValue keeps an unsigned value as an integer when it fits and as a
// decimal string when it would overflow int64.
func uint64LogValue(value uint64) attribute.Value {
	if value > math.MaxInt64 {
		return attribute.StringValue(strconv.FormatUint(value, 10))
	}

	return attribute.Int64Value(int64(value))
}

// anyLogValue converts an arbitrary value: bytes stay bytes, errors become
// their message, and everything else its %+v form.
func anyLogValue(value any) attribute.Value {
	switch typed := value.(type) {
	case []byte:
		return attribute.ByteSliceValue(typed)
	case error:
		return attribute.StringValue(typed.Error())
	default:
		return attribute.StringValue(fmt.Sprintf("%+v", typed))
	}
}
