package workflow_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"arkoren.dev/core/service/workflow"
)

// TestRegistryAcceptsNullInWhitespaceEnvelope keeps JSON null distinct from a
// missing envelope member when a transport reformats a registered step's input.
func TestRegistryAcceptsNullInWhitespaceEnvelope(t *testing.T) {
	t.Parallel()

	step := workflow.NewStep("nullable", func(_ context.Context, input *string) (string, error) {
		if input != nil {
			t.Error("null was not decoded as a nil pointer")
		}

		return "accepted", nil
	})

	registry, err := workflow.Handlers(step)

	if err != nil {
		t.Fatal(err)
	}

	handler, ok := registry.Step("nullable")

	if !ok {
		t.Fatal("step was not registered")
	}

	if _, err := handler.Handle(t.Context(), []byte(`{ "v": null }`)); err != nil {
		t.Fatalf("valid null envelope rejected: %v", err)
	}
}

// TestCodecAndEnvelope checks the value-envelope wire shape, typed decoding,
// and empty-input behavior without involving a workflow engine.
func TestCodecAndEnvelope(t *testing.T) {
	type payload struct {
		// Name exercises a string-valued member in the encoded envelope.
		Name string

		// N exercises a numeric member and empty-decode destination retention.
		N int
	}

	raw, err := workflow.EncodeValue(workflow.JSON{}, payload{
		Name: "a",
		N:    2,
	})

	if err != nil {
		t.Fatal(err)
	}

	if !strings.HasPrefix(string(raw), `{"v":`) {
		t.Fatalf("envelope = %s; want object with v", raw)
	}

	var got payload

	if err := workflow.DecodeValue(nil, raw, &got); err != nil || got != (payload{
		Name: "a",
		N:    2,
	}) {
		t.Fatalf("DecodeValue = %+v, %v", got, err)
	}

	got = payload{
		N: 9,
	}

	if err := workflow.DecodeValue(nil, nil, &got); err != nil || got.N != 9 {
		t.Fatalf("empty DecodeValue changed value: %+v, %v", got, err)
	}

	if err := workflow.DecodeValue(nil, []byte("nonsense"), &got); err == nil {
		t.Fatal("DecodeValue of garbage succeeded")
	}
}

// TestErrorEnvelope checks that error encoding preserves sentinel kinds,
// custom messages, and permanent flags without inventing those properties.
func TestErrorEnvelope(t *testing.T) {
	if workflow.EncodeError(nil) != nil || workflow.DecodeError(nil) != nil {
		t.Fatal("nil error must round-trip as nil")
	}

	for _, sentinel := range []error{
		workflow.ErrCanceled, workflow.ErrTimeout, workflow.ErrNotFound,
		workflow.ErrAlreadyStarted, workflow.ErrUnsupported,
	} {
		wrapped := errors.Join(errors.New("context"), sentinel)

		got := workflow.DecodeError(workflow.EncodeError(wrapped))

		if !errors.Is(got, sentinel) {
			t.Errorf("round trip of %v lost its kind: %v", sentinel, got)
		}
	}

	custom := workflow.NewError("custom", "boom")
	got := workflow.DecodeError(workflow.EncodeError(custom))

	var typed *workflow.Error

	if !errors.Is(got, workflow.NewError("custom", "")) || !errors.As(got, &typed) || typed.Message != "boom" {
		t.Fatalf("custom round trip = %#v", got)
	}

	if errors.Is(got, workflow.ErrTimeout) {
		t.Fatal("custom kind matches ErrTimeout")
	}

	plain := workflow.DecodeError(workflow.EncodeError(errors.New("plain")))

	if plain.Error() != "plain" || errors.Is(plain, workflow.NewError("", "")) {
		t.Fatalf("plain error = %v", plain)
	}

	permanent := workflow.DecodeError(workflow.EncodeError(workflow.Permanent(errors.New("stop"))))

	if !errors.Is(permanent, workflow.ErrPermanent) {
		t.Fatal("permanent flag lost")
	}

	if errors.Is(workflow.DecodeError(workflow.EncodeError(errors.New("x"))), workflow.ErrPermanent) {
		t.Fatal("non-permanent error became permanent")
	}

	if workflow.DecodeError([]byte("not json")).Error() != "not json" {
		t.Fatal("non-envelope data must become the message")
	}
}

// TestDecodeValueRejectsNonEnvelope checks that otherwise valid JSON cannot
// bypass the required workflow value-envelope member.
func TestDecodeValueRejectsNonEnvelope(t *testing.T) {
	var v int

	if err := workflow.DecodeValue(nil, []byte(`{"x":1}`), &v); err == nil {
		t.Fatal("DecodeValue of an object without v returned no error")
	}

	if err := workflow.DecodeValue(nil, []byte(`5`), &v); err == nil {
		t.Fatal("DecodeValue of a bare number returned no error")
	}
}

// TestEncodeErrorContextKinds checks translation of standard context errors
// into the workflow sentinels preserved by engine error envelopes.
func TestEncodeErrorContextKinds(t *testing.T) {
	if err := workflow.DecodeError(workflow.EncodeError(context.Canceled)); !errors.Is(err, workflow.ErrCanceled) {
		t.Fatalf("canceled -> %v; want ErrCanceled", err)
	}

	if err := workflow.DecodeError(workflow.EncodeError(context.DeadlineExceeded)); !errors.Is(err, workflow.ErrTimeout) {
		t.Fatalf("deadline -> %v; want ErrTimeout", err)
	}
}
