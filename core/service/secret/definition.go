package secret

import (
	"bytes"
	"encoding"
	"encoding/json/v2"
	"errors"
	"fmt"
	"log/slog"
	"unicode/utf8"
)

// ErrInvalidSecret identifies an empty or zero typed secret or namespace
// definition, including a leaf inherited from an invalid namespace. Get rejects
// it before calling the driver; the error contains no secret identity.
//
// Example:
//
//	if errors.Is(secret.NewSecret[int]("").Err(), secret.ErrInvalidSecret) {
//		// Supply a nonempty reference.
//	}
var ErrInvalidSecret = errors.New("secret invalid secret")

// errNotUTF8 reports a string-typed secret whose bytes are not valid UTF-8.
// It carries no payload, so it is safe to wrap and log.
var errNotUTF8 = errors.New("payload is not valid UTF-8")

// Secret binds a secret to the Go type its payload decodes into. Share this
// immutable definition between consumers instead of selecting a type at each
// read. The zero value is invalid; construct it with [NewSecret], [NewBinding]
// or a [Namespace]'s Secret method. Invalid namespace declarations remain
// invalid through their leaves, even when the composed reference is nonempty.
// It does not register a schema: separately declared definitions can disagree,
// and JSON-compatible changes can still decode without an error.
//
// A reference is a string "scheme:name", for example "aws:billing/key",
// "vault:app/db", "file:/run/secrets/token" or "env:API_TOKEN". A static
// definition ([NewSecret]) carries its reference; a binding ([NewBinding])
// carries a key that the store resolves to a reference at each Get.
//
// Decoding is by T, and the payload is always bytes:
//   - string: the payload must be valid UTF-8, and one trailing "\n" or "\r\n"
//     is removed, because files and shell output usually end with one;
//   - []byte: a copy of the payload, unchanged;
//   - any type whose pointer implements [encoding.TextUnmarshaler] (such as
//     key.Shared): UnmarshalText receives the payload without that one trailing
//     newline;
//   - everything else, including named string types: JSON, decoded by
//     encoding/json/v2.
//
// Example:
//
//	database := secret.NewSecret[map[string]string]("vault:app/database")
//	config, err := secrets.Get(ctx, database)
//	if err != nil {
//		return err
//	}
//	useCredential(config["password"])
type Secret[T any] struct {
	// name is the static reference, or the binding key when binding is set,
	// retained literally without normalization.
	name string

	// binding marks name as a key resolved through the store's bindings lookup
	// instead of a reference.
	binding bool

	// err retains an inherited namespace declaration failure even when the
	// composed reference is nonempty.
	err error

	// _ prevents conversion or assignment between different decoding types
	// without retaining an instance of T.
	_ [0]*T
}

// NewSecret declares a static reference and the type it decodes into, without
// I/O or inspecting T. Empty references produce invalid definitions; the
// scheme and name syntax belong to the store's driver, which a secretmux driver
// splits at the first colon. It selects the current version at each Get, not
// the version current when the definition is made.
//
// Example:
//
//	database := secret.NewSecret[map[string]string]("vault:app/database")
//	if err := database.Err(); err != nil {
//		return err
//	}
func NewSecret[T any](reference string) Secret[T] {
	return Secret[T]{name: reference}
}

// NewBinding declares a secret whose reference is chosen by deployment rather
// than code. At each Get the store looks key up through [Builder.Bindings]
// (typically os.LookupEnv) and reads the reference it returns, so
// DATABASE_PASSWORD=aws:prod/db moves the secret between backends without a
// rebuild. A binding must be bound: a missing lookup, an unset key or an empty
// value makes Get fail with [ErrUnbound]. There are no defaults. An empty key
// produces an invalid definition.
//
// Example:
//
//	password := secret.NewBinding[string]("DATABASE_PASSWORD")
//	value, err := secrets.Get(ctx, password)
//	if err != nil {
//		return err
//	}
//	useCredential(value)
func NewBinding[T any](key string) Secret[T] {
	return Secret[T]{name: key, binding: true}
}

// Name returns the literal static reference, or the binding key for a
// definition made by [NewBinding]. An invalid definition may return an empty
// name. This explicit accessor bypasses formatting redaction.
//
// Example:
//
//	reference := secret.NewSecret[map[string]string]("vault:app/database").Name()
//	_ = reference // "vault:app/database"
func (secret Secret[T]) Name() string {
	return secret.name
}

// Err returns an inherited namespace failure first, then ErrInvalidSecret
// for an empty reference or key, or a zero value, and nil otherwise. It never
// includes the secret identity or decoding type.
//
// Example:
//
//	if err := secret.NewSecret[int]("env:RETRY_LIMIT").Err(); err != nil {
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

// resolve returns the reference to read: the static reference, or for a
// binding the value bindings returns for its key. A nil lookup, an unset key
// and an empty value are all [ErrUnbound]; the error names only the key.
func (secret Secret[T]) resolve(bindings func(string) (string, bool)) (string, error) {
	if !secret.binding {
		return secret.name, nil
	}

	if bindings == nil {
		return "", fmt.Errorf("%w: %s (no bindings lookup configured)", ErrUnbound, secret.name)
	}

	reference, found := bindings(secret.name)

	if !found || reference == "" {
		return "", fmt.Errorf("%w: %s", ErrUnbound, secret.name)
	}

	return reference, nil
}

// decode converts a payload into T by the rules documented on [Secret]. The
// switch looks at *T because T alone cannot be switched on without reflection
// and TextUnmarshaler is implemented on pointers. data is not retained or
// modified, so callers may clear it afterwards. Errors never include the
// payload.
func (secret Secret[T]) decode(data []byte) (res T, err error) {
	switch target := any(&res).(type) {
	case *string:
		if !utf8.Valid(data) {
			return res, errNotUTF8
		}

		*target = string(trimNewline(data))
	case *[]byte:
		*target = bytes.Clone(data)
	case encoding.TextUnmarshaler:
		err = target.UnmarshalText(trimNewline(data))
	default:
		err = json.Unmarshal(data, &res)
	}

	return res, err
}

// trimNewline removes exactly one trailing "\n" or "\r\n", the terminator that
// editors and shells append to a single-line secret. Further newlines and a
// lone trailing "\r" are payload.
func trimNewline(data []byte) []byte {
	trimmed, found := bytes.CutSuffix(data, []byte("\n"))

	if !found {
		return data
	}

	trimmed, _ = bytes.CutSuffix(trimmed, []byte("\r"))

	return trimmed
}

// Format redacts the identity for every fmt verb, including detailed Go
// formatting. Use Name explicitly when you need the reference.
//
// Example:
//
//	text := fmt.Sprintf("%#v", secret.NewSecret[int]("env:RETRY_LIMIT"))
//	_ = text // "secret.Secret([redacted])"
func (secret Secret[T]) Format(state fmt.State, _ rune) {
	_, _ = state.Write([]byte("secret.Secret([redacted])")) // fmt owns write errors.
}

// LogValue redacts the identity in structured logs independently of fmt.
// This does not redact decoded values, raw driver references, or returned errors.
//
// Example:
//
//	logger.Info("secret definition", "definition", secret.NewSecret[int]("env:RETRY_LIMIT"))
func (secret Secret[T]) LogValue() slog.Value {
	return slog.StringValue("secret.Secret([redacted])")
}
