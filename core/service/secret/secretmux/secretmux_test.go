package secretmux_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"arkoren.dev/core/service/secret"
	"arkoren.dev/core/service/secret/envsecret"
	"arkoren.dev/core/service/secret/memorysecret"
	"arkoren.dev/core/service/secret/secretmux"
	"arkoren.dev/core/service/secret/secrettest"
)

// TestConformance runs the secret conformance suite through the mux over a
// memory source, adding the scheme the mux routes on.
func TestConformance(t *testing.T) {
	t.Parallel()

	secrettest.Run(t, func(*testing.T) secret.Driver {
		driver, err := secretmux.New(func(sources *secretmux.Builder) {
			sources.Source("memory", memorysecret.New())
		})

		if err != nil {
			t.Fatal(err)
		}

		return driver
	}, secrettest.WithReference(func(name string) string {
		return "memory:" + name
	}))
}

// TestNewRejectsInvalidSources verifies each construction failure wraps
// ErrInvalidConfig.
func TestNewRejectsInvalidSources(t *testing.T) {
	t.Parallel()

	memory := memorysecret.New()
	failures := map[string]secretmux.BuilderFunc{
		"no sources": func(*secretmux.Builder) {},
		"empty scheme": func(sources *secretmux.Builder) {
			sources.Source("", memory)
		},
		"colon in scheme": func(sources *secretmux.Builder) {
			sources.Source("a:b", memory)
		},
		"nil driver": func(sources *secretmux.Builder) {
			sources.Source("memory", nil)
		},
		"duplicate scheme": func(sources *secretmux.Builder) {
			sources.Source("memory", memory).Source("memory", memorysecret.New())
		},
	}

	for name, configure := range failures {
		if _, err := secretmux.New(configure); !errors.Is(err, secretmux.ErrInvalidConfig) {
			t.Errorf("%s: New error = %v, want ErrInvalidConfig", name, err)
		}
	}
}

// TestUnknownSchemeListsSortedSchemes verifies the error for an unregistered
// scheme and for a reference without one names every registered scheme in
// order and does not echo the rest of the reference.
func TestUnknownSchemeListsSortedSchemes(t *testing.T) {
	t.Parallel()

	driver, err := secretmux.New(func(sources *secretmux.Builder) {
		sources.Source("zeta", memorysecret.New()).Source("alpha", memorysecret.New())
	})

	if err != nil {
		t.Fatal(err)
	}

	store := secret.New(driver)

	for _, reference := range []string{"gcp:private-name", "private-name"} {
		_, err := store.Raw(t.Context(), reference)

		if !errors.Is(err, secretmux.ErrUnknownScheme) {
			t.Fatalf("Raw(%q) error = %v, want ErrUnknownScheme", reference, err)
		}

		if !strings.Contains(err.Error(), "alpha, zeta") || strings.Contains(err.Error(), "private-name") {
			t.Fatalf("Raw(%q) error = %q", reference, err)
		}
	}
}

// TestWritesRouteByScheme verifies Put, Update and Delete reach only the
// source registered for the scheme.
func TestWritesRouteByScheme(t *testing.T) {
	t.Parallel()

	first := memorysecret.New()
	second := memorysecret.New()
	driver, err := secretmux.New(func(sources *secretmux.Builder) {
		sources.Source("one", first).Source("two", second)
	})

	if err != nil {
		t.Fatal(err)
	}

	store := secret.New(driver)

	if _, err := store.Put(t.Context(), "one:token", []byte("a")); err != nil {
		t.Fatal(err)
	}

	if _, err := store.Update(t.Context(), "one:token", []byte("b")); err != nil {
		t.Fatal(err)
	}

	if _, err := first.Get(t.Context(), secret.Reference{Name: "token"}); err != nil {
		t.Fatalf("first source: %v", err)
	}

	if _, err := second.Get(t.Context(), secret.Reference{Name: "token"}); !errors.Is(err, secret.ErrNotFound) {
		t.Fatalf("second source error = %v, want ErrNotFound", err)
	}

	if err := store.Delete(t.Context(), "one:token"); err != nil {
		t.Fatal(err)
	}
}

// TestNameKeepsLaterColons verifies only the first colon splits.
func TestNameKeepsLaterColons(t *testing.T) {
	t.Parallel()

	memory := memorysecret.New(func(sources *memorysecret.Builder) {
		sources.Values(map[string][]byte{"app:db": []byte("value")})
	})
	driver, err := secretmux.New(func(sources *secretmux.Builder) {
		sources.Source("vault", memory)
	})

	if err != nil {
		t.Fatal(err)
	}

	data, err := secret.New(driver).Raw(t.Context(), "vault:app:db")

	if err != nil || string(data) != "value" {
		t.Fatalf("Raw = %q, %v", data, err)
	}
}

// TestVersionPassesThrough verifies the mux forwards a pinned version.
func TestVersionPassesThrough(t *testing.T) {
	t.Parallel()

	memory := memorysecret.New()
	driver, err := secretmux.New(func(sources *secretmux.Builder) {
		sources.Source("memory", memory)
	})

	if err != nil {
		t.Fatal(err)
	}

	first, err := driver.Put(t.Context(), "memory:token", []byte("old"))

	if err != nil {
		t.Fatal(err)
	}

	if _, err := driver.Put(t.Context(), "memory:token", []byte("new")); err != nil {
		t.Fatal(err)
	}

	pinned, err := driver.Get(t.Context(), secret.Reference{Name: "memory:token", Version: first.Version})

	if err != nil || string(pinned.Data) != "old" {
		t.Fatalf("pinned Get = %q, %v", pinned.Data, err)
	}
}

// TestReadOnlySourceWriteIsUnsupported verifies the source's error reaches the
// caller unchanged through the Store.
func TestReadOnlySourceWriteIsUnsupported(t *testing.T) {
	t.Parallel()

	driver, err := secretmux.New(func(sources *secretmux.Builder) {
		sources.Source("env", envsecret.New(func(string) (string, bool) { return "value", true }))
	})

	if err != nil {
		t.Fatal(err)
	}

	_, err = secret.New(driver).Put(t.Context(), "env:TOKEN", []byte("x"))

	if !errors.Is(err, secret.ErrUnsupported) {
		t.Fatalf("Put error = %v, want ErrUnsupported", err)
	}
}

// ExampleNew resolves a binding to a reference served by the mux.
func ExampleNew() {
	environment := map[string]string{
		"DATABASE_PASSWORD": "env:DB_PASSWORD",
		"DB_PASSWORD":       "fixture-password\n",
	}
	lookup := func(name string) (string, bool) {
		value, found := environment[name]

		return value, found
	}

	sources, err := secretmux.New(func(sources *secretmux.Builder) {
		sources.Source("env", envsecret.New(lookup))
	})

	if err != nil {
		fmt.Println(err)

		return
	}

	secrets := secret.New(sources, func(secrets *secret.Builder) {
		secrets.Bindings(lookup)
	})

	password, err := secrets.Get(context.Background(), secret.NewBinding[string]("DATABASE_PASSWORD"))

	fmt.Println(password, err)
	// Output: fixture-password <nil>
}
