package secret

import (
	"fmt"
	"log/slog"
)

// Namespace composes literal provider names for related Secret definitions
// sharing one JSON decoding type. It is immutable and does not register schemas,
// cache values, pin versions, isolate a provider, or provide bulk operations.
// Its zero value is invalid. Name explicitly exposes the otherwise redacted
// prefix; provider-specific naming restrictions remain the driver's concern.
//
// Example:
//
//	credentials := secret.NewNamespace[map[string]string]("production", "/")
//	database := credentials.Secret("database")
//	config, err := secrets.Get(ctx, database)
//	if err != nil {
//		return err
//	}
//	useCredential(config["password"])
type Namespace[T any] struct {
	// name is the literal prefix used for future child and leaf names.
	name string

	// separator joins this prefix to each immediate child without escaping.
	separator string

	// err preserves the first declaration failure through descendants.
	err error

	// _ binds the namespace to T without storing a decoded secret value.
	_ [0]*T
}

// NamespaceBuilder is the mutable construction draft for namespace settings.
// Callbacks may retain it, but later edits cannot change published namespaces.
// It contains no decoding type or provider state; use Namespace for reads.
//
// Example:
//
//	credentials := secret.NewNamespace[string]("production", "/", func(names *secret.NamespaceBuilder) {
//		names.Separator("-")
//	})
//	_ = credentials.Secret("token")
type NamespaceBuilder struct {
	// separator is the exact delimiter to publish, including an empty string.
	separator string
}

// NamespaceBuilderFunc configures a namespace draft synchronously. Callbacks
// run once in order, even for invalid declarations; nil entries panic and an
// expanded nil or empty slice supplies no edits. Callback panics propagate.
//
// Example:
//
//	var policies []secret.NamespaceBuilderFunc
//	credentials := secret.NewNamespace[string]("production", "/", policies...)
//	_ = credentials.Secret("token")
type NamespaceBuilderFunc = func(*NamespaceBuilder)

// NewNamespace declares a literal prefix and its required separator without
// I/O. There is intentionally no default delimiter because providers accept
// different naming syntax. Empty separator means direct concatenation; empty
// prefix is ErrInvalidSecret. Neither names nor separators are normalized.
// Callbacks run once synchronously in order even for invalid prefixes; nil
// entries and callback panics propagate. Publication detaches retained drafts.
//
// Example:
//
//	credentials := secret.NewNamespace[string]("production", "/")
//	if err := credentials.Err(); err != nil {
//		return err
//	}
//	_ = credentials.Secret("token")
func NewNamespace[T any](prefix string, separator string, configure ...NamespaceBuilderFunc) Namespace[T] {
	return publishNamespace[T](prefix, separator, nil, configure)
}

// publishNamespace validates the prefix before running policies and copies
// completed settings out of the draft. An inherited error takes precedence;
// callbacks cannot repair a missing prefix or a failed ancestor.
func publishNamespace[T any](name, separator string, err error, configure []NamespaceBuilderFunc) Namespace[T] {
	if err == nil && name == "" {
		err = ErrInvalidSecret
	}

	names := &NamespaceBuilder{separator: separator}

	for _, apply := range configure {
		apply(names)
	}

	return Namespace[T]{name: name, separator: names.separator, err: err}
}

// Separator replaces the draft's future join delimiter and returns the same
// draft for chaining. Every string is valid, including empty and multibyte
// strings. It cannot rewrite a prefix already composed by Subnamespace.
//
// Example:
//
//	credentials := secret.NewNamespace[string]("production", "/", func(names *secret.NamespaceBuilder) {
//		names.Separator("")
//	})
//	_ = credentials.Secret("Token").Name() // "productionToken"
func (names *NamespaceBuilder) Separator(separator string) *NamespaceBuilder {
	names.separator = separator

	return names
}

// Name returns the literal composed prefix, even for invalid declarations.
// This is an intentional escape hatch from diagnostic and slog redaction;
// check Err before using it as a provider identity.
//
// Example:
//
//	prefix := secret.NewNamespace[string]("production", "/").Name()
//	_ = prefix // "production"
func (namespace Namespace[T]) Name() string {
	return namespace.name
}

// Separator returns the exact delimiter used for future names. An empty
// delimiter means concatenation, not a default; no trimming or escaping occurs.
//
// Example:
//
//	delimiter := secret.NewNamespace[string]("production", "/").Separator()
//	_ = delimiter // "/"
func (namespace Namespace[T]) Separator() string {
	return namespace.separator
}

// Err reports the first inherited declaration failure, or ErrInvalidSecret
// for an empty prefix or zero namespace. Errors contain no names or types.
// Descendants retain this failure even when their composed names are nonempty.
//
// Example:
//
//	if err := secret.NewNamespace[string]("", "/").Err(); err != nil {
//		return err
//	}
func (namespace Namespace[T]) Err() error {
	if namespace.err != nil {
		return namespace.err
	}

	if namespace.name == "" {
		return ErrInvalidSecret
	}

	return nil
}

// Derive publishes an independent namespace with the same prefix, delimiter,
// and first error before applying callbacks. It never reapplies defaults or
// mutates its source. Callbacks follow NewNamespace's ordering and panic rules.
//
// Example:
//
//	credentials := secret.NewNamespace[string]("production", "/")
//	flat := credentials.Derive(func(names *secret.NamespaceBuilder) {
//		names.Separator("-")
//	})
//	_ = flat.Secret("token").Name() // "production-token"
func (namespace Namespace[T]) Derive(configure ...NamespaceBuilderFunc) Namespace[T] {
	return publishNamespace[T](namespace.name, namespace.separator, namespace.Err(), configure)
}

// Subnamespace joins name with the parent's delimiter before applying child
// callbacks, which affect only future joins. Empty child names are invalid;
// the parent's error wins over a child error. All callbacks still run once in
// order with the same panic rules as NewNamespace, and drafts are detached.
//
// Example:
//
//	credentials := secret.NewNamespace[string]("production", "/")
//	database := credentials.Subnamespace("database", func(names *secret.NamespaceBuilder) {
//		names.Separator("-")
//	})
//	_ = database.Secret("password").Name() // "production/database-password"
func (namespace Namespace[T]) Subnamespace(name string, configure ...NamespaceBuilderFunc) Namespace[T] {
	err := namespace.Err()

	if err == nil && name == "" {
		err = ErrInvalidSecret
	}

	return publishNamespace[T](namespace.name+namespace.separator+name, namespace.separator, err, configure)
}

// Secret binds a literal joined name to the namespace's JSON decoding type.
// Empty leaf names and invalid ancestors produce ErrInvalidSecret definitions
// that Get rejects before provider I/O. Reads still select the current version;
// this operation neither loads nor registers a secret.
//
// Example:
//
//	credentials := secret.NewNamespace[string]("production", "/")
//	token, err := secrets.Get(ctx, credentials.Secret("token"))
//	if err != nil {
//		return err
//	}
//	useCredential(token)
func (namespace Namespace[T]) Secret(name string) Secret[T] {
	err := namespace.Err()

	if err == nil && name == "" {
		err = ErrInvalidSecret
	}

	return Secret[T]{name: namespace.name + namespace.separator + name, err: err}
}

// Format redacts the prefix for every fmt verb, including Go-syntax output.
// Use Name explicitly when a provider identity is required.
//
// Example:
//
//	text := fmt.Sprintf("%#v", secret.NewNamespace[string]("production", "/"))
//	_ = text // "secret.Namespace([redacted])"
func (namespace Namespace[T]) Format(state fmt.State, _ rune) {
	_, _ = state.Write([]byte("secret.Namespace([redacted])")) // fmt owns write errors.
}

// LogValue redacts the prefix in structured logs independently of fmt.
// Explicit Name access and decoded secret values are not sanitized.
//
// Example:
//
//	logger.Info("secret namespace", "namespace", secret.NewNamespace[string]("production", "/"))
func (namespace Namespace[T]) LogValue() slog.Value {
	return slog.StringValue("secret.Namespace([redacted])")
}
