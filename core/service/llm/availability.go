package llm

import (
	"context"
	"fmt"
)

// Available evaluates capability policy without decoding or parsing arguments.
// Invalid definitions and canceled contexts fail before policy execution. With
// no policy it returns true. Policy errors wrap ErrToolAvailability and retain
// their cause, even when wrapping ToolError. Cancellation is checked again after
// a successful callback. Panics propagate and results are never cached; shared
// captures must be concurrency-safe. ctx must not be nil.
//
// Example:
//
//	offered, err := lookupOrder.Available(ctx)
//	if err != nil {
//		return err
//	}
//	if offered {
//		fmt.Println(lookupOrder.Name())
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
		return false, fmt.Errorf("%w: %w", ErrToolAvailability, err)
	}

	return offered, ctx.Err()
}
