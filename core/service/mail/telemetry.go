package mail

import (
	"arkoren.dev/core/internal/clientop"
	"arkoren.dev/core/internal/errtype"
	"arkoren.dev/core/internal/semconv"
)

// telemetryScope is the instrumentation scope of mail telemetry.
const telemetryScope = "arkoren.dev/core/service/mail"

// operationSend is the only mail operation, reported as arkoren.mail.operation.
const operationSend = "send"

// operationNames describes the mail instruments. Sending talks to a relay or
// a provider API, so the buckets span ten milliseconds to a minute.
var operationNames = clientop.Names{
	Scope:    telemetryScope,
	Noun:     "mail operations",
	Count:    semconv.MailOperations,
	Duration: semconv.MailOperationDuration,
	Keys:     []string{semconv.MailOperation},
	Buckets:  []float64{0.01, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60},
}

// errorSentinels name the package errors reported as error.type.
var errorSentinels = []errtype.Sentinel{
	{
		Err:  ErrInvalidMessage,
		Name: "mail.ErrInvalidMessage",
	},
}
