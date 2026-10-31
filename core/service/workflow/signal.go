package workflow

import (
	"fmt"
	"time"

	"arkoren.dev/core/internal/name"
)

// Signal names an external message of type T that a running workflow waits
// for. Create it with [NewSignal]; send it with [Client.Signal].
// Definitions bind a shared name to the same payload type on both sides of the
// wire; using the same name with incompatible types is an application error.
//
//	approved := workflow.NewSignal[bool]("approved")
//	if err := client.Signal(ctx, Onboard, "user-42", approved, true); err != nil {
//		return err
//	}
type Signal[T any] struct {
	// name is the stable identifier of the signal.
	name string
}

// NewSignal returns the signal called identity that carries values of type T. The
// name must satisfy the shared identifier rule of 1-64 lowercase letters,
// digits, or hyphens with no hyphen at either end; NewSignal panics with
// [ErrInvalidName] otherwise.
// The constructor does no I/O; use a shared definition for senders and waiters.
//
//	approved := workflow.NewSignal[bool]("approved")
//	value, err := workflowContext.Wait(approved, workflow.Timeout(time.Minute))
//	if err != nil {
//		return "", err
//	}
//	_ = value
func NewSignal[T any](identity string) Signal[T] {
	if err := name.Identifier(identity); err != nil {
		panic(fmt.Errorf("workflow: NewSignal %q: %w: %w", identity, ErrInvalidName, err))
	}

	return Signal[T]{
		name: identity,
	}
}

// Name returns the name of the signal.
// The name is the stable identity used by both senders and receivers.
//
// Example:
//
//	confirmed := workflow.NewSignal[string]("confirmed")
//	name := confirmed.Name()
func (signal Signal[T]) Name() string {
	return signal.name
}

// Case is one alternative of [Context.Select]. Create it with [On] or
// [After].
// Case callbacks execute synchronously on the workflow's orchestration path
// and must follow the same replay/determinism restrictions as its body.
//
//	approved := workflow.NewSignal[bool]("approved")
//	var accepted bool
//	choice := workflow.On(approved, func(value bool) {
//		accepted = value
//	})
//	if err := workflowContext.Select(choice); err != nil {
//		return "", err
//	}
//	_ = accepted
type Case struct {
	// signal is the signal name, or empty for a timer case.
	signal string

	// after is the delay of a timer case.
	after time.Duration

	// fire decodes the raw signal value, if any, and calls the case function.
	fire func(c *Context, raw []byte) error
}

// On returns a case that calls fn with the value of signal s. fn must not be
// nil; On panics with [ErrNilFunc] otherwise.
// Decoding errors stop Select before fn is invoked. The first remembered value
// for the signal is reused by later waits and selections in the same run.
//
//	approved := workflow.NewSignal[bool]("approved")
//	var accepted bool
//	choice := workflow.On(approved, func(value bool) {
//		accepted = value
//	})
//	if err := workflowContext.Select(choice); err != nil {
//		return "", err
//	}
//	_ = accepted
func On[T any](s Signal[T], fn func(T)) Case {
	if fn == nil {
		panic(fmt.Errorf("workflow: On %q: %w", s.name, ErrNilFunc))
	}

	return Case{
		signal: s.name,
		fire: func(c *Context, raw []byte) error {
			var value T

			if err := DecodeValue(c.codec, raw, &value); err != nil {
				return fmt.Errorf("signal %s: %w", s.name, err)
			}

			fn(value)

			return nil
		},
	}
}

// After returns a case that calls fn once d has passed. fn must not be nil;
// After panics with [ErrNilFunc] otherwise.
// A nonpositive delay becomes an immediately eligible timer alternative;
// the engine still arbitrates it against available signals.
//
//	var expired bool
//	if err := workflowContext.Select(workflow.After(time.Minute, func() {
//		expired = true
//	})); err != nil {
//		return "", err
//	}
//	_ = expired
func After(d time.Duration, fn func()) Case {
	if fn == nil {
		panic(fmt.Errorf("workflow: After: %w", ErrNilFunc))
	}

	return Case{
		after: d,
		fire: func(*Context, []byte) error {
			fn()

			return nil
		},
	}
}

// WaitOption changes one setting of [Context.Wait].
// It affects only this wait and cannot change the workflow's MaxDuration.
//
//	var option workflow.WaitOption = workflow.Timeout(time.Minute)
//	value, err := workflowContext.Wait(Approved, option)
//	if err != nil {
//		return "", err
//	}
//	_ = value
type WaitOption func(*waitConfig)

// waitConfig holds the settings of one wait.
type waitConfig struct {
	// timeout bounds one wait; zero leaves it limited only by the run lifetime.
	timeout time.Duration
}

// Timeout limits [Context.Wait]. When it expires Wait returns [ErrTimeout].
// Without it Wait blocks until the signal arrives or the run ends.
// Zero or a negative duration disables the per-wait timeout; neither disables
// cancellation or the workflow's MaxDuration. An already remembered signal
// value returns immediately without scheduling another timer.
//
//	value, err := workflowContext.Wait(Approved, workflow.Timeout(time.Minute))
//	if errors.Is(err, workflow.ErrTimeout) {
//		return "expired", nil
//	}
//	if err != nil {
//		return "", err
//	}
//	_ = value
func Timeout(d time.Duration) WaitOption {
	return func(config *waitConfig) {
		config.timeout = d
	}
}
