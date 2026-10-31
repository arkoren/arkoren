package feature

import (
	"fmt"
	"log/slog"
)

// Namespace groups literal provider names under an immutable prefix and an
// exact typed fallback. It neither registers flags nor isolates provider state.
// Use NewFlag for standalone names. A zero Namespace is invalid; errors remain
// attached to all descendants, even when their assembled names are nonempty.
// Formatting and structured logging redact the prefix and fallback.
//
// Example:
//
//	checkout := feature.NewNamespace("checkout", false)
//	on, err := flags.Value(ctx, checkout.Flag("enabled"), subject)
//	if err != nil {
//		log.Print(err) // on retains checkout.Fallback().
//	}
type Namespace[T Primitive] struct {
	// name is the literal provider prefix, including any parent separators.
	name string

	// separator joins future children; empty means direct concatenation.
	separator string

	// fallback retains T exactly without conversion through a provider value.
	fallback T

	// err is the first definition error, inherited by every descendant.
	err error
}

// NamespaceBuilder configures future joins, not the namespace's type or
// fallback. Constructors publish a detached copy, so retaining a draft cannot
// change an existing Namespace. Drafts are only for synchronous construction.
//
// Example:
//
//	checkout := feature.NewNamespace("checkout", false, func(names *feature.NamespaceBuilder) {
//		names.Separator("/")
//	})
//	flag := checkout.Flag("enabled") // checkout/enabled
type NamespaceBuilder struct {
	// separator is copied from the parent before callbacks edit future joins.
	separator string
}

// NamespaceBuilderFunc edits a shared construction draft synchronously.
// Callbacks run once in order, even for invalid definitions. Nil entries panic;
// expanding a nil or empty slice supplies no callbacks.
//
// Example:
//
//	var slash feature.NamespaceBuilderFunc = func(names *feature.NamespaceBuilder) {
//		names.Separator("/")
//	}
//	checkout := feature.NewNamespace("checkout", false, slash)
type NamespaceBuilderFunc = func(*NamespaceBuilder)

// NewNamespace publishes a literal prefix and typed fallback without I/O.
// The default separator is ":". Empty prefixes wrap ErrInvalidFlag; whitespace
// and separators in names remain unchanged. Callbacks follow
// NamespaceBuilderFunc's ordering and misuse rules, including invalid prefixes.
//
// Example:
//
//	checkout := feature.NewNamespace("checkout", false)
//	flag := checkout.Flag("enabled") // checkout:enabled, fallback false
func NewNamespace[T Primitive](prefix string, fallback T, configure ...NamespaceBuilderFunc) Namespace[T] {
	return Namespace[T]{name: prefix, separator: ":", fallback: fallback}.Derive(configure...)
}

// Separator selects the literal string used to join future child names.
// Empty concatenates names directly; no escaping, trimming, or validation of
// separator content occurs. It returns the same construction draft for chaining.
//
// Example:
//
//	checkout := feature.NewNamespace("checkout", false, func(names *feature.NamespaceBuilder) {
//		names.Separator("")
//	})
//	flag := checkout.Flag("Enabled") // checkoutEnabled
func (names *NamespaceBuilder) Separator(separator string) *NamespaceBuilder {
	names.separator = separator

	return names
}

// Name returns the exact composed provider prefix. A zero Namespace returns
// an empty prefix; a nonempty prefix alone does not guarantee validity.
//
// Example: prefix := feature.NewNamespace("checkout", false).Name()
func (namespace Namespace[T]) Name() string {
	return namespace.name
}

// Separator returns the literal join string for future children. An explicit
// empty separator remains empty through Derive and Subnamespace.
//
// Example: separator := feature.NewNamespace("checkout", false).Separator()
func (namespace Namespace[T]) Separator() string {
	return namespace.separator
}

// Fallback returns the exact typed value inherited by every child definition.
// It is not installed in the provider and cannot be changed by draft callbacks.
//
// Example: fallback := feature.NewNamespace("uploads", int64(25)).Fallback()
func (namespace Namespace[T]) Fallback() T {
	return namespace.fallback
}

// Err reports the first definition error without I/O. An empty prefix,
// including a zero Namespace, wraps ErrInvalidFlag. Errors survive derivation
// and child creation; diagnostics never include names or fallback values.
//
// Example:
//
//	if err := feature.NewNamespace("checkout", false).Err(); err != nil {
//		log.Print(err)
//	}
func (namespace Namespace[T]) Err() error {
	if namespace.err != nil {
		return namespace.err
	}

	if namespace.name == "" {
		return fmt.Errorf("feature: empty namespace name: %w", ErrInvalidFlag)
	}

	return nil
}

// Derive publishes an independent variant using the existing separator,
// fallback, prefix, and first error. Defaults are not reapplied. All callbacks
// edit one fresh draft under NamespaceBuilderFunc's rules, even when invalid.
//
// Example:
//
//	slash := feature.NewNamespace("checkout", false).Derive(func(names *feature.NamespaceBuilder) {
//		names.Separator("/")
//	})
//	flag := slash.Flag("enabled") // checkout/enabled
func (namespace Namespace[T]) Derive(configure ...NamespaceBuilderFunc) Namespace[T] {
	namespace.err = namespace.Err()
	names := &NamespaceBuilder{separator: namespace.separator}

	for _, configure := range configure {
		configure(names)
	}

	namespace.separator = names.separator

	return namespace
}

// Subnamespace appends name using the parent's separator, then applies child
// callbacks to future joins. Empty child names are invalid even if the assembled
// prefix is nonempty. The fallback and first error are inherited unchanged.
//
// Example:
//
//	checkout := feature.NewNamespace("checkout", false)
//	admin := checkout.Subnamespace("admin", func(names *feature.NamespaceBuilder) {
//		names.Separator("/")
//	})
//	flag := admin.Flag("enabled") // checkout:admin/enabled
func (namespace Namespace[T]) Subnamespace(name string, configure ...NamespaceBuilderFunc) Namespace[T] {
	namespace.err = namespace.Err()

	if name == "" && namespace.err == nil {
		namespace.err = fmt.Errorf("feature: empty namespace name: %w", ErrInvalidFlag)
	}

	namespace.name += namespace.separator + name

	return namespace.Derive(configure...)
}

// Flag publishes a typed leaf with this namespace's fallback and a literal
// joined name. Empty leaf names and inherited namespace errors remain invalid,
// so evaluation and memory setup reject them before accessing a driver.
//
// Example:
//
//	checkout := feature.NewNamespace("checkout", false).Flag("enabled")
//	on, err := flags.Value(ctx, checkout, subject)
//	if err != nil {
//		log.Print(err) // on is false.
//	}
func (namespace Namespace[T]) Flag(name string) Flag[T] {
	definition := NewFlag(namespace.name+namespace.separator+name, namespace.fallback)

	definition.err = namespace.Err()

	if name == "" && definition.err == nil {
		definition.err = fmt.Errorf("feature: empty flag name: %w", ErrInvalidFlag)
	}

	return definition
}

// Format redacts the prefix and fallback for every fmt verb, including
// Go-syntax output. Use Name and Fallback explicitly to obtain those values.
//
// Example:
//
//	description := fmt.Sprintf("%#v", feature.NewNamespace("checkout", false))
//	_ = description // "feature.Namespace([redacted])"
func (namespace Namespace[T]) Format(state fmt.State, _ rune) {
	_, _ = state.Write([]byte("feature.Namespace([redacted])")) // fmt owns write errors.
}

// LogValue redacts the prefix and fallback in structured logs independently
// of fmt. Explicit Name and Fallback access is not sanitized.
//
// Example: logger.Info("flags configured", "namespace", feature.NewNamespace("checkout", false))
func (namespace Namespace[T]) LogValue() slog.Value {
	return slog.StringValue("feature.Namespace([redacted])")
}
