package sdk

import (
	"arkoren.dev/core/service/telemetry"

	"go.opentelemetry.io/otel/attribute"
)

// Attrs converts attrs to OpenTelemetry attributes, skipping invalid ones.
func Attrs(attrs []telemetry.Attr) []attribute.KeyValue {
	converted := make([]attribute.KeyValue, 0, len(attrs))

	for _, attr := range attrs {
		if value, ok := Attr(attr); ok {
			converted = append(converted, value)
		}
	}

	return converted
}

// Attr converts one attribute and reports false for an invalid one.
func Attr(attr telemetry.Attr) (attribute.KeyValue, bool) {
	key := attribute.Key(attr.Key())

	switch attr.Kind() {
	case telemetry.AttrString:
		return key.String(attr.AsString()), true
	case telemetry.AttrInt64:
		return key.Int64(attr.AsInt64()), true
	case telemetry.AttrFloat64:
		return key.Float64(attr.AsFloat64()), true
	case telemetry.AttrBool:
		return key.Bool(attr.AsBool()), true
	case telemetry.AttrStrings:
		return key.StringSlice(attr.AsStrings()), true
	default:
		return attribute.KeyValue{}, false
	}
}
