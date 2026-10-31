package feature

import (
	"errors"
	"fmt"
)

// ErrInvalidFlag identifies a missing flag or namespace name, including zero
// definitions and flags descended from an invalid namespace.
// Names are provider-owned literal identifiers; they are not normalized.
//
// Example: invalid := errors.Is(feature.NewFlag("", false).Err(), feature.ErrInvalidFlag)
var ErrInvalidFlag = errors.New("feature flag invalid definition")

// Flag binds a provider name, Go value type, and fallback in one immutable
// definition. Share it between evaluation and typed memory setup. It does not
// register a flag or enforce an external provider's schema. The zero value is
// invalid; construct definitions with NewFlag.
//
// Example:
//
//	checkout := feature.NewFlag("new-checkout", false)
//	on, err := flags.Value(ctx, checkout, subject)
//	if err != nil {
//		log.Print(err) // on is checkout.Fallback().
//	}
type Flag[T Primitive] struct {
	// name is the exact provider key, retained without normalization.
	name string

	// fallback is returned when evaluation fails or supplies no value.
	fallback T

	// err preserves the first namespace error even if the joined name is nonempty.
	err error
}

// NewFlag publishes an immutable definition without I/O or registration.
// An empty name is invalid; Err reports it at construction or use. All
// Primitive types, including named types, retain their exact Go identity.
//
// Example: checkout := feature.NewFlag("new-checkout", false)
func NewFlag[T Primitive](name string, fallback T) Flag[T] {
	return Flag[T]{name: name, fallback: fallback}
}

// Name returns the literal provider key without changing it. A zero Flag
// returns an empty name and must not be used for evaluation or setup.
//
// Example: name := feature.NewFlag("new-checkout", false).Name()
func (flag Flag[T]) Name() string {
	return flag.name
}

// Fallback returns the definition's typed fallback. Evaluation returns this
// value on error, when disabled, or when no value applies to the subject.
//
// Example: fallback := feature.NewFlag("upload-limit", 25).Fallback()
func (flag Flag[T]) Fallback() T {
	return flag.fallback
}

// Err validates the definition without contacting a driver. Empty names,
// including the zero Flag, and inherited namespace errors wrap ErrInvalidFlag;
// other names are literal. A nonempty joined name cannot repair an invalid
// namespace or an empty leaf name.
//
// Example:
//
//	if err := feature.NewFlag("new-checkout", false).Err(); err != nil {
//		log.Print(err)
//	}
func (flag Flag[T]) Err() error {
	if flag.err != nil {
		return flag.err
	}

	if flag.name == "" {
		return fmt.Errorf("feature: empty flag name: %w", ErrInvalidFlag)
	}

	return nil
}
