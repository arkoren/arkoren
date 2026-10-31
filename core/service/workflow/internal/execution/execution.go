// Package execution protects registered workflow and step invocations.
// Native replay-control panics remain under the runtime's authority.
package execution

import (
	"context"
	"fmt"
	"log/slog"
	"runtime/debug"
)

// Repanicker returns the optional native runtime control-flow hook.
func Repanicker(runtime any) func(any) bool {
	if hook, ok := runtime.(interface{ Repanic(v any) bool }); ok {
		return hook.Repanic
	}

	return nil
}

// Protect must be deferred directly. It logs the stack but does not persist it.
// Accepted control-flow panics propagate unchanged; ordinary panics become
// errors, optionally marked permanent by the owning public package.
func Protect(
	ctx context.Context,
	logger *slog.Logger,
	repanic func(any) bool,
	permanent func(error) error,
	err *error,
) {
	recovered := recover()

	if recovered == nil {
		return
	}

	if repanic != nil && repanic(recovered) {
		panic(recovered)
	}

	logger.ErrorContext(ctx, "panic recovered", slog.Any("panic", recovered), slog.String("stack", string(debug.Stack())))

	if cause, ok := recovered.(error); ok {
		*err = fmt.Errorf("panic: %w", cause)
	} else {
		*err = fmt.Errorf("panic: %v", recovered)
	}

	if permanent != nil {
		*err = permanent(*err)
	}
}
