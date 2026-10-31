package queue

import (
	"arkoren.dev/core/internal/errtype"
	"arkoren.dev/core/service/internal/messaging"
)

// telemetryScope is the instrumentation scope of queue telemetry.
const telemetryScope = "arkoren.dev/core/service/queue"

// errorSentinels name the package errors reported as error.type.
var errorSentinels = []errtype.Sentinel{
	{
		Err:  ErrUnknownName,
		Name: "queue.ErrUnknownName",
	},
}

// newInstruments builds the queue instruments for one telemetry.
var newInstruments = messaging.Build(telemetryScope)
