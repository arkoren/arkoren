package secret_test

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os/exec"
	"strings"
	"testing"

	"arkoren.dev/core/service/secret"
	"arkoren.dev/core/service/secret/memorysecret"
)

// ExampleNewNamespace shows exact-name composition with a separator selected
// for the provider. A child policy changes future joins, not its own prefix.
func ExampleNewNamespace() {
	credentials := secret.NewNamespace[map[string]string]("production", "/")
	database := credentials.Subnamespace("database", func(names *secret.NamespaceBuilder) {
		names.Separator("-")
	})

	definition := database.Secret("credentials")

	if err := definition.Err(); err != nil {
		fmt.Println(err)

		return
	}

	store := secret.New(memorysecret.New(func(secrets *memorysecret.Builder) {
		secrets.Values(map[string][]byte{
			"production/database-credentials": []byte(`{"password":"example"}`),
		})
	}))

	value, err := store.Get(context.Background(), definition)

	if err != nil {
		fmt.Println(err)

		return
	}

	fmt.Println(definition.Name(), value["password"] != "")
	// Output: production/database-credentials true
}

// TestNamespaceLiteralNames verifies that joins retain spaces, punctuation,
// and delimiters already present in both parent and child names.
func TestNamespaceLiteralNames(t *testing.T) {
	t.Parallel()

	credentials := secret.NewNamespace[int](" prod/ ", "/")
	leaf := credentials.Subnamespace("/database ").Secret(" /password")

	if leaf.Name() != " prod/ //database / /password" || leaf.Err() != nil {
		t.Fatalf("literal name = %q, error = %v", leaf.Name(), leaf.Err())
	}
}

// TestNamespaceExplicitEmptySeparator checks that empty is concatenation,
// including after no-op derivation, rather than selecting a hidden default.
func TestNamespaceExplicitEmptySeparator(t *testing.T) {
	t.Parallel()

	credentials := secret.NewNamespace[int]("prod", "").Derive().Subnamespace("Database")

	if credentials.Separator() != "" || credentials.Secret("Password").Name() != "prodDatabasePassword" {
		t.Fatal("empty separator was not preserved")
	}
}

// TestNamespaceMultibyteSeparator checks delimiters are literal strings,
// not restricted to ASCII or truncated to a single rune.
func TestNamespaceMultibyteSeparator(t *testing.T) {
	t.Parallel()

	credentials := secret.NewNamespace[int]("prod", "🗝::")

	if credentials.Secret("password").Name() != "prod🗝::password" {
		t.Fatal("multibyte separator changed")
	}
}

// TestNamespaceChildOverridesOnlyFutureJoins checks parent composition takes
// place before child callbacks and no-op derivation preserves the child policy.
func TestNamespaceChildOverridesOnlyFutureJoins(t *testing.T) {
	t.Parallel()

	credentials := secret.NewNamespace[int]("prod", "/", func(names *secret.NamespaceBuilder) {
		names.Separator(":")
	})

	database := credentials.Subnamespace("database", func(names *secret.NamespaceBuilder) {
		names.Separator("-")
	}).Derive()

	if database.Name() != "prod:database" || database.Secret("password").Name() != "prod:database-password" {
		t.Fatal("child override rewrote its prefix or was lost")
	}

	if credentials.Secret("password").Name() != "prod:password" {
		t.Fatal("child override mutated parent")
	}
}

// TestNamespacePublicationDetachesDraft checks constructors, Derive, and
// Subnamespace publish copies rather than retaining mutable draft pointers.
func TestNamespacePublicationDetachesDraft(t *testing.T) {
	t.Parallel()

	var retained *secret.NamespaceBuilder
	capture := func(names *secret.NamespaceBuilder) {
		retained = names
		names.Separator(":")
	}

	credentials := secret.NewNamespace[int]("prod", "/", capture)
	retained.Separator("bad")

	variant := credentials.Derive(capture)
	retained.Separator("bad")

	child := credentials.Subnamespace("database", capture)
	retained.Separator("bad")

	if credentials.Secret("token").Name() != "prod:token" || variant.Secret("token").Name() != "prod:token" {
		t.Fatal("retained draft changed published root or variant")
	}

	if child.Secret("token").Name() != "prod:database:token" {
		t.Fatal("retained draft changed published child")
	}
}

// TestNamespaceCallbacksRunInOrderOnInvalid checks policy evaluation is
// synchronous, once-only, ordered, and not skipped for invalid declarations.
func TestNamespaceCallbacksRunInOrderOnInvalid(t *testing.T) {
	t.Parallel()

	var calls []string
	first := func(names *secret.NamespaceBuilder) {
		calls = append(calls, "first")
		names.Separator(":")
	}

	second := func(names *secret.NamespaceBuilder) {
		calls = append(calls, "second")
		names.Separator("-")
	}

	credentials := secret.NewNamespace[int]("", "/", first, second)
	credentials = credentials.Derive(first, second).Subnamespace("child", first, second)

	if strings.Join(calls, ",") != "first,second,first,second,first,second" {
		t.Fatalf("callback order = %v", calls)
	}

	if credentials.Separator() != "-" || !errors.Is(credentials.Err(), secret.ErrInvalidSecret) {
		t.Fatal("invalid declaration was repaired or final policy lost")
	}
}

// TestNamespaceNilCallbackPanics checks that a supplied nil policy is misuse,
// not an omitted edit, even when the declaration already has an error.
func TestNamespaceNilCallbackPanics(t *testing.T) {
	t.Parallel()

	defer func() {
		if recover() == nil {
			t.Fatal("nil callback did not panic")
		}
	}()
	secret.NewNamespace[int]("", "/", nil)
}

// TestNamespaceDeriveNilCallbackPanics verifies that inherited invalidity
// does not suppress callback misuse during derivation.
func TestNamespaceDeriveNilCallbackPanics(t *testing.T) {
	t.Parallel()

	defer func() {
		if recover() == nil {
			t.Fatal("nil derivation callback did not panic")
		}
	}()
	(secret.Namespace[int]{}).Derive(nil)
}

// TestNamespaceChildCallbackPanicPropagates verifies construction does not
// recover application panics, even for an empty child of an invalid parent.
func TestNamespaceChildCallbackPanicPropagates(t *testing.T) {
	t.Parallel()

	defer func() {
		if recovered := recover(); recovered != "callback failed" {
			t.Fatalf("callback panic = %v", recovered)
		}
	}()
	(secret.Namespace[int]{}).Subnamespace("", func(names *secret.NamespaceBuilder) {
		names.Separator(":")
		panic("callback failed")
	})
}

// TestNamespaceExpandedNilCallbacks checks a nil policy slice is a no-op
// across construction and immutable derivation, preserving empty separators.
func TestNamespaceExpandedNilCallbacks(t *testing.T) {
	t.Parallel()

	var policies []secret.NamespaceBuilderFunc
	credentials := secret.NewNamespace[int]("prod", "", policies...).Derive(policies...)
	child := credentials.Subnamespace("Database", policies...)

	if child.Secret("Password").Name() != "prodDatabasePassword" || child.Err() != nil {
		t.Fatal("expanded nil callbacks changed settings")
	}
}

// TestNamespaceInvalidAncestorsStayInvalid checks failed parent and child
// declarations cannot become valid simply by composing a nonempty leaf name.
func TestNamespaceInvalidAncestorsStayInvalid(t *testing.T) {
	t.Parallel()

	valid := secret.NewNamespace[int]("private-prefix", "/")

	for _, credentials := range []secret.Namespace[int]{
		{},
		secret.NewNamespace[int]("", "/"),
		valid.Subnamespace(""),
		valid.Subnamespace("").Derive(),
		(secret.Namespace[int]{}).Subnamespace("child"),
	} {
		if !errors.Is(credentials.Err(), secret.ErrInvalidSecret) {
			t.Fatal("invalid namespace accepted")
		}

		leaf := credentials.Derive().Subnamespace("nested").Secret("password")

		if _, err := secret.New(nil).Get(t.Context(), leaf); !errors.Is(err, secret.ErrInvalidSecret) {
			t.Fatalf("invalid leaf Get = %v", err)
		}
	}
}

// TestNamespaceEmptyLeafIsInvalid checks a nonempty composed prefix cannot
// hide an empty leaf, and validation errors disclose neither prefix nor type.
func TestNamespaceEmptyLeafIsInvalid(t *testing.T) {
	t.Parallel()

	leaf := secret.NewNamespace[int]("private-prefix", "/").Secret("")

	if !errors.Is(leaf.Err(), secret.ErrInvalidSecret) {
		t.Fatal("empty leaf accepted")
	}

	if _, err := secret.New(nil).Get(t.Context(), leaf); !errors.Is(err, secret.ErrInvalidSecret) {
		t.Fatalf("empty leaf Get = %v", err)
	}

	if strings.Contains(leaf.Err().Error(), "private") || strings.Contains(leaf.Err().Error(), "int") {
		t.Fatal("declaration error disclosed identity or type")
	}
}

// TestNamespaceCancellationPrecedesValidation checks invalid namespace leaves
// retain the typed reader's cancellation precedence without provider access.
func TestNamespaceCancellationPrecedesValidation(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	leaf := (secret.Namespace[int]{}).Secret("password")

	if _, err := secret.New(nil).Get(ctx, leaf); !errors.Is(err, context.Canceled) {
		t.Fatalf("Get error = %v", err)
	}
}

// TestNamespaceReadsCurrentAndPreservesPinnedAccess checks exact provider
// identity, typed JSON reads after rotation, and unchanged raw version access.
func TestNamespaceReadsCurrentAndPreservesPinnedAccess(t *testing.T) {
	t.Parallel()

	store := secret.New(memorysecret.New())
	leaf := secret.NewNamespace[map[string]string](" prod/", "/").Secret("database ")

	first, err := store.Put(t.Context(), " prod//database ", []byte(`{"password":"old"}`))

	if err != nil {
		t.Fatal(err)
	}

	before, err := store.Get(t.Context(), leaf)

	if err != nil || before["password"] != "old" {
		t.Fatalf("initial = %v, error = %v", before, err)
	}

	if _, err := store.Update(t.Context(), " prod//database ", []byte(`{"password":"new"}`)); err != nil {
		t.Fatal(err)
	}

	after, err := store.Get(t.Context(), leaf)

	if err != nil || after["password"] != "new" {
		t.Fatalf("rotated = %v, error = %v", after, err)
	}

	pinned, err := store.Driver().Get(t.Context(), secret.Reference{Name: leaf.Name(), Version: first.Version})

	if err != nil || string(pinned.Data) != `{"password":"old"}` {
		t.Fatalf("pinned = %s, error = %v", pinned.Data, err)
	}
}

// TestNamespaceFormattingRedactsIdentity checks ordinary, detailed, quoted,
// hexadecimal, and structured diagnostics while preserving explicit Name access.
func TestNamespaceFormattingRedactsIdentity(t *testing.T) {
	t.Parallel()

	credentials := secret.NewNamespace[int]("private-prefix", "private-separator")
	text := fmt.Sprintf("%v %+v %#v %s %q %x", credentials, credentials, credentials, credentials, credentials, credentials)

	if strings.Contains(text, "private") || strings.Contains(slog.AnyValue(credentials).Resolve().String(), "private") {
		t.Fatal("namespace formatting leaked identity")
	}

	if fmt.Sprint(credentials) != "secret.Namespace([redacted])" {
		t.Fatal("unexpected namespace format label")
	}

	if slog.AnyValue(credentials).Resolve().String() != "secret.Namespace([redacted])" {
		t.Fatal("unexpected structured namespace label")
	}

	if credentials.Name() != "private-prefix" {
		t.Fatal("explicit name access changed")
	}
}

// TestNamespaceRetypingRejected checks namespace assignment and conversion,
// and generated leaf conversion and explicit reader type arguments, all fail.
func TestNamespaceRetypingRejected(t *testing.T) {
	t.Parallel()

	command := exec.CommandContext(t.Context(), "go", "test", "-gcflags=-e", "./testdata/namespaceretyping")

	output, err := command.CombinedOutput()

	if err == nil {
		t.Fatal("namespace retyping compiled")
	}

	for _, diagnostic := range []string{
		"cannot convert secret.NewNamespace[string]",
		"cannot use secret.NewNamespace[string]",
		"cannot convert names.Secret",
		"cannot use names.Secret",
		"argument to store.Get[int]",
	} {
		if !strings.Contains(string(output), diagnostic) {
			t.Fatalf("missing %q diagnostic: %s", diagnostic, output)
		}
	}
}
