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

// TestSecretLiteralName checks provider identities are not normalized.
func TestSecretLiteralName(t *testing.T) {
	t.Parallel()

	definition := secret.NewSecret[map[string]string]("literal / name")

	if definition.Name() != "literal / name" || definition.Err() != nil {
		t.Fatal("definition changed its literal name")
	}
}

// TestSecretInvalid checks empty and zero definitions fail without I/O.
// A nil driver makes an accidental provider call fail the test.
func TestSecretInvalid(t *testing.T) {
	t.Parallel()

	store := secret.New(nil)

	for _, definition := range []secret.Secret[int]{{}, secret.NewSecret[int]("")} {
		if !errors.Is(definition.Err(), secret.ErrInvalidSecret) {
			t.Fatal("invalid definition accepted")
		}

		if _, err := store.Get(t.Context(), definition); !errors.Is(err, secret.ErrInvalidSecret) {
			t.Fatalf("Get error = %v", err)
		}
	}
}

// TestSecretCancellationPrecedesValidation checks cancellation wins even
// for a zero definition, without making a provider request.
func TestSecretCancellationPrecedesValidation(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	_, err := secret.New(nil).Get(ctx, secret.Secret[int]{})

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Get error = %v", err)
	}
}

// TestSecretReadsCurrentAfterRotation checks shared definitions do not
// pin a version or cache values, while raw version selection remains available.
func TestSecretReadsCurrentAfterRotation(t *testing.T) {
	t.Parallel()

	store := secret.New(memorysecret.New())
	definition := secret.NewSecret[map[string]string]("database")

	first, err := store.Put(t.Context(), definition.Name(), []byte(`{"password":"old"}`))

	if err != nil {
		t.Fatal(err)
	}

	if _, err := store.Update(t.Context(), definition.Name(), []byte(`{"password":"new"}`)); err != nil {
		t.Fatal(err)
	}

	value, err := store.Get(t.Context(), definition)

	if err != nil || value["password"] != "new" {
		t.Fatalf("current = %v, error = %v", value, err)
	}

	old, err := store.Driver().Get(t.Context(), secret.Reference{Name: definition.Name(), Version: first.Version})

	if err != nil || string(old.Data) != `{"password":"old"}` {
		t.Fatalf("pinned = %s, error = %v", old.Data, err)
	}
}

// TestSecretMissingPreservesCause checks typed reads keep provider error
// identity so callers can use errors.Is just as they do for raw reads.
func TestSecretMissingPreservesCause(t *testing.T) {
	t.Parallel()

	_, err := secret.New(memorysecret.New()).Get(t.Context(), secret.NewSecret[int]("missing"))

	if !errors.Is(err, secret.ErrNotFound) {
		t.Fatalf("Get error = %v", err)
	}
}

// TestSecretCompatibleDriftStillDecodes documents that definitions are
// not schemas: an absent member still decodes to the Go zero value.
func TestSecretCompatibleDriftStillDecodes(t *testing.T) {
	t.Parallel()

	store := secret.New(secretDriver{value: []byte(`{}`)})

	value, err := store.Get(t.Context(), secret.NewSecret[map[string]string]("database"))

	if err != nil || value["password"] != "" {
		t.Fatalf("decoded = %v, error = %v", value, err)
	}
}

// TestSecretDecodePreservesCause checks the decoder error stays available
// through wrapping; the definition's diagnostic redaction does not sanitize it.
func TestSecretDecodePreservesCause(t *testing.T) {
	t.Parallel()

	store := secret.New(secretDriver{value: []byte(`"not a number"`)})

	_, err := store.Get(t.Context(), secret.NewSecret[int]("private-secret"))

	if err == nil || errors.Unwrap(err) == nil {
		t.Fatalf("decode error = %v", err)
	}
}

// TestSecretFormattingRedactsIdentity covers detailed, quoted, and
// structured formatting, leaving Name as the intentional escape hatch.
func TestSecretFormattingRedactsIdentity(t *testing.T) {
	t.Parallel()

	definition := secret.NewSecret[int]("private-secret")
	text := fmt.Sprintf("%v %+v %#v %s %q %x", definition, definition, definition, definition, definition, definition)

	if strings.Contains(text, "private") || strings.Contains(slog.AnyValue(definition).Resolve().String(), "private") {
		t.Fatal("formatting exposed secret identity")
	}

	if fmt.Sprint(definition) != "secret.Secret([redacted])" {
		t.Fatal("formatting uses an unexpected definition label")
	}

	if slog.AnyValue(definition).Resolve().String() != "secret.Secret([redacted])" {
		t.Fatal("structured logging uses an unexpected definition label")
	}
}

// TestSecretRetypingRejected checks assignment, conversion, and explicit
// Get type arguments cannot change a shared definition's decoding type.
func TestSecretRetypingRejected(t *testing.T) {
	t.Parallel()

	command := exec.CommandContext(t.Context(), "go", "test", "-gcflags=-e", "./testdata/retyping")

	output, err := command.CombinedOutput()

	if err == nil {
		t.Fatal("definition retyping compiled")
	}

	for _, diagnostic := range []string{
		"cannot convert secret.NewSecret[string]",
		"cannot use secret.NewSecret[string]",
		"argument to store.Get[int]",
	} {
		if !strings.Contains(string(output), diagnostic) {
			t.Fatalf("missing %q diagnostic: %s", diagnostic, output)
		}
	}
}
