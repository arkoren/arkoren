package session

import (
	"fmt"
	"log/slog"
)

// Namespace is an immutable literal prefix for fields of T. It composes JSON
// member names, not cache record names: [Builder.Namespace] selects cache records
// separately. Namespaces do not isolate storage, register schemas, or own TTL;
// session middleware owns lifetime. The zero value is invalid.
//
// Example:
//
//	preferences := session.NewNamespace[string]("preferences")
//	theme := preferences.Field("theme") // preferences:theme
//	if err := current.Put(theme, "dark"); err != nil {
//		return err
//	}
type Namespace[T any] struct {
	// name is the exact published prefix, without escaping or normalization.
	name string

	// separator joins future children without rewriting existing name bytes.
	separator string

	// err preserves invalid ancestors even after a nonempty name is composed.
	err error

	// _ prevents assignment and conversion between different T instantiations.
	_ [0]*T
}

// NamespaceBuilder configures one namespace draft. Methods return the same
// draft for chaining. Publication copies its settings, so retaining and changing
// the draft cannot change a published namespace or its fields.
//
// Example:
//
//	preferences := session.NewNamespace[string]("preferences", func(fields *session.NamespaceBuilder) {
//		fields.Separator("/")
//	})
//	_ = preferences.Field("theme") // preferences/theme
type NamespaceBuilder struct {
	// separator is the literal future join string, including explicit empty.
	separator string
}

// NamespaceBuilderFunc edits a fresh draft synchronously. Callbacks run exactly
// once in order, including invalid declarations. Nil entries and application
// panics propagate; expanded nil or empty slices omit edits.
//
// Example:
//
//	var policy session.NamespaceBuilderFunc = func(fields *session.NamespaceBuilder) {
//		fields.Separator("/")
//	}
//	_ = session.NewNamespace[string]("preferences", policy)
type NamespaceBuilderFunc = func(*NamespaceBuilder)

// NewNamespace declares a nonempty literal prefix with default separator ":".
// Empty prefixes retain [ErrInvalidField], including in descendants. Callbacks
// follow [NamespaceBuilderFunc]'s rules; they cannot repair invalid prefixes.
// Prefer [NewField] when the exact standalone member name is already known.
//
// Example:
//
//	preferences := session.NewNamespace[string]("preferences")
//	if err := preferences.Err(); err != nil {
//		return err
//	}
//	_ = preferences.Field("theme")
func NewNamespace[T any](prefix string, configure ...NamespaceBuilderFunc) Namespace[T] {
	return (Namespace[T]{name: prefix, separator: ":"}).Derive(configure...)
}

// Separator replaces the join string for future children. Empty means literal
// concatenation, not the default; no trimming, escaping, or normalization occurs.
//
// Example:
//
//	preferences := session.NewNamespace[string]("preferences:", func(fields *session.NamespaceBuilder) {
//		fields.Separator("")
//	})
//	_ = preferences.Field("theme") // preferences:theme
func (fields *NamespaceBuilder) Separator(separator string) *NamespaceBuilder {
	fields.separator = separator

	return fields
}

// Name returns the exact composed prefix, including invalid declarations.
// Formatting redacts this identity; use Name only for deliberate inspection.
//
// Example:
//
//	name := session.NewNamespace[int]("cart").Name() // "cart"
//	_ = name
func (space Namespace[T]) Name() string {
	return space.name
}

// Separator returns the literal join string for future fields and namespaces.
// An empty result means concatenation; Derive does not reapply the default.
//
// Example:
//
//	separator := session.NewNamespace[int]("cart").Separator() // ":"
//	_ = separator
func (space Namespace[T]) Separator() string {
	return space.separator
}

// Err returns [ErrInvalidField] for zero, empty, or invalid descendant
// declarations. Errors never disclose names or types. A nonempty Name alone
// does not establish validity: an invalid ancestor remains invalid.
//
// Example:
//
//	if err := session.NewNamespace[int]("cart").Err(); err != nil {
//		return err
//	}
func (space Namespace[T]) Err() error {
	if space.err != nil {
		return space.err
	}

	if space.name == "" {
		return ErrInvalidField
	}

	return nil
}

// Derive publishes an independent namespace after ordered edits on a fresh
// draft. It preserves the prefix, inherited errors, and separator without
// reapplying defaults. Callbacks follow [NamespaceBuilderFunc]'s rules.
//
// Example:
//
//	base := session.NewNamespace[string]("preferences")
//	local := base.Derive(func(fields *session.NamespaceBuilder) {
//		fields.Separator("/")
//	})
//	_ = local.Field("theme") // preferences/theme
func (space Namespace[T]) Derive(configure ...NamespaceBuilderFunc) Namespace[T] {
	space.err = space.Err()
	draft := &NamespaceBuilder{separator: space.separator}

	for _, edit := range configure {
		edit(draft)
	}

	space.separator = draft.separator

	return space
}

// Subnamespace joins a literal child using the parent's separator before
// applying child callbacks. A child separator affects only future joins. Empty
// children and invalid ancestors retain [ErrInvalidField] through later edits.
//
// Example:
//
//	base := session.NewNamespace[string]("preferences")
//	child := base.Subnamespace("display", func(fields *session.NamespaceBuilder) {
//		fields.Separator("/")
//	})
//	_ = child.Field("theme") // preferences:display/theme
func (space Namespace[T]) Subnamespace(name string, configure ...NamespaceBuilderFunc) Namespace[T] {
	child := space

	child.err = space.Err()

	if name == "" {
		child.err = ErrInvalidField
	}

	child.name = space.name + space.separator + name

	return child.Derive(configure...)
}

// Format redacts namespace identities for every fmt verb, including detailed Go
// formatting. Use [Namespace.Name] explicitly when the identity is needed.
//
// Example:
//
//	text := fmt.Sprintf("%+v", session.NewNamespace[int]("cart"))
//	_ = text // "session.Namespace([redacted])"
func (space Namespace[T]) Format(state fmt.State, _ rune) {
	_, _ = state.Write([]byte("session.Namespace([redacted])")) // fmt owns write errors.
}

// LogValue redacts namespace identities in structured logs independently of
// fmt formatting. It does not expose the prefix, separator, or value type.
//
// Example:
//
//	logger.Info("session fields", "namespace", session.NewNamespace[int]("cart"))
func (space Namespace[T]) LogValue() slog.Value {
	return slog.StringValue("session.Namespace([redacted])")
}
