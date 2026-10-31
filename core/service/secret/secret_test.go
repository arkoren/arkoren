package secret_test

import (
	"context"
	"testing"

	"arkoren.dev/core/service/secret"
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

// TestSecretsStringReturnsRawString verifies string secret retrieval.
func TestSecretsStringReturnsRawString(t *testing.T) {
	t.Parallel()

	store := secret.New(secretDriver{
		value: []byte("secret"),
	})

	value, err := store.String(context.Background(), "password")

	if err != nil || value != "secret" {
		t.Fatalf("value = %q, err = %v", value, err)
	}
}
