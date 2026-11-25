package envsecret_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"

	"arkoren.dev/core/service/secret"
	"arkoren.dev/core/service/secret/envsecret"
	"arkoren.dev/core/service/secret/secrettest"
)

// variables is a concurrency-safe fake environment shared by one test.
type variables struct {
	// mutex guards values.
	mutex sync.Mutex

	// values maps variable names to their values.
	values map[string]string

	// next numbers the variables seeded by the conformance suite.
	next atomic.Int64
}

// lookup implements the function envsecret.New expects.
func (env *variables) lookup(name string) (string, bool) {
	env.mutex.Lock()
	defer env.mutex.Unlock()

	value, found := env.values[name]

	return value, found
}

// set stores one variable.
func (env *variables) set(name string, value string) {
	env.mutex.Lock()
	defer env.mutex.Unlock()

	env.values[name] = value
}

// TestConformance runs the secret conformance suite for a read-only text source.
func TestConformance(t *testing.T) {
	t.Parallel()

	env := &variables{values: map[string]string{}}

	secrettest.Run(t, func(*testing.T) secret.Driver {
		return envsecret.New(env.lookup)
	}, secrettest.WithReadOnly(), secrettest.WithTextOnly(), secrettest.WithSeed(func(t testing.TB, data []byte) string {
		name := fmt.Sprintf("ENVSECRET_TEST_%d", env.next.Add(1))
		env.set(name, string(data))

		return name
	}))
}

// TestGetUnsetIsNotFound verifies an unset variable wraps secret.ErrNotFound.
func TestGetUnsetIsNotFound(t *testing.T) {
	t.Parallel()

	env := &variables{values: map[string]string{}}

	_, err := envsecret.New(env.lookup).Get(t.Context(), secret.Reference{Name: "UNSET"})

	if !errors.Is(err, secret.ErrNotFound) {
		t.Fatalf("Get error = %v, want ErrNotFound", err)
	}
}

// TestGetEmptyValueIsPresent verifies a variable set to "" is a present secret.
func TestGetEmptyValueIsPresent(t *testing.T) {
	t.Parallel()

	env := &variables{values: map[string]string{"EMPTY": ""}}

	value, err := envsecret.New(env.lookup).Get(t.Context(), secret.Reference{Name: "EMPTY"})

	if err != nil || len(value.Data) != 0 {
		t.Fatalf("Get = %q, %v", value.Data, err)
	}
}

// TestGetRejectsInvalidNames verifies names outside the POSIX grammar fail
// without reaching the lookup and are not reported as missing.
func TestGetRejectsInvalidNames(t *testing.T) {
	t.Parallel()

	driver := envsecret.New(func(string) (string, bool) {
		t.Error("lookup called for an invalid name")

		return "", false
	})

	for _, name := range []string{"", "1ABC", "A-B", "A B", "A=B", "A\x00B", "ÄB", "a/b"} {
		_, err := driver.Get(t.Context(), secret.Reference{Name: name})

		if err == nil || errors.Is(err, secret.ErrNotFound) {
			t.Errorf("Get(%q) error = %v, want invalid name error", name, err)
		}
	}
}

// TestGetAcceptsPOSIXNames verifies the edges of the accepted grammar.
func TestGetAcceptsPOSIXNames(t *testing.T) {
	t.Parallel()

	env := &variables{values: map[string]string{"_": "a", "a1": "b", "A_B_9": "c"}}

	for _, name := range []string{"_", "a1", "A_B_9"} {
		if _, err := envsecret.New(env.lookup).Get(t.Context(), secret.Reference{Name: name}); err != nil {
			t.Errorf("Get(%q) = %v", name, err)
		}
	}
}

// TestGetRejectsInvalidUTF8 verifies a non-UTF-8 value is an error that does
// not echo the value.
func TestGetRejectsInvalidUTF8(t *testing.T) {
	t.Parallel()

	env := &variables{values: map[string]string{"BINARY": "secret\xff"}}

	_, err := envsecret.New(env.lookup).Get(t.Context(), secret.Reference{Name: "BINARY"})

	if err == nil || errors.Is(err, secret.ErrNotFound) {
		t.Fatalf("Get error = %v, want UTF-8 error", err)
	}
}

// TestWritesPreferCanceledContext verifies a canceled context outranks the
// unsupported error, as the Driver contract requires.
func TestWritesPreferCanceledContext(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	_, err := envsecret.New(nil).Put(ctx, "A", nil)

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Put error = %v, want context.Canceled", err)
	}
}

// ExampleNew reads a secret through a fixed environment.
func ExampleNew() {
	driver := envsecret.New(func(name string) (string, bool) {
		return "fixture-token", name == "API_TOKEN"
	})

	value, err := driver.Get(context.Background(), secret.Reference{Name: "API_TOKEN"})

	fmt.Println(string(value.Data), err)
	// Output: fixture-token <nil>
}
