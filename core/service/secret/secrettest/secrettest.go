package secrettest

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"strings"
	"testing"

	"arkoren.dev/core/service/secret"
)

// defaultMissingVersion is a version no driver has issued, in the numeric form
// used by memory, Vault and Google Secret Manager.
const defaultMissingVersion = "999999"

// config holds the settings produced by [Option] values.
type config struct {
	// text restricts payloads to UTF-8 text.
	text bool

	// jsonObject restricts payloads to compact, key-sorted JSON objects.
	jsonObject bool

	// missingVersion is a well-formed version that was never issued.
	missingVersion string
}

// Option declares a capability of the driver under test. Options never relax
// the contract's semantics.
type Option func(*config)

// WithTextValues declares that the backend stores UTF-8 text only, so the
// suite does not write arbitrary bytes.
func WithTextValues() Option {
	return func(config *config) {
		config.text = true
	}
}

// WithJSONObjectValues declares that the backend stores JSON objects and
// returns them re-encoded compactly with sorted keys, so the suite writes only
// such payloads. It implies [WithTextValues].
func WithJSONObjectValues() Option {
	return func(config *config) {
		config.text, config.jsonObject = true, true
	}
}

// WithMissingVersion supplies a version identifier that is well formed for the
// backend but was never issued, for example a zero UUID. The default is
// "999999".
func WithMissingVersion(version string) Option {
	return func(config *config) {
		config.missingVersion = version
	}
}

// suite carries the state shared by the subtests of one [Run] call.
type suite struct {
	// newDriver creates the driver under test.
	newDriver func(t *testing.T) secret.Driver

	// config holds the declared options.
	config config
}

// Run executes the secret conformance suite against drivers produced by
// newDriver. The factory is called once per subtest. Run marks every subtest
// parallel and gives each its own secret name, so a shared backend works.
func Run(t *testing.T, newDriver func(t *testing.T) secret.Driver, opts ...Option) {
	t.Helper()

	settings := config{missingVersion: defaultMissingVersion}

	for _, opt := range opts {
		opt(&settings)
	}

	s := suite{newDriver: newDriver, config: settings}

	for _, group := range []struct {
		// name labels the conformance group in test output.
		name string
		// fn runs the group with its shared suite configuration.
		fn func(*testing.T, suite)
	}{
		{"Get", get},
		{"Put", creating},
		{"Versions", versions},
		{"Update", update},
		{"Delete", deletion},
		{"Isolation", isolation},
		{"Cancellation", cancellation},
	} {
		t.Run(group.name, func(t *testing.T) {
			group.fn(t, s)
		})
	}
}

// env is what one subtest receives: a driver and a private secret name.
type env struct {
	// driver is the driver under test.
	driver secret.Driver

	// name is unique to the subtest.
	name string

	// config holds the declared options.
	config config
}

// payload returns a distinct payload valid for the backend.
func (env env) payload(n int) []byte {
	if env.config.jsonObject {
		return fmt.Appendf(nil, `{"value":"v%d"}`, n)
	}

	return fmt.Appendf(nil, "value-%d", n)
}

// sub runs fn as a parallel subtest with a fresh driver and a unique name, and
// deletes the secret afterwards when it still exists.
func (suite suite) sub(t *testing.T, name string, fn func(t *testing.T, e env)) {
	t.Helper()

	t.Run(name, func(t *testing.T) {
		t.Parallel()

		e := env{
			driver: suite.newDriver(t),
			name:   "conformance-" + strings.ToLower(rand.Text()),
			config: suite.config,
		}
		t.Cleanup(func() {
			err := e.driver.Delete(context.WithoutCancel(t.Context()), e.name)

			if err != nil && !errors.Is(err, secret.ErrNotFound) {
				t.Errorf("cleanup Delete(%q) = %v", e.name, err)
			}
		})

		fn(t, e)
	})
}

// must fails the test immediately when err is not nil.
func must(t *testing.T, op string, err error) {
	t.Helper()

	if err != nil {
		t.Fatalf("%s: %v", op, err)
	}
}

// write writes data as a new version of the subtest's secret.
func write(t *testing.T, e env, data []byte) secret.Value {
	t.Helper()

	value, err := e.driver.Put(t.Context(), e.name, data)

	must(t, "Put", err)

	return value
}

// current reads the current value of the subtest's secret.
func current(t *testing.T, e env) secret.Value {
	t.Helper()

	value, err := e.driver.Get(t.Context(), secret.Reference{Name: e.name})

	must(t, "Get", err)

	return value
}

// wantData asserts that value carries want.
func wantData(t *testing.T, op string, value secret.Value, want []byte) {
	t.Helper()

	if !bytes.Equal(value.Data, want) {
		t.Errorf("%s data = %q, want %q", op, value.Data, want)
	}
}

// wantNotFound asserts that err wraps [secret.ErrNotFound].
func wantNotFound(t *testing.T, op string, err error) {
	t.Helper()

	if !errors.Is(err, secret.ErrNotFound) {
		t.Errorf("%s error = %v, want ErrNotFound", op, err)
	}
}

// get covers reading.
func get(t *testing.T, s suite) {
	s.sub(t, "MissingSecretIsNotFound", func(t *testing.T, e env) {
		_, err := e.driver.Get(t.Context(), secret.Reference{Name: e.name})

		wantNotFound(t, "Get", err)
	})
	s.sub(t, "MissingSecretWithVersionIsNotFound", func(t *testing.T, e env) {
		_, err := e.driver.Get(t.Context(), secret.Reference{Name: e.name, Version: e.config.missingVersion})

		wantNotFound(t, "Get", err)
	})
	s.sub(t, "UnknownVersionIsNotFound", func(t *testing.T, e env) {
		write(t, e, e.payload(1))

		_, err := e.driver.Get(t.Context(), secret.Reference{Name: e.name, Version: e.config.missingVersion})

		wantNotFound(t, "Get", err)
	})
}

// creating covers Put.
func creating(t *testing.T, s suite) {
	s.sub(t, "CreatesSecret", func(t *testing.T, e env) {
		written := write(t, e, e.payload(1))

		wantData(t, "Put", written, e.payload(1))

		if written.Version == "" {
			t.Error("Put returned an empty Version")
		}

		got := current(t, e)

		wantData(t, "Get", got, e.payload(1))

		if got.Version != written.Version {
			t.Errorf("Get Version = %q, Put Version = %q, want equal", got.Version, written.Version)
		}
	})
	s.sub(t, "SetOnExistingSecretWritesNewCurrentVersion", func(t *testing.T, e env) {
		first := write(t, e, e.payload(1))
		second := write(t, e, e.payload(2))

		if first.Version == second.Version {
			t.Errorf("both writes returned Version %q, want distinct versions", first.Version)
		}

		got := current(t, e)

		wantData(t, "Get", got, e.payload(2))

		if got.Version != second.Version {
			t.Errorf("current Version = %q, want %q", got.Version, second.Version)
		}
	})

	if !s.config.text {
		s.sub(t, "StoresArbitraryBytes", func(t *testing.T, e env) {
			want := []byte{0, 1, 0xff, '\n', 0x80}
			write(t, e, want)

			wantData(t, "Get", current(t, e), want)
		})
	}
}

// versions checks concrete-version identity and unchanged payloads within one
// existing logical secret. Reads follow later Put and Update calls, before
// deletion; recreation and possible selector reuse are not portable assertions.
func versions(t *testing.T, s suite) {
	s.sub(t, "GetByVersionReturnsThatVersion", func(t *testing.T, e env) {
		first := write(t, e, e.payload(1))
		second := write(t, e, e.payload(2))

		for _, want := range []struct {
			// version identifies the historical secret value being checked.
			version secret.Value
			// data is the expected payload for the selected secret version.
			data []byte
		}{{first, e.payload(1)}, {second, e.payload(2)}} {
			got, err := e.driver.Get(t.Context(), secret.Reference{Name: e.name, Version: want.version.Version})

			must(t, "Get by version", err)
			wantData(t, "Get by version "+want.version.Version, got, want.data)

			if got.Version != want.version.Version {
				t.Errorf("Get Version = %q, want %q", got.Version, want.version.Version)
			}
		}
	})
	s.sub(t, "EmptyVersionSelectsCurrent", func(t *testing.T, e env) {
		write(t, e, e.payload(1))
		write(t, e, e.payload(2))

		wantData(t, "Get", current(t, e), e.payload(2))
	})
	s.sub(t, "UpdatePreservesOlderVersions", func(t *testing.T, e env) {
		first := write(t, e, e.payload(1))

		updated, err := e.driver.Update(t.Context(), e.name, e.payload(2))

		must(t, "Update", err)

		old, err := e.driver.Get(t.Context(), secret.Reference{Name: e.name, Version: first.Version})

		must(t, "Get first version", err)
		wantData(t, "first version", old, e.payload(1))

		if updated.Version == first.Version {
			t.Errorf("Update reused Version %q", first.Version)
		}
	})
}

// update covers Update.
func update(t *testing.T, s suite) {
	s.sub(t, "WritesNewCurrentVersion", func(t *testing.T, e env) {
		write(t, e, e.payload(1))

		updated, err := e.driver.Update(t.Context(), e.name, e.payload(2))

		must(t, "Update", err)
		wantData(t, "Update", updated, e.payload(2))

		got := current(t, e)

		wantData(t, "Get", got, e.payload(2))

		if got.Version != updated.Version || got.Version == "" {
			t.Errorf("current Version = %q, Update Version = %q, want equal and non-empty", got.Version, updated.Version)
		}
	})
	s.sub(t, "MissingSecretIsNotFoundAndNotCreated", func(t *testing.T, e env) {
		_, err := e.driver.Update(t.Context(), e.name, e.payload(1))

		wantNotFound(t, "Update", err)

		_, err = e.driver.Get(t.Context(), secret.Reference{Name: e.name})

		wantNotFound(t, "Get after failed Update", err)
	})
}

// deletion checks absence after Delete without recovering or recreating the
// secret. It does not assert that a deleted name stays unavailable forever.
func deletion(t *testing.T, s suite) {
	s.sub(t, "RemovesSecretAndAllVersions", func(t *testing.T, e env) {
		first := write(t, e, e.payload(1))
		write(t, e, e.payload(2))

		must(t, "Delete", e.driver.Delete(t.Context(), e.name))

		_, err := e.driver.Get(t.Context(), secret.Reference{Name: e.name})

		wantNotFound(t, "Get current", err)

		_, err = e.driver.Get(t.Context(), secret.Reference{Name: e.name, Version: first.Version})

		wantNotFound(t, "Get first version", err)
	})
	s.sub(t, "MissingSecretIsNotFound", func(t *testing.T, e env) {
		wantNotFound(t, "Delete", e.driver.Delete(t.Context(), e.name))
	})
	s.sub(t, "UpdateAfterDeleteIsNotFound", func(t *testing.T, e env) {
		write(t, e, e.payload(1))

		must(t, "Delete", e.driver.Delete(t.Context(), e.name))

		_, err := e.driver.Update(t.Context(), e.name, e.payload(2))

		wantNotFound(t, "Update", err)
	})
}

// isolation covers copy semantics and independence between secrets.
func isolation(t *testing.T, s suite) {
	s.sub(t, "SetDoesNotRetainInput", func(t *testing.T, e env) {
		data := e.payload(1)
		write(t, e, data)

		data[len(data)-2] = 'X'

		wantData(t, "Get", current(t, e), e.payload(1))
	})
	s.sub(t, "ReturnedDataIsIndependent", func(t *testing.T, e env) {
		written := write(t, e, e.payload(1))
		written.Data[0] = 'X'
		got := current(t, e)
		got.Data[0] = 'X'

		wantData(t, "Get", current(t, e), e.payload(1))
	})
	s.sub(t, "SecretsAreIndependent", func(t *testing.T, e env) {
		write(t, e, e.payload(1))

		other := e.name + "-other"
		t.Cleanup(func() {
			err := e.driver.Delete(context.WithoutCancel(t.Context()), other)

			if err != nil && !errors.Is(err, secret.ErrNotFound) {
				t.Errorf("cleanup Delete(%q) = %v", other, err)
			}
		})

		if _, err := e.driver.Put(t.Context(), other, e.payload(2)); err != nil {
			t.Fatalf("Put other: %v", err)
		}

		wantData(t, "Get", current(t, e), e.payload(1))
		must(t, "Delete other", e.driver.Delete(t.Context(), other))
		wantData(t, "Get after deleting other", current(t, e), e.payload(1))
	})
}

// cancellation covers operations on an already canceled context.
func cancellation(t *testing.T, s suite) {
	s.sub(t, "OperationsFailWithContextError", func(t *testing.T, e env) {
		write(t, e, e.payload(1))

		ctx, cancel := context.WithCancel(t.Context())
		cancel()

		ops := map[string]func() error{
			"Get": func() error {
				_, err := e.driver.Get(ctx, secret.Reference{Name: e.name})

				return err
			},
			"Put": func() error {
				_, err := e.driver.Put(ctx, e.name, e.payload(2))

				return err
			},
			"Update": func() error {
				_, err := e.driver.Update(ctx, e.name, e.payload(2))

				return err
			},
			"Delete": func() error {
				return e.driver.Delete(ctx, e.name)
			},
		}

		for name, op := range ops {
			if err := op(); !errors.Is(err, context.Canceled) {
				t.Errorf("%s error = %v, want context.Canceled", name, err)
			}
		}

		wantData(t, "Get", current(t, e), e.payload(1))
	})
}
