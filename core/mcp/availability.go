package mcp

import (
	"cmp"
	"context"
	"errors"
	"fmt"
)

// WithAvailability returns a guarded copy without evaluating either policy.
// Both the existing policy and policy must permit each request, in that order;
// denial short-circuits. Nil policy records an invalid declaration. Captures
// remain shared and must be concurrency-safe; the original tool is unchanged.
//
// Example:
//
//	guarded := tool.WithAvailability(func(ctx context.Context) (bool, error) {
//		return permissions.Allowed(ctx)
//	})
func (tool Tool) WithAvailability(policy func(context.Context) (bool, error)) Tool {
	if policy == nil {
		if tool.err == nil {
			tool.err = errors.New("availability policy is nil")
		}

		return tool
	}

	previous := tool
	tool.available = func(ctx context.Context) (bool, error) {
		offered, err := previous.Available(ctx)

		if err != nil || !offered {
			return false, err
		}

		return policy(ctx)
	}

	return tool
}

// Available evaluates the request's capability policy without parsing input or
// invoking the handler. No policy means true. Invalid declarations and canceled
// contexts fail before policy execution. Policy errors preserve their cause and
// wrap ErrAvailability, even when that cause is a client-safe ToolError.
// Results are never cached; captures are shared and must be concurrency-safe.
// This does not replace resource authorization in the handler. Panics propagate.
//
// Example:
//
//	offered, err := tool.Available(ctx)
//	if err != nil {
//		return err
//	}
//	if offered {
//		fmt.Println(tool.Name())
//	}
func (tool Tool) Available(ctx context.Context) (bool, error) {
	if err := tool.Err(); err != nil {
		return false, err
	}

	if err := ctx.Err(); err != nil {
		return false, err
	}

	if tool.available == nil {
		return true, nil
	}

	offered, err := tool.available(ctx)

	if err != nil {
		return false, fmt.Errorf("%w: %w", ErrAvailability, err)
	}

	return offered, ctx.Err()
}

// Available replaces the request-time capability policy. Omitting it offers the
// tool to everyone; explicitly passing nil records a sticky declaration error.
// The callback must be observational and concurrency-safe. It may run for both
// listing and calling with the same context, and its captures remain shared.
// Use it for roles or plans, not argument-dependent resource authorization:
// handlers must still authorize the resources they access. Panics propagate in
// core and are recovered by protocol adapters, like handler panics.
//
// Example:
//
//	action.Available(func(ctx context.Context) (bool, error) {
//		return plans.CanExport(ctx)
//	})
func (action *ToolBuilder[In, Out]) Available(policy func(context.Context) (bool, error)) *ToolBuilder[In, Out] {
	if policy == nil {
		action.err = cmp.Or(action.err, errors.New("availability policy is nil"))
	}

	action.available = policy

	return action
}
