package hatchetworkflow

import (
	"encoding/json/v2"
	"errors"
	"testing"

	"arkoren.dev/core/service/workflow"
)

// TestEnvelopeFollowedByText verifies that native text appended after an
// application error envelope does not prevent error recovery.
func TestEnvelopeFollowedByText(t *testing.T) {
	t.Parallel()

	raw := string(workflow.EncodeError(workflow.NewError("k", "m")))

	err := failure("x " + raw + " trailing {not json}")

	var decoded *workflow.Error

	if !errors.As(err, &decoded) || decoded.Kind != "k" {
		t.Fatalf("failure() = %#v", err)
	}
}

// envelopeTexts returns an engine failure message embedding an error envelope
// as plain text and the same kind of message with the envelope JSON-escaped.
func envelopeTexts(t *testing.T) (plain, escaped string) {
	t.Helper()

	raw := string(workflow.EncodeError(workflow.NewError("k", `say "hi"`)))
	quoted, err := json.Marshal("engine said: " + raw)

	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}

	return "prefix " + raw, string(quoted[1 : len(quoted)-1])
}

// requireEnvelopeFailure verifies failure extracts the envelope from text.
func requireEnvelopeFailure(t *testing.T, text string) {
	t.Helper()

	err := failure(text)

	var decoded *workflow.Error

	if !errors.As(err, &decoded) || decoded.Kind != "k" || decoded.Message != `say "hi"` {
		t.Errorf("failure() = %#v", err)
	}
}

// TestEnvelopeExtractionPlain verifies that an unescaped error envelope is
// extracted from surrounding engine failure text.
func TestEnvelopeExtractionPlain(t *testing.T) {
	t.Parallel()

	plain, _ := envelopeTexts(t)

	requireEnvelopeFailure(t, plain)
}

// TestEnvelopeExtractionEscaped verifies that an escaped error envelope is
// recovered without changing its application error kind.
func TestEnvelopeExtractionEscaped(t *testing.T) {
	t.Parallel()

	_, escaped := envelopeTexts(t)

	requireEnvelopeFailure(t, escaped)
}

// TestEnvelopeExtractionPlainEngineFailure verifies that ordinary engine
// failure text does not masquerade as a known application failure.
func TestEnvelopeExtractionPlainEngineFailure(t *testing.T) {
	t.Parallel()

	if err := failure("plain engine failure"); errors.Is(err, workflow.ErrTimeout) || err.Error() != "plain engine failure" {
		t.Errorf("failure(plain) = %v", err)
	}
}

// TestOutputOf verifies that an absent native output decodes to an empty
// application result without an error.
func TestOutputOf(t *testing.T) {
	t.Parallel()

	if got, err := outputOf(nil); err != nil || len(got) != 0 {
		t.Errorf("outputOf(nil) = %q, %v", got, err)
	}

	if _, err := outputOf([]byte("nope")); err == nil {
		t.Error("outputOf(nope) error = nil")
	}
}
