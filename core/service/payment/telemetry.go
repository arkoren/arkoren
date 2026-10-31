package payment

import (
	"arkoren.dev/core/internal/clientop"
	"arkoren.dev/core/internal/errtype"
	"arkoren.dev/core/internal/semconv"
)

// operationNames defines shared client-operation instruments. The bounds cover
// provider network requests, from ten milliseconds to one minute.
var operationNames = clientop.Names{
	Scope:    "arkoren.dev/core/service/payment",
	Noun:     "payment operations",
	Count:    semconv.PaymentOperations,
	Duration: semconv.PaymentOperationDuration,
	Keys:     []string{semconv.PaymentOperation},
	Buckets:  []float64{0.01, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60},
}

// errorSentinels classify errors by stable contract identity, never provider
// messages, which can contain secrets and high-cardinality resource data.
var errorSentinels = []errtype.Sentinel{
	{Err: ErrInvalidRequest, Name: "payment.ErrInvalidRequest"},
	{Err: ErrInvalidState, Name: "payment.ErrInvalidState"},
	{Err: ErrNotFound, Name: "payment.ErrNotFound"},
	{Err: ErrUnsupported, Name: "payment.ErrUnsupported"},
	{Err: ErrUnauthorized, Name: "payment.ErrUnauthorized"},
	{Err: ErrForbidden, Name: "payment.ErrForbidden"},
	{Err: ErrDeclined, Name: "payment.ErrDeclined"},
	{Err: ErrRateLimited, Name: "payment.ErrRateLimited"},
	{Err: ErrUnavailable, Name: "payment.ErrUnavailable"},
	{Err: ErrOutcomeUnknown, Name: "payment.ErrOutcomeUnknown"},
}
