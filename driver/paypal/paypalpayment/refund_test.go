package paypalpayment_test

import (
	"context"
	"encoding/json/v2"
	"errors"
	"net/http"
	"strings"
	"testing"

	"arkoren.dev/core/service/payment"
)

// TestRefundSendsExplicitAmountWithoutHydration checks the actual provider
// mutation wire, caller key and pending result independently of capture reads.
func TestRefundSendsExplicitAmountWithoutHydration(t *testing.T) {
	t.Parallel()

	provider, backend := newMerchant(t)

	refund, err := backend.RefundPayment(t.Context(), payment.NewRefundRequest("capture:CAPTURE",
		payment.Money{Amount: 100, Currency: "USD"}, "refund-123"))

	if err != nil {
		t.Fatal(err)
	}

	if refund.Status != payment.RefundStatusPending || refund.PaymentID != "capture:CAPTURE" {
		t.Fatal("pending refund or known original capture was lost")
	}

	calls := provider.calls("/v2/payments/captures/CAPTURE/refund")

	if len(calls) != 1 {
		t.Fatal("refund was not dispatched exactly once")
	}

	var payload struct {
		Amount struct {
			Value    string `json:"value"`
			Currency string `json:"currency_code"`
		} `json:"amount"`
	}

	if err := json.Unmarshal(calls[0].body, &payload); err != nil {
		t.Fatal(err)
	}

	if payload.Amount.Value != "1.00" || payload.Amount.Currency != "USD" {
		t.Fatal("explicit refund amount was omitted or changed")
	}

	if calls[0].header.Get("PayPal-Request-Id") != "refund-123" {
		t.Fatal("refund key changed")
	}

	if len(provider.calls("/v2/payments/refunds/REFUND")) != 0 {
		t.Fatal("refund response was hydrated")
	}
}

// TestRefundReplayKeepsCallerKeyAndPendingIdentity proves caller-driven retries
// reuse the caller's key and provider identity without hidden retries.
// The provider, not the driver, owns idempotency retention and money movement.
func TestRefundReplayKeepsCallerKeyAndPendingIdentity(t *testing.T) {
	t.Parallel()

	provider, backend := newMerchant(t)
	request := payment.NewRefundRequest("capture:CAPTURE", payment.Money{Amount: 100, Currency: "USD"}, "refund-replay")

	first, err := backend.RefundPayment(t.Context(), request)

	if err != nil {
		t.Fatal(err)
	}

	second, err := backend.RefundPayment(t.Context(), request)

	if err != nil {
		t.Fatal(err)
	}

	if first != second || second.Status != payment.RefundStatusPending {
		t.Fatal("provider replay identity or pending refund changed")
	}

	calls := provider.calls("/v2/payments/captures/CAPTURE/refund")

	if len(calls) != 2 {
		t.Fatal("caller-driven replay did not dispatch exactly once per call")
	}

	for _, sent := range calls {
		if sent.header.Get("PayPal-Request-Id") != request.IdempotencyKey() {
			t.Fatal("caller refund key changed")
		}
	}
}

// TestRefundLookupPendingRemainsPending observes the refund's own asynchronous
// state without interpreting a successful capture as returned funds.
func TestRefundLookupPendingRemainsPending(t *testing.T) {
	t.Parallel()

	attempts := 0
	backend := backendWith(t, authenticated(func(request *http.Request) (*http.Response, error) {
		attempts++

		if request.Method != http.MethodGet || request.URL.Path != "/v2/payments/refunds/REFUND" {
			t.Fatal("refund lookup dispatched an unexpected operation")
		}

		return response(200, `{"id":"REFUND","status":"PENDING","amount":{"currency_code":"USD","value":"1.00"}}`), nil
	}))

	refund, err := backend.GetRefund(t.Context(), "refund:REFUND")

	if err != nil {
		t.Fatal(err)
	}

	if refund.Status != payment.RefundStatusPending || refund.NativeStatus != "PENDING" || attempts != 1 {
		t.Fatal("pending refund was promoted or hydrated")
	}
}

// TestRefundDispatchedCancellationRetainsCause marks an uncertain refund without
// replaying money movement, and retains the application's cancellation reason.
func TestRefundDispatchedCancellationRetainsCause(t *testing.T) {
	t.Parallel()

	cause := errors.New("caller stopped waiting for refund")
	ctx, cancel := context.WithCancelCause(t.Context())
	t.Cleanup(func() {
		cancel(nil)
	})

	attempts := 0
	backend := backendWith(t, authenticated(func(request *http.Request) (*http.Response, error) {
		attempts++
		cancel(cause)

		return nil, request.Context().Err()
	}))

	_, err := backend.RefundPayment(ctx, payment.NewRefundRequest("capture:CAPTURE",
		payment.Money{Amount: 100, Currency: "USD"}, "refund-canceled"))

	for _, target := range []error{payment.ErrOutcomeUnknown, payment.ErrUnavailable, context.Canceled, cause} {
		if !errors.Is(err, target) {
			t.Fatalf("refund lost error identity %v: %v", target, err)
		}
	}

	if attempts != 1 {
		t.Fatal("ambiguous refund was retried or hydrated")
	}
}

// TestRefundDoesNotTrustForeignUpLink never follows an arbitrary provider link
// or manufactures a capture relationship from a foreign host.
func TestRefundDoesNotTrustForeignUpLink(t *testing.T) {
	t.Parallel()

	attempts := 0
	backend := backendWith(t, authenticated(func(_ *http.Request) (*http.Response, error) {
		attempts++

		return response(200, `{"id":"REFUND","status":"CANCELLED","amount":{"currency_code":"USD","value":"1.00"},
			"links":[{"rel":"up","href":"https://untrusted.example/v2/payments/captures/CAPTURE"}]}`), nil
	}))

	refund, err := backend.GetRefund(t.Context(), "refund:REFUND")

	if err != nil {
		t.Fatal(err)
	}

	if refund.PaymentID != "" || refund.Status != payment.RefundStatusCanceled {
		t.Fatal("foreign link was trusted or canceled status was lost")
	}

	if attempts != 1 {
		t.Fatal("untrusted relationship was fetched")
	}
}

// TestUnknownRefundStatusIsRetained preserves future provider state tokens
// without converting an unfamiliar state into a successful refund.
func TestUnknownRefundStatusIsRetained(t *testing.T) {
	t.Parallel()

	backend := backendWith(t, authenticated(func(_ *http.Request) (*http.Response, error) {
		return response(200, `{"id":"REFUND","status":"FUTURE_STATE","amount":{"currency_code":"USD","value":"1.00"}}`), nil
	}))

	refund, err := backend.GetRefund(t.Context(), "refund:REFUND")

	if err != nil {
		t.Fatal(err)
	}

	if refund.Status != payment.RefundStatusUnknown || refund.NativeStatus != "FUTURE_STATE" {
		t.Fatal("unknown native refund status lost")
	}
}

// readRefundLink observes one provider link through the public driver and real
// OAuth/HTTP encoding boundary. Each call owns a fresh fixture; no link is ever
// fetched. Unexpected business routes or methods fail the test, while malformed
// link text remains provider JSON data rather than a request destination.
func readRefundLink(t *testing.T, relation, href string) payment.Refund {
	t.Helper()

	body, err := json.Marshal(map[string]any{
		"id": "REFUND", "status": "PENDING",
		"amount": map[string]string{"currency_code": "USD", "value": "1.00"},
		"links":  []map[string]string{{"rel": relation, "href": href}},
	})

	if err != nil {
		t.Fatal(err)
	}

	backend := backendWith(t, authenticated(func(request *http.Request) (*http.Response, error) {
		if request.Method != http.MethodGet || request.URL.Path != "/v2/payments/refunds/REFUND" {
			t.Fatal("refund relationship was fetched or mutated")
		}

		return response(http.StatusOK, string(body)), nil
	}))

	refund, err := backend.GetRefund(t.Context(), "refund:REFUND")

	if err != nil {
		t.Fatal(err)
	}

	return refund
}

// TestRefundCaptureLinkAcceptsSandboxOrigin preserves the exact sandbox up-link
// relationship without a second provider lookup or a fabricated payment ID.
func TestRefundCaptureLinkAcceptsSandboxOrigin(t *testing.T) {
	t.Parallel()

	refund := readRefundLink(t, "up", "https://api-m.sandbox.paypal.com/v2/payments/captures/CAPTURE")

	if refund.PaymentID != "capture:CAPTURE" {
		t.Fatalf("sandbox capture relationship = %q", refund.PaymentID)
	}
}

// TestRefundCaptureLinkAcceptsLiveOrigin preserves the existing two-origin
// allowlist, including a live link observed by a sandbox-configured driver.
func TestRefundCaptureLinkAcceptsLiveOrigin(t *testing.T) {
	t.Parallel()

	refund := readRefundLink(t, "up", "https://api-m.paypal.com/v2/payments/captures/CAPTURE")

	if refund.PaymentID != "capture:CAPTURE" {
		t.Fatalf("live capture relationship = %q", refund.PaymentID)
	}
}

// TestRefundCaptureLinkAcceptsNativeIdentityCharacters preserves all allowed
// ASCII letter, digit, underscore, and hyphen identity characters verbatim.
func TestRefundCaptureLinkAcceptsNativeIdentityCharacters(t *testing.T) {
	t.Parallel()

	refund := readRefundLink(t, "up", "https://api-m.paypal.com/v2/payments/captures/aAZ09_-")

	if refund.PaymentID != "capture:aAZ09_-" {
		t.Fatalf("native capture identity = %q", refund.PaymentID)
	}
}

// TestRefundCaptureLinkAcceptsMaximumIdentityLength keeps the provider's
// 255-byte path-safe identity boundary inclusive rather than shortening it.
func TestRefundCaptureLinkAcceptsMaximumIdentityLength(t *testing.T) {
	t.Parallel()

	id := strings.Repeat("A", 255)

	refund := readRefundLink(t, "up", "https://api-m.paypal.com/v2/payments/captures/"+id)

	if refund.PaymentID != payment.ID("capture:"+id) {
		t.Fatal("maximum-length native capture identity was not preserved")
	}
}

// TestRefundCaptureLinkAcceptsEmptyFragment preserves the existing distinction
// between a bare fragment marker and actual fragment content after URL parsing.
func TestRefundCaptureLinkAcceptsEmptyFragment(t *testing.T) {
	t.Parallel()

	refund := readRefundLink(t, "up", "https://api-m.paypal.com/v2/payments/captures/CAPTURE#")

	if refund.PaymentID != "capture:CAPTURE" {
		t.Fatalf("empty-fragment capture relationship = %q", refund.PaymentID)
	}
}

// TestRefundCaptureLinkAcceptsParsedHTTPSScheme preserves net/url's accepted
// uppercase HTTPS spelling without introducing authority normalization.
func TestRefundCaptureLinkAcceptsParsedHTTPSScheme(t *testing.T) {
	t.Parallel()

	refund := readRefundLink(t, "up", "HTTPS://api-m.paypal.com/v2/payments/captures/CAPTURE")

	if refund.PaymentID != "capture:CAPTURE" {
		t.Fatalf("parsed HTTPS capture relationship = %q", refund.PaymentID)
	}
}

// TestRefundCaptureLinkRejectsMalformedURL treats URL parsing errors as an
// absent relationship, not an error in the otherwise valid refund snapshot.
func TestRefundCaptureLinkRejectsMalformedURL(t *testing.T) {
	t.Parallel()

	refund := readRefundLink(t, "up", "https://api-m.paypal.com/v2/payments/captures/%zz")

	if refund.PaymentID != "" {
		t.Fatal("malformed capture URL was trusted")
	}
}

// TestRefundCaptureLinkRejectsNonHTTPS refuses a downgrade even when both the
// native authority and capture path otherwise match the accepted shape.
func TestRefundCaptureLinkRejectsNonHTTPS(t *testing.T) {
	t.Parallel()

	refund := readRefundLink(t, "up", "http://api-m.paypal.com/v2/payments/captures/CAPTURE")

	if refund.PaymentID != "" {
		t.Fatal("non-HTTPS capture URL was trusted")
	}
}

// TestRefundCaptureLinkRejectsForeignAuthority preserves exact authority
// matching rather than trusting suffixes, case folding, or a trailing DNS dot.
func TestRefundCaptureLinkRejectsForeignAuthority(t *testing.T) {
	t.Parallel()

	for _, authority := range []string{
		"untrusted.example", "api-m.paypal.com.untrusted.example",
		"API-M.PAYPAL.COM", "api-m.paypal.com.",
	} {
		refund := readRefundLink(t, "up", "https://"+authority+"/v2/payments/captures/CAPTURE")

		if refund.PaymentID != "" {
			t.Fatal("nonliteral PayPal authority was trusted")
		}
	}
}

// TestRefundCaptureLinkRejectsExplicitPorts preserves the ban on any explicit
// port, including the conventional HTTPS port and nondefault port spellings.
func TestRefundCaptureLinkRejectsExplicitPorts(t *testing.T) {
	t.Parallel()

	for _, authority := range []string{"api-m.paypal.com:443", "api-m.paypal.com:8443"} {
		refund := readRefundLink(t, "up", "https://"+authority+"/v2/payments/captures/CAPTURE")

		if refund.PaymentID != "" {
			t.Fatal("explicit-port capture URL was trusted")
		}
	}
}

// TestRefundCaptureLinkRejectsUserinfo refuses credentials independently of
// authority matching, including empty userinfo and username/password forms.
func TestRefundCaptureLinkRejectsUserinfo(t *testing.T) {
	t.Parallel()

	for _, userinfo := range []string{"buyer@", "buyer:password@", "@"} {
		refund := readRefundLink(t, "up", "https://"+userinfo+"api-m.paypal.com/v2/payments/captures/CAPTURE")

		if refund.PaymentID != "" {
			t.Fatal("credential-bearing capture URL was trusted")
		}
	}
}

// TestRefundCaptureLinkRejectsQueryContent refuses a capture relationship with
// query data instead of stripping potentially meaningful provider parameters.
func TestRefundCaptureLinkRejectsQueryContent(t *testing.T) {
	t.Parallel()

	refund := readRefundLink(t, "up", "https://api-m.paypal.com/v2/payments/captures/CAPTURE?cursor=value")

	if refund.PaymentID != "" {
		t.Fatal("capture URL with query content was trusted")
	}
}

// TestRefundCaptureLinkRejectsFragmentContent refuses actual fragment data
// without changing the separately characterized empty-fragment behavior.
func TestRefundCaptureLinkRejectsFragmentContent(t *testing.T) {
	t.Parallel()

	refund := readRefundLink(t, "up", "https://api-m.paypal.com/v2/payments/captures/CAPTURE#details")

	if refund.PaymentID != "" {
		t.Fatal("capture URL with fragment content was trusted")
	}
}

// TestRefundCaptureLinkRejectsEmptyQuery preserves rejection of the bare query
// marker even though there are no query bytes after it.
func TestRefundCaptureLinkRejectsEmptyQuery(t *testing.T) {
	t.Parallel()

	refund := readRefundLink(t, "up", "https://api-m.paypal.com/v2/payments/captures/CAPTURE?")

	if refund.PaymentID != "" {
		t.Fatal("capture URL with an empty query marker was trusted")
	}
}

// TestRefundCaptureLinkRejectsEscapedPaths refuses encoded aliases for the
// accepted route or ID, encoded slashes, and decoded invalid identity bytes.
func TestRefundCaptureLinkRejectsEscapedPaths(t *testing.T) {
	t.Parallel()

	for _, path := range []string{
		"/v2/payments/%63aptures/CAPTURE", "/v2/payments/captures/CAP%54URE",
		"/v2/payments/captures/CAPTURE%2FOTHER", "/v2/payments/captures/CAPTURE%20OTHER",
	} {
		refund := readRefundLink(t, "up", "https://api-m.paypal.com"+path)

		if refund.PaymentID != "" {
			t.Fatal("escaped capture path was trusted")
		}
	}
}

// TestRefundCaptureLinkRejectsOtherPaths requires the exact case-sensitive v2
// captures prefix rather than guessing resource families or normalizing paths.
func TestRefundCaptureLinkRejectsOtherPaths(t *testing.T) {
	t.Parallel()

	for _, path := range []string{
		"/v1/payments/captures/CAPTURE", "/v2/payments/refunds/CAPTURE",
		"/v2/payments/captures", "/V2/payments/captures/CAPTURE",
	} {
		refund := readRefundLink(t, "up", "https://api-m.paypal.com"+path)

		if refund.PaymentID != "" {
			t.Fatal("noncapture path was trusted")
		}
	}
}

// TestRefundCaptureLinkRejectsEmptyIdentity keeps the trailing slash from
// becoming an invented native identity or a malformed capture: handle.
func TestRefundCaptureLinkRejectsEmptyIdentity(t *testing.T) {
	t.Parallel()

	refund := readRefundLink(t, "up", "https://api-m.paypal.com/v2/payments/captures/")

	if refund.PaymentID != "" {
		t.Fatal("empty native capture identity was trusted")
	}
}

// TestRefundCaptureLinkRejectsOverlongIdentity preserves the native identity's
// upper bound even when every byte would otherwise be path-safe ASCII.
func TestRefundCaptureLinkRejectsOverlongIdentity(t *testing.T) {
	t.Parallel()

	refund := readRefundLink(t, "up", "https://api-m.paypal.com/v2/payments/captures/"+strings.Repeat("A", 256))

	if refund.PaymentID != "" {
		t.Fatal("overlong native capture identity was trusted")
	}
}

// TestRefundCaptureLinkRejectsInvalidIdentityCharacters keeps dot, extra path
// segments, and non-ASCII identities outside the path-safe native alphabet.
func TestRefundCaptureLinkRejectsInvalidIdentityCharacters(t *testing.T) {
	t.Parallel()

	for _, id := range []string{"CAPTURE.OTHER", "CAPTURE/OTHER", "CAPTURÉ"} {
		refund := readRefundLink(t, "up", "https://api-m.paypal.com/v2/payments/captures/"+id)

		if refund.PaymentID != "" {
			t.Fatal("invalid native capture identity was trusted")
		}
	}
}

// TestRefundCaptureLinkRejectsNonabsoluteURLs keeps relative, scheme-relative,
// and opaque spellings from acquiring a trusted origin implicitly.
func TestRefundCaptureLinkRejectsNonabsoluteURLs(t *testing.T) {
	t.Parallel()

	for _, href := range []string{
		"/v2/payments/captures/CAPTURE", "//api-m.paypal.com/v2/payments/captures/CAPTURE",
		"https:api-m.paypal.com/v2/payments/captures/CAPTURE", "",
	} {
		refund := readRefundLink(t, "up", href)

		if refund.PaymentID != "" {
			t.Fatal("nonabsolute capture URL was trusted")
		}
	}
}

// TestRefundCaptureLinkRequiresUpRelation keeps otherwise trusted URLs from
// being treated as capture relationships when the provider names another role.
func TestRefundCaptureLinkRequiresUpRelation(t *testing.T) {
	t.Parallel()

	refund := readRefundLink(t, "self", "https://api-m.paypal.com/v2/payments/captures/CAPTURE")

	if refund.PaymentID != "" {
		t.Fatal("non-up relationship was interpreted as a capture")
	}
}
