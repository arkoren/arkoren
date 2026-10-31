package session

import (
	"errors"
	"fmt"
	"log/slog"
)

// ErrInvalidField identifies an empty or zero session field or namespace, or a
// descendant of an invalid namespace. Operations reject these declarations
// before reading or changing session state.
//
// Example:
//
//	if errors.Is(session.NewField[int]("").Err(), session.ErrInvalidField) {
//		// Supply a nonempty application-owned field name.
//	}
var ErrInvalidField = errors.New("session invalid field")

// Field binds a literal storage name to its Go value type. It is immutable and
// safe to share. Its zero value is invalid; construct fields with [NewField].
// Independently declared fields with the same name share persisted JSON, so
// applications must keep their types consistent across deployments.
//
// Example:
//
//	theme := session.NewField[string]("theme")
//	if err := current.Put(theme, "dark"); err != nil {
//		return err
//	}
type Field[T any] struct {
	// name is the exact persisted member name, without normalization.
	name string

	// err preserves an invalid ancestor even when the composed name is nonempty.
	err error

	// _ prevents assignment and conversion between different T values
	// without retaining a value or allocating storage for it.
	_ [0]*T
}

// NewField declares an immutable field with a literal, nonempty name. It does
// not register a schema or inspect T. Empty names produce an invalid field
// whose [Field.Err] and all session operations return [ErrInvalidField].
//
// Example:
//
//	userID := session.NewField[int]("user_id")
//	id, err := current.Get(userID)
//	if err != nil {
//		return err
//	}
//	_ = id
func NewField[T any](name string) Field[T] {
	return Field[T]{name: name}
}

// Name returns the exact persisted member name, including an empty name for
// an invalid declaration. Ordinary formatting redacts this identity instead.
//
// Example:
//
//	name := session.NewField[string]("theme").Name() // "theme"
//	_ = name
func (field Field[T]) Name() string {
	return field.name
}

// Err returns [ErrInvalidField] for an empty name, zero value, or invalid
// namespace ancestor, and nil for a valid declaration. The error never includes
// the field name or value type.
//
// Example:
//
//	if err := session.NewField[int]("user_id").Err(); err != nil {
//		return err
//	}
func (field Field[T]) Err() error {
	if field.err != nil {
		return field.err
	}

	if field.name == "" {
		return ErrInvalidField
	}

	return nil
}

// Format redacts the identity for every fmt verb, including detailed Go
// formatting. Use [Field.Name] explicitly when the identity is needed.
//
// Example:
//
//	text := fmt.Sprintf("%+v", session.NewField[int]("user_id"))
//	_ = text // "session.Field([redacted])"
func (field Field[T]) Format(state fmt.State, _ rune) {
	_, _ = state.Write([]byte("session.Field([redacted])")) // fmt owns write errors.
}

// LogValue redacts the field identity in structured logs, independently of
// the formatting path used by fmt.
//
// Example:
//
//	logger.Info("session field", "field", session.NewField[int]("user_id"))
func (field Field[T]) LogValue() slog.Value {
	return slog.StringValue("session.Field([redacted])")
}
