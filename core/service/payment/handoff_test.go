package payment_test

import (
	"bytes"
	"encoding/json/v2"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"arkoren.dev/core/service/payment"
)

// TestAuthorizationActionCodec checks the explicit discriminant and SDK token
// handoff, without interpreting initialization as a paid financial observation.
func TestAuthorizationActionCodec(t *testing.T) {
	t.Parallel()

	action := payment.NewClientAuthorization("stripe.payment_intent", "secret-token")

	checkActionCodec(t, action, `{"kind":"authorization","protocol":"stripe.payment_intent","token":"secret-token"}`)

	authorization, ok := action.Authorization()

	if !ok || authorization.Token != "secret-token" {
		t.Fatal("missing authorization")
	}

	authorization.Token = "changed"
	observed, _ := action.Authorization()

	if observed.Token != "secret-token" {
		t.Fatal("accessor changed action")
	}

	if _, ok := action.Order(); ok {
		t.Fatal("authorization exposed order")
	}

	if _, ok := action.Session(); ok {
		t.Fatal("authorization exposed session")
	}
}

// TestOrderActionCodec checks the separate client-order variant. Its SDK order
// reference is not converted to a financial payment identity.
func TestOrderActionCodec(t *testing.T) {
	t.Parallel()

	action := payment.NewClientOrder("paypal.order", "ORDER-1")

	checkActionCodec(t, action, `{"kind":"order","protocol":"paypal.order","order_id":"ORDER-1"}`)

	order, ok := action.Order()

	if !ok || order.OrderID != "ORDER-1" {
		t.Fatal("missing client order")
	}

	if _, ok := action.Authorization(); ok {
		t.Fatal("order exposed authorization")
	}

	if _, ok := action.Session(); ok {
		t.Fatal("order exposed session")
	}
}

// TestSessionActionCodec permits third-party session protocols without a registry.
// Data remains one opaque string, not a toolkit-decoded JSON metadata bag.
func TestSessionActionCodec(t *testing.T) {
	t.Parallel()

	action := payment.NewClientSession("third_party.session-v2", "session-1", "opaque-sdk-data")

	checkActionCodec(t, action,
		`{"kind":"session","protocol":"third_party.session-v2","session_id":"session-1","data":"opaque-sdk-data"}`)

	session, ok := action.Session()

	if !ok || session.Data != "opaque-sdk-data" {
		t.Fatal("missing session")
	}

	if _, ok := action.Authorization(); ok {
		t.Fatal("session exposed authorization")
	}

	if _, ok := action.Order(); ok {
		t.Fatal("session exposed order")
	}
}

// checkActionCodec verifies exact authorized wire shape and a lossless decode.
// Every variant uses this check, so tags and accessor state cannot drift apart.
func checkActionCodec(t *testing.T, action payment.ClientAction, expected string) {
	t.Helper()

	encoded, err := json.Marshal(action)

	if err != nil {
		t.Fatal(err)
	}

	if string(encoded) != expected {
		t.Fatalf("wire = %s", encoded)
	}

	var decoded payment.ClientAction

	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}

	if decoded != action {
		t.Fatal("action round trip changed value")
	}
}

// TestActionRejectsInvalidValues keeps unknown kinds/protocol syntax, missing
// required handoff material, and impossible mixed variants outside client code.
func TestActionRejectsInvalidValues(t *testing.T) {
	t.Parallel()

	for _, action := range []payment.ClientAction{
		{}, payment.NewClientAuthorization("bad protocol", "token"),
		payment.NewClientAuthorization("vendor.authorization", ""),
		payment.NewClientAuthorization("vendor.authorization", "token\n"),
		payment.NewClientOrder("paypal.order", "two words"),
		payment.NewClientSession("vendor.session", "", "data"),
		payment.NewClientSession("vendor.session", "session-1", ""),
	} {
		if !errors.Is(action.Err(), payment.ErrInvalidRequest) {
			t.Fatal("invalid action accepted")
		}

		if _, err := json.Marshal(action); !errors.Is(err, payment.ErrInvalidRequest) {
			t.Fatal("invalid action encoded")
		}
	}
}

// TestActionDecodeRejectsUnknownAndMixedTags preserves previous state on every
// malformed, unknown, missing, or mixed-variant input.
func TestActionDecodeRejectsUnknownAndMixedTags(t *testing.T) {
	t.Parallel()

	original := payment.NewClientOrder("paypal.order", "ORDER-1")

	for _, wire := range []string{
		`{}`, `{"kind":"unknown","protocol":"vendor.action"}`,
		`{"kind":"authorization","protocol":"vendor.authorization","token":"secret","order_id":"ORDER-2"}`,
		`{"kind":"authorization","protocol":"vendor.authorization","token":"secret","order_id":""}`,
		`{"kind":"authorization","protocol":"vendor.authorization","token":"secret","order_id":null}`,
		`{"kind":"order","protocol":"paypal.order","order_id":"ORDER-2","unknown":"value"}`,
		`{"kind":"session","protocol":"vendor.session","session_id":"session-2"}`,
		`{"kind":"order","protocol":"paypal.order","order_id":`,
	} {
		action := original

		if err := json.Unmarshal([]byte(wire), &action); err == nil {
			t.Fatal("invalid action decoded")
		}

		if action != original {
			t.Fatal("failed action decode replaced state")
		}
	}
}

// TestPreparationAllowsOnlySessionReferences verifies absence is retained rather
// than inventing financial identity, hosted checkout, status, or amount.
func TestPreparationAllowsOnlySessionReferences(t *testing.T) {
	t.Parallel()

	prepared := payment.Preparation{SessionID: "session-1",
		Action: payment.NewClientSession("vendor.session", "session-1", "sdk-data")}

	if err := prepared.Err(); err != nil {
		t.Fatal(err)
	}

	wire, err := json.Marshal(prepared)

	if err != nil {
		t.Fatal(err)
	}

	expected := `{"session_id":"session-1","action":{"kind":"session","protocol":"vendor.session","session_id":"session-1","data":"sdk-data"}}`

	if string(wire) != expected {
		t.Fatalf("session-only wire = %s", wire)
	}

	var decoded payment.Preparation

	if err := json.Unmarshal(wire, &decoded); err != nil {
		t.Fatal(err)
	}

	if decoded != prepared {
		t.Fatal("session-only observation changed")
	}
}

// TestPreparationAllowsNoResourceReferences permits a client order or token
// without asserting any payment, hosted checkout, or client-session identity.
func TestPreparationAllowsNoResourceReferences(t *testing.T) {
	t.Parallel()

	prepared := payment.Preparation{Action: payment.NewClientOrder("paypal.order", "ORDER-1")}

	if err := prepared.Err(); err != nil {
		t.Fatal(err)
	}

	if prepared.PaymentID != "" || prepared.CheckoutID != "" || prepared.SessionID != "" {
		t.Fatal("fabricated resource reference")
	}
}

// TestPreparationInvalidDecodeIsAtomic requires the action and checks syntax only
// for supplied references, preserving the prior result on failure.
func TestPreparationInvalidDecodeIsAtomic(t *testing.T) {
	t.Parallel()

	original := payment.Preparation{Action: payment.NewClientOrder("paypal.order", "ORDER-1")}

	for _, wire := range []string{
		`{}`, `{"payment_id":"two words","action":{"kind":"order","protocol":"paypal.order","order_id":"ORDER-2"}}`,
	} {
		prepared := original

		if err := json.Unmarshal([]byte(wire), &prepared); err == nil {
			t.Fatal("invalid preparation decoded")
		}

		if prepared != original {
			t.Fatal("failed decode replaced preparation")
		}
	}

	if _, err := json.Marshal(payment.Preparation{}); !errors.Is(err, payment.ErrInvalidRequest) {
		t.Fatal("missing action encoded")
	}
}

// TestHandoffDiagnosticsRedactEveryVariant checks ordinary, expanded, Go-syntax,
// and structured formatting independently of intentional JSON client delivery.
func TestHandoffDiagnosticsRedactEveryVariant(t *testing.T) {
	t.Parallel()

	authorization := payment.NewClientAuthorization("stripe.payment_intent", "private-token")
	order := payment.NewClientOrder("paypal.order", "private-order")
	session := payment.NewClientSession("vendor.session", "private-session", "private-data")
	authorizationValue, _ := authorization.Authorization()
	orderValue, _ := order.Order()
	sessionValue, _ := session.Session()
	values := []any{authorization, order, session, authorizationValue, orderValue, sessionValue,
		payment.Preparation{PaymentID: "private-payment", CheckoutID: "private-checkout", SessionID: "private-session", Action: session}}

	for _, value := range values {
		checkRedactedDiagnostic(t, value)
	}
}

// checkRedactedDiagnostic exercises independent fmt and slog protocol hooks.
// Redaction must not depend on a specific logger or a default formatting verb.
func checkRedactedDiagnostic(t *testing.T, value any) {
	t.Helper()

	var logs bytes.Buffer
	slog.New(slog.NewJSONHandler(&logs, nil)).InfoContext(t.Context(), "handoff", "value", value)

	text := fmt.Sprintf("%v %+v %#v %s %q", value, value, value, value, value) + logs.String()

	if strings.Contains(text, "private-") {
		t.Fatal("diagnostics disclosed handoff data")
	}

	if !strings.Contains(text, "REDACTED") {
		t.Fatal("diagnostics omitted redaction marker")
	}
}
