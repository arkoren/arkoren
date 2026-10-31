package execution

import (
	"cmp"
	"context"
	"errors"
	"time"
)

// Delivery is the settlement capability of an already admitted delivery.
type Delivery interface {
	// Acknowledge requests successful completion.
	Acknowledge(context.Context) error
	// Retry requests another delivery after a lower-bound delay.
	Retry(context.Context, time.Duration) error
	// Reject requests terminal rejection.
	Reject(context.Context) error
	// Fail requests terminal failure handling.
	Fail(context.Context, error) error
}

// Classification binds public error identities without a parent-package cycle.
type Classification struct {
	// Unknown constructs the unknown-name failure after settlement starts.
	Unknown func(string) error
	// Decode extracts an unrecoverable payload decoding failure.
	Decode func(error) (error, bool)
	// Rejected recognizes an explicit terminal rejection.
	Rejected func(error) bool
	// Permanent recognizes an explicit permanent failure.
	Permanent func(error) bool
	// RetryAfter extracts an explicit delay instead of policy backoff.
	RetryAfter func(error) (time.Duration, bool)
}

// Notifications publishes only confirmed settlement outcomes, except TimedOut
// which reports the expired attempt before selecting its settlement.
type Notifications struct {
	// RetryAttempts snapshots the delivery before cap and delay selection.
	RetryAttempts func() int
	// Failed runs after successful terminal failure settlement.
	Failed func(context.Context, error)
	// Rejected runs after successful rejection settlement.
	Rejected func(context.Context, error)
	// Retried runs after the transport accepts a retry.
	Retried func(context.Context, time.Duration, error)
	// TimedOut reports an expired execution deadline.
	TimedOut func(context.Context)
}

const (
	// settlementBudget starts only after decoding and execution finish.
	settlementBudget = 30 * time.Second
	// unknownDelay permits a rolling deployment to register the missing handler.
	unknownDelay = 30 * time.Second
	// unknownAttempts is used when no unknown-name limit was configured.
	unknownAttempts = 10
)

// Process returns the logical execution error separately from settlement error.
// Shutdown retries bypass only the immediate cap; reported attempts stay intact.
func Process(
	ctx context.Context,
	delivery Delivery,
	name string,
	payload []byte,
	attempts int,
	known bool,
	unknownLimit int,
	handler Handler,
	classify Classification,
	notify Notifications,
) (error, error) {
	if !known {
		settlement, cancel := context.WithTimeout(context.WithoutCancel(ctx), settlementBudget)
		defer cancel()
		err := classify.Unknown(name)
		limit := cmp.Or(unknownLimit, unknownAttempts)

		if limit > 0 && attempts >= limit {
			return err, fail(settlement, delivery, err, notify)
		}

		return err, delivery.Retry(settlement, unknownDelay)
	}

	err, timedOut := Attempt(ctx, handler, payload)
	settlement, cancel := context.WithTimeout(context.WithoutCancel(ctx), settlementBudget)
	defer cancel()

	if err == nil {
		return nil, delivery.Acknowledge(settlement)
	}

	if failure, ok := classify.Decode(err); ok {
		return err, fail(settlement, delivery, failure, notify)
	}

	if ctx.Err() != nil && errors.Is(err, ctx.Err()) {
		return err, delivery.Retry(settlement, 0)
	}

	if timedOut {
		notify.TimedOut(settlement)
	}

	return err, settle(settlement, delivery, handler, err, classify, notify)
}

// fail publishes a terminal notification only after acceptance by the driver.
func fail(ctx context.Context, delivery Delivery, cause error, notify Notifications) error {
	if err := delivery.Fail(ctx, cause); err != nil {
		return err
	}

	notify.Failed(ctx, cause)

	return nil
}

// settle maps explicit outcomes before applying the ordinary attempt cap.
func settle(ctx context.Context, delivery Delivery, handler Handler, cause error, classify Classification, notify Notifications) error {
	if classify.Rejected(cause) {
		if err := delivery.Reject(ctx); err != nil {
			return err
		}

		notify.Rejected(ctx, cause)

		return nil
	}

	if classify.Permanent(cause) {
		return fail(ctx, delivery, cause, notify)
	}

	attempts := notify.RetryAttempts()

	if handler.Attempts > 0 && attempts >= handler.Attempts {
		return fail(ctx, delivery, cause, notify)
	}

	var delay time.Duration

	if requested, ok := classify.RetryAfter(cause); ok {
		delay = max(requested, 0)
	} else if handler.Backoff != nil {
		attempt := attempts + 1

		if attempt == 1 {
			attempt = 2
		}

		delay = max(handler.Backoff(attempt), 0)
	}

	if err := delivery.Retry(ctx, delay); err != nil {
		return err
	}

	notify.Retried(ctx, delay, cause)

	return nil
}
