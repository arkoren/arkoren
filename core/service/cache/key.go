package cache

import (
	"context"
	"fmt"
	"log/slog"
	"time"
)

// Namespace is an immutable literal prefix and write policy for typed keys.
// Names are opaque bytes: no escaping, splitting, or normalization occurs.
// A zero Namespace is invalid. Use Name explicitly when inspecting identities;
// ordinary formatting and structured logging redact them.
//
//	users := cache.NewNamespace[string]("users")
//	key := users.Key("42") // key.Name() == "users:42"
type Namespace[T any] struct {
	// name is the exact published prefix.
	name string
	// separator joins the next child, not existing portions of name.
	separator string
	// policy stores the inherited write lifetime and first error.
	policy KeyBuilder
	// _ makes different value instantiations nonconvertible without storage.
	_ [0]*T
}

// Key identifies a JSON value of T and its write lifetime. TTL is policy, not
// identity: different policies can address the same stored entry. The zero
// value is invalid; NewKey can deliberately address an empty identity.
// Formatting and logging redact identities; Name is the explicit escape hatch.
//
//	key := cache.NewKey[string]("application.version")
//	value, err := store.Get(ctx, key)
//	if err != nil {
//		return err
//	}
//	_ = value
type Key[T any] struct {
	// name is the exact driver identity, including a deliberately empty name.
	name string
	// policy stores the write lifetime and first declaration error.
	policy KeyBuilder
	// valid distinguishes a constructed empty key from a zero Key.
	valid bool
	// _ prevents conversion to a different value instantiation without storage.
	_ [0]*T
}

// NamespaceBuilder configures a fresh namespace draft. Retaining and changing a
// draft after publication cannot change an existing definition. Methods return
// the draft for fluent configuration; the first invalid setting stays invalid.
//
//	space := cache.NewNamespace[string]("users", func(namespace *cache.NamespaceBuilder) {
//		namespace.Separator(":").TTL(time.Minute)
//	})
//	_ = space.Key("42")
type NamespaceBuilder struct {
	// separator is used literally, including the explicit empty string.
	separator string
	// policy contains shared lifetime validation and sticky errors.
	policy KeyBuilder
}

// KeyBuilder configures only the write lifetime of a key. It does not alter
// identity. A retained draft cannot change a published Key.
//
//	key := cache.NewKey[string]("version", func(entry *cache.KeyBuilder) {
//		entry.TTL(time.Minute)
//	})
//	_ = key.TTL()
type KeyBuilder struct {
	// ttl is the write lifetime; zero means no expiration.
	ttl time.Duration
	// err preserves the first invalid declaration without exposing identities.
	err error
}

// NamespaceBuilderFunc configures one shared draft synchronously. Callbacks run
// once in order even for invalid declarations; nil entries panic. Expanded nil
// or empty slices omit edits. Publication copies the completed draft.
//
//	var policy cache.NamespaceBuilderFunc = func(namespace *cache.NamespaceBuilder) {
//		namespace.TTL(time.Minute)
//	}
//	_ = cache.NewNamespace[string]("users", policy)
type NamespaceBuilderFunc = func(*NamespaceBuilder)

// KeyBuilderFunc configures a key draft once, synchronously and in order, even
// when its definition is invalid. Nil entries panic; empty slices omit edits.
//
//	var policy cache.KeyBuilderFunc = func(entry *cache.KeyBuilder) {
//		entry.TTL(time.Minute)
//	}
//	_ = cache.NewKey[string]("version", policy)
type KeyBuilderFunc = func(*KeyBuilder)

// NewNamespace declares a nonempty literal prefix with separator ":" and no
// expiry by default. Invalid declarations retain ErrInvalidKey or ErrInvalidTTL
// for validation at use. Callback panics propagate, including nil callbacks.
// To retain identities created with the former dot default, explicitly set
// NamespaceBuilder.Separator("."). Existing entries are not renamed or read
// through aliases; prefix bytes and standalone NewKey identities stay unchanged.
//
//	users := cache.NewNamespace[string]("users")
//	_ = users.Key("42")
func NewNamespace[T any](name string, configure ...NamespaceBuilderFunc) Namespace[T] {
	space := Namespace[T]{name: name, separator: ":"}

	if name == "" {
		space.policy.err = ErrInvalidKey
	}

	return space.Derive(configure...)
}

// NewKey declares a standalone typed key with an exact physical name. It does
// not join, normalize, or validate the name's bytes; an explicit empty name is
// valid. The default TTL is zero (no expiry); a negative lifetime invalidates
// the key. Callbacks follow KeyBuilderFunc's publication rules.
//
//	key := cache.NewKey[string]("application.version", func(entry *cache.KeyBuilder) {
//		entry.TTL(time.Minute)
//	})
//	if err := store.Put(ctx, key, "v1"); err != nil {
//		return err
//	}
func NewKey[T any](exact string, configure ...KeyBuilderFunc) Key[T] {
	return (Key[T]{name: exact, valid: true}).Derive(configure...)
}

// TTL sets the lifetime inherited by future keys and subnamespaces. Zero means no
// expiry; negative values preserve an error wrapping ErrInvalidTTL, even if a
// later callback supplies a valid lifetime.
//
//	space := cache.NewNamespace[string]("users", func(namespace *cache.NamespaceBuilder) {
//		namespace.TTL(time.Minute)
//	})
//	_ = space.TTL()
func (namespace *NamespaceBuilder) TTL(ttl time.Duration) *NamespaceBuilder {
	namespace.policy.TTL(ttl)

	return namespace
}

// Separator sets the literal join string for future children. Empty means
// concatenation, not the default. Existing name bytes are never rewritten.
//
//	space := cache.NewNamespace[string]("users:", func(namespace *cache.NamespaceBuilder) {
//		namespace.Separator("")
//	})
//	_ = space.Key("42") // users:42
func (namespace *NamespaceBuilder) Separator(separator string) *NamespaceBuilder {
	namespace.separator = separator

	return namespace
}

// TTL sets a key's write lifetime without changing its identity. Zero disables
// expiration. Negative durations preserve the first error wrapping ErrInvalidTTL.
//
//	key := cache.NewKey[string]("version", func(entry *cache.KeyBuilder) {
//		entry.TTL(time.Minute)
//	})
//	_ = key.TTL()
func (entry *KeyBuilder) TTL(ttl time.Duration) *KeyBuilder {
	entry.ttl = ttl

	if ttl < 0 && entry.err == nil {
		entry.err = fmt.Errorf("cache key lifetime: %w", ErrInvalidTTL)
	}

	return entry
}

// Derive copies this namespace and applies edits without reapplying defaults.
// Inherited errors remain sticky. Callbacks follow NamespaceBuilderFunc's rules.
//
//	base := cache.NewNamespace[string]("users")
//	local := base.Derive(func(namespace *cache.NamespaceBuilder) {
//		namespace.TTL(time.Minute)
//	})
//	_ = local.Key("42")
func (space Namespace[T]) Derive(configure ...NamespaceBuilderFunc) Namespace[T] {
	draft := &NamespaceBuilder{separator: space.separator, policy: space.policy}

	if space.name == "" && draft.policy.err == nil {
		draft.policy.err = ErrInvalidKey
	}

	for _, edit := range configure {
		edit(draft)
	}

	space.separator, space.policy = draft.separator, draft.policy

	return space
}

// Subnamespace joins name using the parent's separator, then applies child edits.
// A child's Separator changes only its future children, never this join. TTL
// and errors are inherited. An empty child is invalid, not a singleton key.
//
//	base := cache.NewNamespace[string]("app")
//	child := base.Subnamespace("users", func(namespace *cache.NamespaceBuilder) {
//		namespace.Separator("/")
//	})
//	_ = child.Key("42") // app:users/42
func (space Namespace[T]) Subnamespace(name string, configure ...NamespaceBuilderFunc) Namespace[T] {
	child := space

	child.policy.err = space.Err()

	if name == "" && child.policy.err == nil {
		child.policy.err = ErrInvalidKey
	}

	child.name = space.name + space.separator + name

	return child.Derive(configure...)
}

// Key joins a nonempty literal child using this namespace's separator, inheriting
// TTL and sticky errors. Use NewKey for standalone or empty identities.
//
//	space := cache.NewNamespace[string]("users")
//	_ = space.Key("42") // users:42
func (space Namespace[T]) Key(name string, configure ...KeyBuilderFunc) Key[T] {
	key := Key[T]{name: space.name + space.separator + name, policy: space.policy, valid: true}

	key.policy.err = space.Err()

	if name == "" && key.policy.err == nil {
		key.policy.err = ErrInvalidKey
	}

	return key.Derive(configure...)
}

// Derive copies this key and applies write-policy edits. Identity is unchanged;
// inherited errors remain sticky and a zero Key cannot become valid through edits.
//
//	base := cache.NewKey[string]("version")
//	local := base.Derive(func(entry *cache.KeyBuilder) {
//		entry.TTL(time.Minute)
//	})
//	_ = local.Name() // version
func (key Key[T]) Derive(configure ...KeyBuilderFunc) Key[T] {
	draft := key.policy

	draft.err = key.Err()

	for _, edit := range configure {
		edit(&draft)
	}

	key.policy = draft

	return key
}

// Name explicitly reveals the exact prefix, without validation or normalization.
// Avoid logging it: names may contain personal identifiers or secrets.
//
//	name := cache.NewNamespace[string]("users").Name() // users
//	_ = name
func (space Namespace[T]) Name() string { return space.name }

// Separator returns the literal join string used for future children, including
// an explicit empty string. It does not describe separators already in Name.
//
//	separator := cache.NewNamespace[string]("users").Separator() // :
//	_ = separator
func (space Namespace[T]) Separator() string { return space.separator }

// TTL returns the inherited write lifetime, not a stored entry's remaining time.
// Zero means no expiry. Inspect Err before using an invalid definition.
//
//	lifetime := cache.NewNamespace[string]("users").TTL() // 0
//	_ = lifetime
func (space Namespace[T]) TTL() time.Duration { return space.policy.ttl }

// Err returns the first declaration failure, or nil for a valid namespace.
// Empty and zero namespaces wrap ErrInvalidKey; errors never contain names.
//
//	if err := cache.NewNamespace[string]("").Err(); err != nil {
//		_ = errors.Is(err, cache.ErrInvalidKey)
//	}
func (space Namespace[T]) Err() error {
	if space.policy.err != nil {
		return space.policy.err
	}

	if space.name == "" {
		return ErrInvalidKey
	}

	return nil
}

// Name explicitly reveals the exact driver identity, even for invalid keys.
// Do not log it indiscriminately: ordinary formatting intentionally redacts it.
//
//	name := cache.NewKey[string]("version").Name() // version
//	_ = name
func (key Key[T]) Name() string { return key.name }

// TTL returns the write lifetime; zero means no expiry. This is not the stored
// entry's remaining lifetime, which is available from Cache.TTL.
//
//	lifetime := cache.NewKey[string]("version").TTL() // 0
//	_ = lifetime
func (key Key[T]) TTL() time.Duration { return key.policy.ttl }

// Err returns the first declaration error or nil. A zero Key is invalid, unlike
// NewKey with an explicit empty name. Returned errors contain no identities.
//
//	key := cache.NewKey[string]("")
//	if err := key.Err(); err != nil {
//		return err
//	}
func (key Key[T]) Err() error {
	if key.policy.err != nil {
		return key.policy.err
	}

	if !key.valid {
		return ErrInvalidKey
	}

	return nil
}

// String redacts identity for ordinary formatting; use Name for explicit access.
//
//	text := cache.NewKey[string]("secret").String() // cache.Key[redacted]
//	_ = text
func (key Key[T]) String() string { return "cache.Key[redacted]" }

// GoString redacts identity even in Go-syntax diagnostic formatting.
//
//	text := cache.NewKey[string]("secret").GoString()
//	_ = text // cache.Key[redacted]
func (key Key[T]) GoString() string { return key.String() }

// LogValue redacts the complete key for structured loggers, not just its name.
//
//	value := cache.NewKey[string]("secret").LogValue()
//	_ = value.String() // cache.Key[redacted]
func (key Key[T]) LogValue() slog.Value { return slog.StringValue(key.String()) }

// Format prevents any format verb from reflecting a key's private identity,
// including mismatched verbs such as %d. String quoting and hexadecimal verbs
// apply to the redacted marker; other verbs render that marker as a string.
// Width and precision never apply to the actual key name. Use Name explicitly
// when the physical identity is required.
//
// Example:
//
//	key := cache.NewKey[string]("private-key")
//	text := fmt.Sprintf("%d", key) // cache.Key[redacted]
//	_ = text
func (key Key[T]) Format(state fmt.State, verb rune) {
	formatDefinition(state, verb, key.String())
}

// String redacts the namespace identity in ordinary diagnostic formatting.
//
//	text := cache.NewNamespace[string]("secret").String()
//	_ = text // cache.Namespace[redacted]
func (space Namespace[T]) String() string { return "cache.Namespace[redacted]" }

// GoString redacts the namespace identity in Go-syntax diagnostic formatting.
//
//	text := cache.NewNamespace[string]("secret").GoString()
//	_ = text // cache.Namespace[redacted]
func (space Namespace[T]) GoString() string { return space.String() }

// LogValue redacts the complete definition when a structured logger inspects it.
//
//	value := cache.NewNamespace[string]("secret").LogValue()
//	_ = value.String() // cache.Namespace[redacted]
func (space Namespace[T]) LogValue() slog.Value { return slog.StringValue(space.String()) }

// Format prevents diagnostic formatting from reflecting a namespace's private
// prefix. Quoting and hexadecimal verbs format only its redacted marker; all
// other verbs render that marker as a string. Explicit Name access remains the
// intentional escape hatch, independently of formatting flags or precision.
//
// Example:
//
//	space := cache.NewNamespace[string]("private-prefix")
//	text := fmt.Sprintf("%d", space) // cache.Namespace[redacted]
//	_ = text
func (space Namespace[T]) Format(state fmt.State, verb rune) {
	formatDefinition(state, verb, space.String())
}

// formatDefinition applies formatting only to a redacted definition marker.
// String-like verbs retain their ordinary quoting/encoding; unsupported verbs
// normalize to string formatting so fmt never falls back to reflecting fields.
// Both Key and Namespace must use this same policy to prevent identity leaks.
func formatDefinition(state fmt.State, verb rune, marker string) {
	switch verb {
	case 'q', 'x', 'X':
	default:
		verb = 's'
	}

	// Formatter has no error result; fmt owns the enclosing output operation.
	_, _ = fmt.Fprintf(state, fmt.FormatString(state, verb), marker)
}

// validateKey gives cancellation precedence over declaration errors. Facades
// call it before encoding, decoding, computing, or performing driver I/O.
func validateKey[T any](ctx context.Context, key Key[T]) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	return key.Err()
}
