// Package capture copies and normalizes telemetry snapshots before storage.
package capture

import (
	"log/slog"

	"arkoren.dev/core/service/telemetry"
)

// AppendLogAttr appends attr under its dotted key, descending into groups.
// It resolves caller callbacks before storage locks are acquired. Empty keys
// and groups are dropped; unsupported scalar types retain their text form.
func AppendLogAttr(attrs []telemetry.Attr, prefix string, attr slog.Attr) []telemetry.Attr {
	value := attr.Value.Resolve()

	if value.Kind() == slog.KindGroup {
		if attr.Key != "" {
			prefix += attr.Key + "."
		}

		for _, member := range value.Group() {
			attrs = AppendLogAttr(attrs, prefix, member)
		}

		return attrs
	}

	key := prefix + attr.Key

	if attr.Key == "" {
		return attrs
	}

	switch value.Kind() {
	case slog.KindInt64:
		return append(attrs, telemetry.Int64(key, value.Int64()))
	case slog.KindFloat64:
		return append(attrs, telemetry.Float64(key, value.Float64()))
	case slog.KindBool:
		return append(attrs, telemetry.Bool(key, value.Bool()))
	default:
		return append(attrs, telemetry.String(key, value.String()))
	}
}
