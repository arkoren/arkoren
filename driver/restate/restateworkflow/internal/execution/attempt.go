// Package execution owns Restate attempts and suspension-safe runtime state.
package execution

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"

	restatesdk "github.com/restatedev/sdk-go"

	"arkoren.dev/core/service/workflow"
	"arkoren.dev/driver/restate/restateworkflow/internal/protocol"
)

// runOptions maps step options to Restate Run options. Restate counts the
// first execution as attempt one, like [workflow.StepOptions.Attempts].
// All backoff fields are explicit: omission would select native defaults, not
// the core meanings of immediate initial retry and no maximum interval. The
// largest Go duration represents the same saturation bound as Backoff.
func runOptions(name string, policy workflow.StepOptions) []restatesdk.RunOption {
	maximum := policy.Max

	if maximum == 0 {
		maximum = time.Duration(math.MaxInt64)
	}

	return []restatesdk.RunOption{
		restatesdk.WithName(name),
		restatesdk.WithMaxRetryAttempts(uint(max(policy.Attempts, 1))),
		restatesdk.WithInitialRetryInterval(max(policy.Initial, 0)),
		restatesdk.WithRetryIntervalFactor(float32(max(policy.Factor, 1))),
		restatesdk.WithMaxRetryInterval(maximum),
	}
}

// attempt runs one execution of a step. A failure becomes an error whose
// message is the [workflow.EncodeError] envelope. Restate keeps the message
// of the last failure when the attempts run out, so the kind survives. A
// [workflow.Permanent] failure is terminal for Restate too.
// Timeout is cooperative: the handler must return before its result can be
// proposed. Once its context expires, a late success is not accepted. This
// function never starts an untracked goroutine to escape an arbitrary handler.
func attempt(runCtx context.Context, handler workflow.StepHandler, in []byte, policy workflow.StepOptions) (protocol.Payload, error) {
	ctx := runCtx

	if policy.Timeout > 0 {
		var cancel context.CancelFunc

		ctx, cancel = context.WithTimeout(runCtx, policy.Timeout)
		defer cancel()
	}

	out, err := handler.Handle(ctx, in)

	if ctx.Err() != nil {
		err = errors.Join(ctx.Err(), err)
	}

	if errors.Is(ctx.Err(), context.DeadlineExceeded) && runCtx.Err() == nil {
		err = fmt.Errorf("%w: step %s exceeded %s: %w", workflow.ErrTimeout, handler.Name, policy.Timeout, err)
	}

	if err == nil {
		return out, nil
	}

	envelope := errors.New(string(workflow.EncodeError(err)))

	if errors.Is(err, workflow.ErrPermanent) {
		return nil, restatesdk.ToTerminalError(envelope)
	}

	return nil, envelope
}
