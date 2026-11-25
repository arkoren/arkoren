package secret_test

import (
	"bytes"
	"context"
	"errors"
	"net/netip"
	"testing"

	"arkoren.dev/core/service/secret"
	"arkoren.dev/core/service/secret/memorysecret"
)

// secretDriver returns a configured test secret value.
type secretDriver struct {
	// value contains the raw secret bytes returned by Get.
	value []byte
}

// Get returns the configured test secret value.
func (driver secretDriver) Get(context.Context, secret.Reference) (secret.Value, error) {
	return secret.Value{Data: driver.value, Version: "1"}, nil
}

// Put accepts test secret writes.
func (secretDriver secretDriver) Put(context.Context, string, []byte) (secret.Value, error) {
	return secret.Value{}, nil
}

// Update accepts test secret updates.
func (secretDriver secretDriver) Update(context.Context, string, []byte) (secret.Value, error) {
	return secret.Value{}, nil
}

// Delete accepts test secret deletions.
func (secretDriver secretDriver) Delete(context.Context, string) error {
	return nil
}

// TestSecretsGetDecodesJSON verifies typed secret decoding.
func TestSecretsGetDecodesJSON(t *testing.T) {
	t.Parallel()

	store := secret.New(secretDriver{
		value: []byte(`{"name":"arkoren"}`),
	})
	definition := secret.NewSecret[struct {
		// Name is the JSON field used to verify typed value serialization.
		Name string `json:"name"`
	}]("app")

	value, err := store.Get(context.Background(), definition)

	if err != nil || value.Name != "arkoren" {
		t.Fatalf("value = %#v, err = %v", value, err)
	}
}

// TestGetStringDropsOneTrailingNewline verifies the single-terminator rule for
// both line endings, and that further newlines are payload.
func TestGetStringDropsOneTrailingNewline(t *testing.T) {
	t.Parallel()

	for payload, want := range map[string]string{
		"secret":     "secret",
		"secret\n":   "secret",
		"secret\r\n": "secret",
		"secret\n\n": "secret\n",
		"secret\r":   "secret\r",
	} {
		store := secret.New(secretDriver{value: []byte(payload)})

		value, err := store.Get(t.Context(), secret.NewSecret[string]("file:token"))

		if err != nil || value != want {
			t.Errorf("Get(%q) = %q, %v, want %q", payload, value, err, want)
		}
	}
}

// TestGetStringRejectsInvalidUTF8 verifies a binary payload is an error, not a
// silently corrupted string, and that the error does not echo the payload.
func TestGetStringRejectsInvalidUTF8(t *testing.T) {
	t.Parallel()

	store := secret.New(secretDriver{value: []byte{'p', 'w', 0xff, 0xfe}})

	_, err := store.Get(t.Context(), secret.NewSecret[string]("file:token"))

	if err == nil {
		t.Fatal("invalid UTF-8 decoded into a string")
	}

	if bytes.Contains([]byte(err.Error()), []byte{0xff}) {
		t.Fatalf("error echoes payload: %q", err)
	}
}

// TestGetBytesReturnsUnmodifiedCopy verifies binary payloads, including a
// trailing newline, survive untouched and are detached from the driver.
func TestGetBytesReturnsUnmodifiedCopy(t *testing.T) {
	t.Parallel()

	payload := []byte{0, 1, 0xff, '\n'}
	store := secret.New(secretDriver{value: payload})

	value, err := store.Get(t.Context(), secret.NewSecret[[]byte]("file:key"))

	if err != nil || !bytes.Equal(value, []byte{0, 1, 0xff, '\n'}) {
		t.Fatalf("Get = %v, %v", value, err)
	}
}

// TestGetTextUnmarshalerReceivesTrimmedText verifies types implementing
// encoding.TextUnmarshaler decode from text without the trailing newline.
func TestGetTextUnmarshalerReceivesTrimmedText(t *testing.T) {
	t.Parallel()

	store := secret.New(secretDriver{value: []byte("127.0.0.1\n")})

	value, err := store.Get(t.Context(), secret.NewSecret[netip.Addr]("file:addr"))

	if err != nil || value != netip.MustParseAddr("127.0.0.1") {
		t.Fatalf("Get = %v, %v", value, err)
	}
}

// TestGetTextUnmarshalerError verifies an UnmarshalText failure is returned.
func TestGetTextUnmarshalerError(t *testing.T) {
	t.Parallel()

	store := secret.New(secretDriver{value: []byte("not an address")})

	if _, err := store.Get(t.Context(), secret.NewSecret[netip.Addr]("file:addr")); err == nil {
		t.Fatal("invalid text decoded")
	}
}

// TestRawKeepsPayloadUntouched verifies Raw applies no decoding.
func TestRawKeepsPayloadUntouched(t *testing.T) {
	t.Parallel()

	store := secret.New(secretDriver{value: []byte("secret\r\n")})

	data, err := store.Raw(t.Context(), "file:token")

	if err != nil || string(data) != "secret\r\n" {
		t.Fatalf("Raw = %q, %v", data, err)
	}
}

// TestBindingReadsResolvedReference verifies a binding reads the reference its
// key resolves to, at each Get.
func TestBindingReadsResolvedReference(t *testing.T) {
	t.Parallel()

	driver := memorysecret.New(func(secrets *memorysecret.Builder) {
		secrets.Values(map[string][]byte{"aws:prod/db": []byte("old"), "vault:app/db": []byte("new")})
	})
	target := "aws:prod/db"
	store := secret.New(driver, func(secrets *secret.Builder) {
		secrets.Bindings(func(key string) (string, bool) {
			return target, key == "DATABASE_PASSWORD"
		})
	})
	password := secret.NewBinding[string]("DATABASE_PASSWORD")

	first, err := store.Get(t.Context(), password)

	if err != nil || first != "old" {
		t.Fatalf("first Get = %q, %v", first, err)
	}

	target = "vault:app/db"

	second, err := store.Get(t.Context(), password)

	if err != nil || second != "new" {
		t.Fatalf("second Get = %q, %v", second, err)
	}
}

// TestBindingMustBeBound verifies every unbound shape fails with ErrUnbound
// before the driver is reached.
func TestBindingMustBeBound(t *testing.T) {
	t.Parallel()

	password := secret.NewBinding[string]("DATABASE_PASSWORD")
	lookups := map[string]func(string) (string, bool){
		"missing lookup": nil,
		"unset key":      func(string) (string, bool) { return "", false },
		"empty value":    func(string) (string, bool) { return "", true },
	}

	for name, lookup := range lookups {
		store := secret.New(nil, func(secrets *secret.Builder) {
			secrets.Bindings(lookup)
		})

		if _, err := store.Get(t.Context(), password); !errors.Is(err, secret.ErrUnbound) {
			t.Errorf("%s: Get error = %v, want ErrUnbound", name, err)
		}
	}
}

// TestBindingEmptyKeyIsInvalid verifies an empty key is an invalid definition.
func TestBindingEmptyKeyIsInvalid(t *testing.T) {
	t.Parallel()

	if !errors.Is(secret.NewBinding[string]("").Err(), secret.ErrInvalidSecret) {
		t.Fatal("empty binding key accepted")
	}
}
