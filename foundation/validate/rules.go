package validate

import (
	"context"
	"slices"
)

// oneOfRule builds the OneOf (allow) and NotOneOf rules.
func oneOfRule[T comparable](values []T, allow bool) func(context.Context, T) error {
	values = slices.Clone(values)

	code := "not_one_of"

	if allow {
		code = "one_of"
	}

	listed := make([]any, len(values))

	for index, value := range values {
		listed[index] = value
	}

	return func(_ context.Context, v T) error {
		if slices.Contains(values, v) == allow {
			return nil
		}

		return Fail(code, map[string]any{"values": listed})
	}
}
