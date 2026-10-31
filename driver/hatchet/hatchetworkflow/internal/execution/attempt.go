package execution

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/hatchet-dev/hatchet/pkg/client" //nolint:staticcheck // public durable hooks expose this legacy listener type
	hatchet "github.com/hatchet-dev/hatchet/sdks/go"

	"arkoren.dev/core/service/workflow"
	"arkoren.dev/driver/hatchet/hatchetworkflow/internal/protocol"
)

// RequireDurableMemo rejects the SDK's process-local fallback at the native
// workflow boundary, before clocks, identifiers, or application code run.
// The SDK can disable these hooks after an independent version probe fails,
// even when Host.Start already verified an adequate engine version.
func RequireDurableMemo(ctx hatchet.DurableContext) error {
	hooks, ok := ctx.(interface {
		// DurableTaskListener supplies the engine-backed memo transport.
		DurableTaskListener() *client.DurableTaskListener
		// DurableEvictionSupported is the capability captured by this worker.
		DurableEvictionSupported() bool
	})

	if !ok {
		return fmt.Errorf("native durable memo hooks unavailable: %w", workflow.ErrUnsupported)
	}

	if hooks.DurableTaskListener() == nil || !hooks.DurableEvictionSupported() {
		return fmt.Errorf("native durable memo transport unavailable: %w", workflow.ErrUnsupported)
	}

	return nil
}

// Attempt returns the task function of a step. It enforces the timeout
// itself and tells Hatchet when to stop retrying.
func Attempt(registry *workflow.Registry, handler workflow.StepHandler) func(hatchet.Context, protocol.Wire) (protocol.Wire, error) {
	return func(ctx hatchet.Context, in protocol.Wire) (protocol.Wire, error) {
		options := stepOptions(handler.Options, in)
		callCtx := context.Context(ctx)

		if options.Timeout > 0 {
			var cancel context.CancelFunc

			callCtx, cancel = context.WithTimeout(ctx, options.Timeout)
			defer cancel()
		}

		callCtx = registry.NewTraceContext(callCtx, in.Trace)

		out, err := handler.Handle(callCtx, []byte(in.Data))

		if ctx.Err() == nil && errors.Is(callCtx.Err(), context.DeadlineExceeded) && !errors.Is(err, workflow.ErrPermanent) {
			err = fmt.Errorf("step %s: %w", handler.Name, workflow.ErrTimeout)
		}

		if err == nil {
			return protocol.Wire{
				Data: string(out),
			}, nil
		}

		failed := protocol.EncodeFailure(err)

		if errors.Is(err, workflow.ErrPermanent) || ctx.RetryCount()+1 >= options.Attempts {
			return protocol.Wire{}, hatchet.NewNonRetryableError(failed)
		}

		return protocol.Wire{}, hatchet.NewRetryAfterError(options.Backoff(ctx.RetryCount()+1), failed)
	}
}

// stepOptions applies per-call limits without expanding the registered native
// retry or timeout policy. Zero timeout/attempt wire values retain registration
// defaults; RetryWithoutDelay explicitly overrides positive initial backoff.
// Missing flags retain old-input behavior. RetryAfter sends the completed policy
// to supporting engines without creating durable wait nodes.
func stepOptions(registered workflow.StepOptions, in protocol.Wire) workflow.StepOptions {
	if in.RetryWithoutDelay {
		registered.Initial = 0
	}

	if in.Timeout > 0 {
		timeout := time.Duration(in.Timeout)

		if registered.Timeout > 0 {
			timeout = min(timeout, registered.Timeout)
		}

		registered.Timeout = timeout
	}

	registered.Attempts = max(registered.Attempts, 1)

	if in.Attempts > 0 {
		registered.Attempts = min(registered.Attempts, in.Attempts)
	}

	return registered
}
