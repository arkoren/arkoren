package natsbroadcast_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"arkoren.dev/core/service/broadcast"
	"arkoren.dev/driver/nats/natsbroadcast"

	"github.com/nats-io/nkeys"
)

// TestNewAcceptsNKeySeedMaterial exercises the public constructor with a
// deterministic native user seed, not a path to a credentials file.
func TestNewAcceptsNKeySeedMaterial(t *testing.T) {
	t.Parallel()

	pair, err := nkeys.FromRawSeed(nkeys.PrefixByteUser, bytes.Repeat([]byte{1}, 32))

	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(pair.Wipe)
	seed, err := pair.Seed()

	if err != nil {
		t.Fatal(err)
	}

	_, err = natsbroadcast.New(func(broker *natsbroadcast.Builder) {
		broker.NKeySeed(string(seed))
	})

	if err != nil {
		t.Fatal("valid seed material was rejected")
	}
}

// TestConsumeRejectsCanceledContextBeforeStartup verifies cancellation wins over
// connection state and no subscription setup is attempted.
func TestConsumeRejectsCanceledContextBeforeStartup(t *testing.T) {
	t.Parallel()

	driver, err := natsbroadcast.New()

	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	err = driver.Consume(ctx, func(context.Context, broadcast.Message) error {
		t.Error("canceled consumer invoked handler")

		return nil
	})

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Consume = %v, want canceled", err)
	}
}

// TestSubscribeRejectsCanceledContextBeforeStartup protects raw setup as well
// as envelope consumption; no canceled request can create a registration.
func TestSubscribeRejectsCanceledContextBeforeStartup(t *testing.T) {
	t.Parallel()

	driver, err := natsbroadcast.New()

	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	_, err = driver.Subscribe(ctx, "users.#", func([]byte) {
		t.Error("canceled subscription invoked handler")
	})

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Subscribe = %v, want canceled", err)
	}
}

// TestInvalidURLErrorDoesNotExposeCredentials checks constructor diagnostics,
// not merely Config formatting, for malformed credential-bearing URL input.
func TestInvalidURLErrorDoesNotExposeCredentials(t *testing.T) {
	t.Parallel()

	_, err := natsbroadcast.New(func(broker *natsbroadcast.Builder) {
		broker.URLs("nats://alice:private%zz@broker:4222")
	})

	if err == nil {
		t.Fatal("malformed URL accepted")
	}

	if strings.Contains(err.Error(), "private") {
		t.Fatal("URL credential exposed in constructor error")
	}
}

// TestMalformedSchemelessURLErrorsDoNotExposeCredentials checks the independent
// constructor-validation path. These inputs must fail before network I/O and
// retain only the public configuration-error identity, never the URL contents.
func TestMalformedSchemelessURLErrorsDoNotExposeCredentials(t *testing.T) {
	t.Parallel()

	for _, value := range []string{
		"constructor-private-user:constructor-private-password%zz@broker:4222",
		"constructor-private-token%zz@broker:4222",
		"constructor-private-user:constructor-private-password@broker:badport",
		"constructor-private-token@[broker",
		"constructor-private-token@:4222",
	} {
		_, err := natsbroadcast.New(func(broker *natsbroadcast.Builder) {
			broker.URLs(value)
		})

		if !errors.Is(err, natsbroadcast.ErrInvalidConfig) {
			t.Fatalf("New = %v, want invalid configuration", err)
		}

		var logs strings.Builder
		slog.New(slog.NewJSONHandler(&logs, nil)).ErrorContext(t.Context(), "invalid config", "error", err)
		output := fmt.Sprintf("%v %+v %#v %s", err, err, err, logs.String())

		if strings.Contains(output, "constructor-private") {
			t.Fatal("constructor diagnostics exposed credential-bearing input")
		}
	}
}

// TestInvalidNKeySeedErrorDoesNotExposeInput protects error diagnostics as well
// as Config formatting: malformed credentials must not become filenames.
func TestInvalidNKeySeedErrorDoesNotExposeInput(t *testing.T) {
	t.Parallel()

	const secret = "invalid-private-seed-material"
	_, err := natsbroadcast.New(func(broker *natsbroadcast.Builder) {
		broker.NKeySeed(secret)
	})

	if err == nil {
		t.Fatal("invalid seed accepted")
	}

	if strings.Contains(err.Error(), secret) {
		t.Fatal("credential input exposed in error")
	}
}

// TestNewFromRejectsNilConnection prevents a borrowed driver from silently
// creating a connection that its Stop method would not own.
func TestNewFromRejectsNilConnection(t *testing.T) {
	t.Parallel()

	_, err := natsbroadcast.NewFrom(nil)

	if !errors.Is(err, natsbroadcast.ErrInvalidConfig) {
		t.Fatalf("NewFrom = %v, want invalid native connection", err)
	}
}

// TestNewRejectsWildcardPrefix preserves a concrete publication namespace rather
// than letting configuration manufacture invalid native publish subjects.
func TestNewRejectsWildcardPrefix(t *testing.T) {
	t.Parallel()

	_, err := natsbroadcast.New(func(broker *natsbroadcast.Builder) {
		broker.Prefix("billing.*")
	})

	if err == nil {
		t.Fatal("wildcard prefix accepted")
	}
}
