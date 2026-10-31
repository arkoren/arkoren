// Package push provides portable outbound push submission, not delivery.
// A Sender validates an immutable Message, resolves retention policy, and calls
// one Driver exactly once. Applications own registration storage, authorization,
// retries, reconciliation, and cleanup of registrations confirmed invalid.
//
// Construct messages with a value Subscription or Token and a nonempty title:
//
//	message := push.NewMessage(push.Token("device-registration"), "Order ready",
//		func(notification *push.MessageBuilder) {
//			notification.Body("Your order is ready.").Data(map[string]string{"order": "42"})
//		})
//	if err := message.Err(); err != nil {
//		return err
//	}
//	receipt, err := push.New(driver).Send(ctx, message, push.TTL(time.Hour))
//	if err != nil {
//		return err
//	}
//	_ = receipt.ID // Optional provider acceptance ID, not a delivery confirmation.
//
// Native tokens are interpreted only by the configured driver; this package
// does not route between providers. Subscription validation is structural and
// performs no I/O. Drivers must separately protect endpoint requests against
// SSRF, including redirects, private DNS results, and application allowlists.
// Driver-specific payload limits and reserved metadata keys belong in drivers.
//
// DefaultTTL is applied only by Sender. Raw SendConfig.TTL is already resolved:
// zero means no storage and drivers never apply a default. No-store does not
// prevent immediate delivery. Positive retention is not a delivery guarantee.
//
// Sender and published messages are safe for concurrent use with a conforming
// Driver. Construction drafts are not. Message containers are copied on input,
// publication, and inspection; callbacks and live services are not cloned.
//
// Each instrumented Send emits a "push send" client span and the
// arkoren.push.operations and arkoren.push.operation.duration metrics. Only
// bounded operation and error-class labels are recorded. Messages, tokens,
// endpoints, keys, metadata, receipts, and error text are never recorded. Nil
// telemetry follows telemetry.Default on every call; disabled telemetry is inert.
// ErrOutcomeUnknown takes precedence over joined cancellation or provider causes
// in telemetry classification. Send preserves all joined causes for errors.Is;
// uncertain acceptance is never converted into proof of failed delivery.
//
// Builder.Events opts into best-effort in-process Accepted and Failed submission
// notifications; nil disables them. Preflight failures emit none. Payloads hold
// only duration and bounded error classes, never message or recipient data.
// outcome_unknown means acceptance may have occurred, not failed delivery.
// Notifications are not durable and must not drive delivery guarantees.
package push
