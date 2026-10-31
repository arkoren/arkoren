package engine

import (
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"

	"arkoren.dev/foundation/validate"
)

// Executor binds typed parsing, invocation, and result encoding after
// declaration validation. The caller keeps it behind an availability guard, so
// schema parsing and handler side effects cannot precede capability admission.
// Schema implementations, handlers, and captures remain shared; their panics
// propagate. Only explicit validation violations become model-safe ToolErrors.
func Executor[In, Out any](
	toolName string,
	schema validate.Schema[In],
	fn func(context.Context, In) (Out, error),
	feedback func(string) error,
) func(context.Context, []byte) (string, error) {
	return func(ctx context.Context, arguments []byte) (string, error) {
		if len(arguments) == 0 {
			arguments = []byte("{}")
		}

		var in In

		if err := json.Unmarshal(arguments, &in); err != nil {
			return "", feedback("invalid arguments: " + err.Error())
		}

		in, err := schema.Parse(ctx, in)

		var violations validate.Errors

		if errors.As(err, &violations) {
			return "", feedback(violations.Error())
		}

		if err != nil {
			return "", err
		}

		out, err := fn(ctx, in)

		if err != nil {
			return "", err
		}

		if text, ok := any(out).(string); ok {
			return text, nil
		}

		encoded, err := json.Marshal(out)

		if err != nil {
			return "", fmt.Errorf("llm: encode result of tool %q: %w", toolName, err)
		}

		return string(encoded), nil
	}
}
