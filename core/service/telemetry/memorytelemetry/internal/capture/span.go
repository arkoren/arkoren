package capture

import (
	"slices"

	"arkoren.dev/core/service/telemetry"
)

// CloneSpan detaches every owned span and event container. Attribute values
// remain immutable telemetry values; identity and parent metadata are copied
// unchanged from the backend-selected snapshot.
func CloneSpan(span telemetry.FinishedSpan) telemetry.FinishedSpan {
	span.Attrs = slices.Clone(span.Attrs)
	span.Events = slices.Clone(span.Events)

	for index := range span.Events {
		span.Events[index].Attrs = slices.Clone(span.Events[index].Attrs)
	}

	return span
}
