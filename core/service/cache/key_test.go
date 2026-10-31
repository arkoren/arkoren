package cache_test

import (
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"testing"
	"time"

	"arkoren.dev/core/service/cache"
)

// TestNamespaceDefaults verifies literal colon joins and no-expiry policy.
func TestNamespaceDefaults(t *testing.T) {
	t.Parallel()

	space := cache.NewNamespace[string]("users")
	key := space.Key("42")

	if space.Name() != "users" || space.Separator() != ":" || space.TTL() != 0 {
		t.Fatal("unexpected namespace defaults")
	}

	if key.Name() != "users:42" || key.TTL() != 0 || key.Err() != nil {
		t.Fatal("unexpected key defaults")
	}
}

// TestNamespaceExplicitDotPreservesLegacyNames verifies opting into the former
// separator preserves literal dots across derivation and nested namespaces.
func TestNamespaceExplicitDotPreservesLegacyNames(t *testing.T) {
	t.Parallel()

	space := cache.NewNamespace[string]("app.v1", func(namespace *cache.NamespaceBuilder) {
		namespace.Separator(".")
	})

	key := space.Derive().Subnamespace("users").Key("42")

	if key.Name() != "app.v1.users.42" || key.Err() != nil {
		t.Fatalf("explicit dot identity = %q, %v", key.Name(), key.Err())
	}
}

// TestNamespaceLiteralBytes verifies joins preserve separators, whitespace,
// delimiters, Unicode, and arbitrary bytes rather than encoding components.
func TestNamespaceLiteralBytes(t *testing.T) {
	t.Parallel()

	space := cache.NewNamespace[string](" \x00é.")
	key := space.Subnamespace(".child ").Key(".leaf:\xff")

	if key.Name() != " \x00é.:.child :.leaf:\xff" {
		t.Fatalf("literal identity = %q", key.Name())
	}
}

// TestSubnamespaceUsesParentSeparator verifies child edits affect subsequent joins
// only, and lifetime inheritance can be overridden independently at each level.
func TestSubnamespaceUsesParentSeparator(t *testing.T) {
	t.Parallel()

	space := cache.NewNamespace[int]("app", func(namespace *cache.NamespaceBuilder) {
		namespace.Separator(":").TTL(time.Hour)
	})

	child := space.Subnamespace("users", func(namespace *cache.NamespaceBuilder) {
		namespace.Separator("/").TTL(time.Minute)
	})

	key := child.Key("42")

	if child.Name() != "app:users" || key.Name() != "app:users/42" || key.TTL() != time.Minute {
		t.Fatal("child did not inherit the join and override the policy")
	}

	if space.Key("42").TTL() != time.Hour || space.Separator() != ":" {
		t.Fatal("parent was changed")
	}
}

// TestEmptySeparatorConcatenates verifies an explicit empty separator is not
// normalized back to the default during construction, derivation, or inheritance.
func TestEmptySeparatorConcatenates(t *testing.T) {
	t.Parallel()

	space := cache.NewNamespace[string]("app", func(namespace *cache.NamespaceBuilder) {
		namespace.Separator("")
	})

	key := space.Derive().Subnamespace("users").Key("42")

	if key.Name() != "appusers42" || space.Separator() != "" {
		t.Fatalf("identity = %q", key.Name())
	}
}

// TestNamespacePublicationDetachesDraft verifies publication copies policy roots
// and Derive can remove inherited expiry without changing the original space.
func TestNamespacePublicationDetachesDraft(t *testing.T) {
	t.Parallel()

	var retained *cache.NamespaceBuilder
	space := cache.NewNamespace[string]("app", func(namespace *cache.NamespaceBuilder) {
		retained = namespace
		namespace.TTL(time.Hour)
	})

	local := space.Derive(func(namespace *cache.NamespaceBuilder) {
		namespace.Separator("/").TTL(0)
	})
	retained.Separator(".").TTL(-time.Second)

	if space.TTL() != time.Hour || space.Err() != nil || space.Separator() != ":" {
		t.Fatal("retained draft changed parent")
	}

	if local.TTL() != 0 || local.Key("42").Name() != "app/42" {
		t.Fatal("derived policy or identity changed")
	}
}

// TestKeyPublicationDetachesDraft verifies a key snapshots the completed draft
// and a derived write lifetime does not change identity or parent policy.
func TestKeyPublicationDetachesDraft(t *testing.T) {
	t.Parallel()

	var entry *cache.KeyBuilder
	key := cache.NewKey[string]("app/42", func(draft *cache.KeyBuilder) {
		entry = draft
		draft.TTL(time.Minute)
	})

	derived := key.Derive(func(draft *cache.KeyBuilder) {
		draft.TTL(0)
	})
	entry.TTL(-time.Second)

	if key.Name() != "app/42" || key.TTL() != time.Minute || key.Err() != nil {
		t.Fatal("published child changed")
	}

	if derived.Name() != key.Name() || derived.TTL() != 0 || derived.Err() != nil {
		t.Fatal("derived key changed identity or inherited stale policy")
	}
}

// TestNewKeyPreservesExactNames verifies standalone keys accept explicit empty
// strings, unlike invalid zero keys and empty namespace children.
func TestNewKeyPreservesExactNames(t *testing.T) {
	t.Parallel()

	for _, name := range []string{"", " ", "a.b:c/", "\x00\xff"} {
		key := cache.NewKey[int](name)

		if key.Name() != name || key.Err() != nil {
			t.Fatalf("standalone key %q rejected or modified", name)
		}
	}

	var zero cache.Key[int]

	if !errors.Is(zero.Err(), cache.ErrInvalidKey) || !errors.Is(zero.Derive().Err(), cache.ErrInvalidKey) {
		t.Fatal("zero key became valid")
	}

	if !errors.Is(cache.NewNamespace[int]("app").Key("").Err(), cache.ErrInvalidKey) {
		t.Fatal("empty child accepted")
	}
}

// TestInvalidNamespaceErrorsPropagate verifies an invalid root cannot become
// valid through subnamespaces, keys, derivation, or later successful settings.
func TestInvalidNamespaceErrorsPropagate(t *testing.T) {
	t.Parallel()

	var zero cache.Namespace[int]
	space := zero.Subnamespace("child").Derive(func(namespace *cache.NamespaceBuilder) {
		namespace.TTL(time.Minute)
	})

	if !errors.Is(space.Err(), cache.ErrInvalidKey) || !errors.Is(space.Key("id").Err(), cache.ErrInvalidKey) {
		t.Fatal("zero namespace error lost")
	}

	if !errors.Is(cache.NewNamespace[int]("").Err(), cache.ErrInvalidKey) {
		t.Fatal("empty root accepted")
	}

	if !errors.Is(cache.NewNamespace[int]("app").Subnamespace("").Err(), cache.ErrInvalidKey) {
		t.Fatal("empty subnamespace accepted")
	}
}

// TestNegativeTTLIsSticky verifies negative policy fails without leaking names,
// even when later settings or derivation provide a valid lifetime.
func TestNegativeTTLIsSticky(t *testing.T) {
	t.Parallel()

	space := cache.NewNamespace[int]("secret", func(namespace *cache.NamespaceBuilder) {
		namespace.TTL(-time.Second).TTL(time.Minute)
	})

	key := space.Subnamespace("private").Key("identifier", func(entry *cache.KeyBuilder) {
		entry.TTL(0)
	})

	if !errors.Is(key.Err(), cache.ErrInvalidTTL) || strings.Contains(key.Err().Error(), "secret") {
		t.Fatal("inherited error lost or identity exposed")
	}

	standalone := cache.NewKey[int]("secret", func(entry *cache.KeyBuilder) {
		entry.TTL(-time.Second).TTL(0)
	}).Derive()

	if !errors.Is(standalone.Err(), cache.ErrInvalidTTL) {
		t.Fatal("standalone key error lost")
	}
}

// TestInvalidCallbacksRunOnceInOrder verifies declarations run every supplied
// edit directly on a shared draft, including invalid required names.
func TestInvalidCallbacksRunOnceInOrder(t *testing.T) {
	t.Parallel()

	calls := 0
	var retained *cache.NamespaceBuilder
	space := cache.NewNamespace[int]("", func(namespace *cache.NamespaceBuilder) {
		calls++
		retained = namespace
		namespace.TTL(-time.Second)
	}, func(namespace *cache.NamespaceBuilder) {
		if calls != 1 || retained != namespace {
			t.Fatal("callbacks not ordered on one draft")
		}

		calls++
	})

	key := space.Key("", func(entry *cache.KeyBuilder) {
		calls++
		entry.TTL(time.Minute)
	})

	if calls != 3 || !errors.Is(key.Err(), cache.ErrInvalidKey) {
		t.Fatal("callbacks skipped or first error lost")
	}
}

// TestNilNamespaceCallbackPanics verifies supplied nil is programmer misuse,
// including when the identity is invalid; later callbacks do not execute.
func TestNilNamespaceCallbackPanics(t *testing.T) {
	t.Parallel()

	defer func() {
		if recover() == nil {
			t.Error("nil callback did not panic")
		}
	}()
	cache.NewNamespace[int]("", nil, func(namespace *cache.NamespaceBuilder) {
		t.Error("callback after nil executed")
	})
}

// TestNilKeyCallbackPanics verifies key callbacks obey the same direct-call
// contract even when inherited declaration errors are already present.
func TestNilKeyCallbackPanics(t *testing.T) {
	t.Parallel()

	defer func() {
		if recover() == nil {
			t.Error("nil callback did not panic")
		}
	}()
	cache.NewNamespace[int]("").Key("", nil)
}

// TestEmptyCallbackExpansionPreservesDefinitions verifies omission is distinct
// from a supplied nil entry and derivation without edits preserves values.
func TestEmptyCallbackExpansionPreservesDefinitions(t *testing.T) {
	t.Parallel()

	var namespaces []cache.NamespaceBuilderFunc
	var entries []cache.KeyBuilderFunc
	space := cache.NewNamespace[string]("app", namespaces...)
	key := cache.NewKey[string]("", entries...)

	if space.Derive(namespaces...).Key("id", entries...).Name() != "app:id" || key.Derive(entries...).Err() != nil {
		t.Fatal("empty expansion changed a definition")
	}
}

// TestDefinitionFormattingRedactsNames checks ordinary and Go-syntax formatting
// and structured-log values never reveal identity, while Name remains explicit.
func TestDefinitionFormattingRedactsNames(t *testing.T) {
	t.Parallel()

	space := cache.NewNamespace[string]("secret-identity")
	key := space.Key("private")

	for _, text := range []string{
		fmt.Sprintf("%v %+v %#v %s %q", key, key, key, key, key),
		fmt.Sprintf("%v %+v %#v %s %q", space, space, space, space, space),
		key.LogValue().String(), space.LogValue().String(),
	} {
		if strings.Contains(text, "secret") || strings.Contains(text, "private") {
			t.Fatalf("identity exposed: %s", text)
		}
	}

	if key.Name() != "secret-identity:private" {
		t.Fatal("explicit name unavailable")
	}
}

// TestGenericKeyRetypingRejected compiles negative fixtures against the current
// public API. Both assignment and explicit conversion must reject changing T.
func TestGenericKeyRetypingRejected(t *testing.T) {
	t.Parallel()

	command := exec.CommandContext(t.Context(), "go", "test", "-gcflags=-e", "./testdata/retyping")

	output, err := command.CombinedOutput()

	if err == nil {
		t.Fatal("cross-instantiation retyping compiled")
	}

	for _, diagnostic := range []string{
		"cannot convert cache.NewKey[string]",
		"cannot convert cache.NewNamespace[string]",
		"cannot use cache.NewKey[string]",
		"cannot use 42",
		"argument to store.Get[int]",
		"argument to store.Put[int]",
		"argument to store.Increment",
		"argument to store.Decrement",
		"argument to store.IncrementWithTTL",
		"in call to store.Has, cannot infer T",
		"in call to store.Delete, cannot infer T",
		"in call to store.TTL, cannot infer T",
	} {
		if !strings.Contains(string(output), diagnostic) {
			t.Fatalf("missing %q diagnostic: %s", diagnostic, output)
		}
	}
}

// TestDeriveDraftsCannotMutatePublishedDefinitions verifies retained derivation
// drafts do not modify either the original definition or its published variant.
func TestDeriveDraftsCannotMutatePublishedDefinitions(t *testing.T) {
	t.Parallel()

	base := cache.NewNamespace[string]("app")
	var namespace *cache.NamespaceBuilder
	derived := base.Derive(func(draft *cache.NamespaceBuilder) {
		namespace = draft
		draft.Separator(".").TTL(time.Minute)
	})
	namespace.Separator("/").TTL(-time.Second)
	var entry *cache.KeyBuilder
	key := derived.Key("id").Derive(func(draft *cache.KeyBuilder) {
		entry = draft
		draft.TTL(time.Hour)
	})
	entry.TTL(-time.Second)

	if derived.Separator() != "." || derived.TTL() != time.Minute || derived.Err() != nil {
		t.Fatal("derived namespace retained mutable draft")
	}

	if key.Name() != "app.id" || key.TTL() != time.Hour || key.Err() != nil {
		t.Fatal("derived key retained mutable draft")
	}

	if base.Separator() != ":" || base.TTL() != 0 {
		t.Fatal("derivation changed parent")
	}
}

// TestNilNewKeyCallbackPanics verifies exact-name construction does not filter
// programmer misuse, including when the exact identity is deliberately empty.
func TestNilNewKeyCallbackPanics(t *testing.T) {
	t.Parallel()

	defer func() {
		if recover() == nil {
			t.Error("nil standalone key callback did not panic")
		}
	}()
	cache.NewKey[string]("", nil)
}

// TestDefinitionCallbackPanicPropagates verifies application panic identity is
// unchanged, rather than converted into a key declaration error.
func TestDefinitionCallbackPanicPropagates(t *testing.T) {
	t.Parallel()

	marker := &struct{}{}
	defer func() {
		if recovered := recover(); recovered != marker {
			t.Errorf("panic = %v, want original marker", recovered)
		}
	}()
	cache.NewKey[int]("key", func(entry *cache.KeyBuilder) {
		panic(marker)
	})
}

// TestStateCacheFormattingRejectsReflectiveVerbLeaks covers format mismatches
// as well as ordinary string verbs: diagnostics must not fall back to reflecting
// a definition's private name when the caller accidentally chooses a bad verb.
func TestStateCacheFormattingRejectsReflectiveVerbLeaks(t *testing.T) {
	t.Parallel()

	for _, definition := range []any{
		cache.NewKey[string]("private-cache-identity"),
		cache.NewNamespace[string]("private-cache-identity"),
	} {
		text := fmt.Sprintf("%d %f %t %v %#v %s %q", definition, definition, definition,
			definition, definition, definition, definition)

		if strings.Contains(text, "private-cache-identity") {
			t.Fatalf("formatting exposed a cache identity: %s", text)
		}
	}
}
