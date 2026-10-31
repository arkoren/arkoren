package secret

import (
	"errors"
	"fmt"
	"log/slog"
)

// ErrInvalidSecret identifies an empty or zero typed secret or namespace
// definition, including a leaf inherited from an invalid namespace. Get rejects
// it before calling the driver; the error contains no secret identity.
//
// Example:
//
//	if errors.Is(secret.NewSecret[int]("").Err(), secret.ErrInvalidSecret) {
//		// Supply a nonempty provider secret name.
//	}
var ErrInvalidSecret = errors.New("secret invalid secret")

// Secret binds a literal secret name to its JSON decoding type. Share
// this immutable definition between consumers instead of selecting a type at
// each read. The zero value is invalid; construct it with NewSecret or a
// Namespace's Secret method. Invalid namespace declarations remain invalid
// through their leaves, even when the composed provider name is nonempty.
// It does not register a schema: separately declared definitions can disagree,
// and JSON-compatible changes can still decode without an error.
//
// Example:
//
//	database := secret.NewSecret[map[string]string]("prod/database")
//	config, err := secrets.Get(ctx, database)
//	if err != nil {
//		return err
//	}
//	useCredential(config["password"])
type Secret[T any] struct {
	// name is the provider identity, retained literally without normalization.
	name string

	// err retains an inherited namespace declaration failure even when the
	// composed provider name is nonempty.
	err error

	// _ prevents conversion or assignment between different decoding types
	// without retaining an instance of T.
	_ [0]*T
}

// NewSecret declares a shared name and decoding type without I/O or
// inspecting T. Empty names produce invalid definitions; provider-specific name
// restrictions remain the driver's responsibility. It selects the current
// version at each Get, not the version current when the definition is made.
//
// Example:
//
//	database := secret.NewSecret[map[string]string]("prod/database")
//	if err := database.Err(); err != nil {
//		return err
//	}
func NewSecret[T any](name string) Secret[T] {
	return Secret[T]{name: name}
}

// Name returns the literal provider identity, including an empty name for an
// invalid definition. This explicit accessor bypasses formatting redaction.
//
// Example:
//
//	name := secret.NewSecret[map[string]string]("prod/database").Name()
//	_ = name // "prod/database"
func (secret Secret[T]) Name() string {
	return secret.name
}

// Err returns an inherited namespace failure first, then ErrInvalidSecret
// for an empty name or zero value, and nil otherwise. It never includes the
// secret identity or decoding type.
//
// Example:
//
//	if err := secret.NewSecret[int]("retry-limit").Err(); err != nil {
//		return err
//	}
func (secret Secret[T]) Err() error {
	if secret.err != nil {
		return secret.err
	}

	if secret.name == "" {
		return ErrInvalidSecret
	}

	return nil
}

// Format redacts the identity for every fmt verb, including detailed Go
// formatting. Use Name explicitly when you need the provider identity.
//
// Example:
//
//	text := fmt.Sprintf("%#v", secret.NewSecret[int]("retry-limit"))
//	_ = text // "secret.Secret([redacted])"
func (secret Secret[T]) Format(state fmt.State, _ rune) {
	_, _ = state.Write([]byte("secret.Secret([redacted])")) // fmt owns write errors.
}

// LogValue redacts the identity in structured logs independently of fmt.
// This does not redact decoded values, raw driver references, or returned errors.
//
// Example:
//
//	logger.Info("secret definition", "definition", secret.NewSecret[int]("retry-limit"))
func (secret Secret[T]) LogValue() slog.Value {
	return slog.StringValue("secret.Secret([redacted])")
}
