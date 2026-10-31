package paypalpayment_test

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"arkoren.dev/core/service/payment"
	"arkoren.dev/core/service/payment/paymenttest"
	"arkoren.dev/driver/paypal/paypalpayment"
)

// TestConstructorDoesNoIO verifies both callback publication and safe defaults.
func TestConstructorDoesNoIO(t *testing.T) {
	t.Parallel()

	provider, backend := newMerchant(t)

	if len(provider.calls("/v1/oauth2/token")) != 0 {
		t.Fatal("constructor performed network I/O")
	}

	if backend.HTTPClient().Timeout <= 0 {
		t.Fatal("missing finite default timeout")
	}

	if err := backend.HTTPClient().CheckRedirect(nil, nil); !errors.Is(err, http.ErrUseLastResponse) {
		t.Fatal("redirects must not forward credentials")
	}
}

// TestConcurrentColdReadsShareOneToken checks the cancelable cache under the
// race detector without sleeps, refresh goroutines or external network calls.
func TestConcurrentColdReadsShareOneToken(t *testing.T) {
	t.Parallel()

	provider, backend := newMerchant(t)
	var workers sync.WaitGroup

	for range 20 {
		workers.Go(func() {
			if _, err := backend.GetPayment(t.Context(), "capture:CAPTURE"); err != nil {
				t.Error(err)
			}
		})
	}

	workers.Wait()

	if len(provider.calls("/v1/oauth2/token")) != 1 || len(provider.calls("/v2/payments/captures/CAPTURE")) != 20 {
		t.Fatal("cold calls did not share one cached token")
	}

	if _, err := backend.GetPayment(t.Context(), "capture:CAPTURE"); err != nil {
		t.Fatal(err)
	}

	if len(provider.calls("/v1/oauth2/token")) != 1 {
		t.Fatal("warm call refreshed a valid token")
	}
}

// TestCanceledContextDoesNotAcquireToken supplements the shared suite with a
// provider-specific valid capture identity and explicit no-I/O assertion.
func TestCanceledContextDoesNotAcquireToken(t *testing.T) {
	t.Parallel()

	provider, backend := newMerchant(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	_, err := backend.GetPayment(ctx, "capture:CAPTURE")

	if !errors.Is(err, context.Canceled) || errors.Is(err, payment.ErrOutcomeUnknown) {
		t.Fatal(err)
	}

	if len(provider.calls("/v1/oauth2/token")) != 0 {
		t.Fatal("canceled request dispatched OAuth")
	}
}

// TestMalformedReadJSONIsUnavailable preserves the decoder cause while giving
// callers a portable provider-failure class, never a money-moving outcome.
func TestMalformedReadJSONIsUnavailable(t *testing.T) {
	t.Parallel()

	backend := backendWith(t, authenticated(func(_ *http.Request) (*http.Response, error) {
		return response(http.StatusOK, `{`), nil
	}))

	_, err := backend.GetPayment(t.Context(), "capture:CAPTURE")

	if !errors.Is(err, payment.ErrUnavailable) || errors.Is(err, payment.ErrOutcomeUnknown) {
		t.Fatalf("malformed read classification = %v", err)
	}

	var cause *jsontext.SyntacticError

	if !errors.As(err, &cause) {
		t.Fatalf("decoder cause lost: %v", err)
	}
}

// checkoutRequest supplies a valid mutation so error tests isolate provider
// behavior rather than the core's independent request validation boundary.
func checkoutRequest() payment.CheckoutRequest {
	return payment.NewCheckoutRequest(payment.Money{Amount: 1200, Currency: "USD"},
		"https://shop.example/paid", "order-123")
}

// TestMalformedOAuthJSONIsUnavailable fails before dispatching the business
// mutation and preserves the JSON cause without exposing its provider text.
func TestMalformedOAuthJSONIsUnavailable(t *testing.T) {
	t.Parallel()

	backend := backendWith(t, transportFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Path != "/v1/oauth2/token" {
			t.Fatal("malformed OAuth dispatched a business operation")
		}

		return response(http.StatusOK, `{`), nil
	}))

	_, err := backend.CreateCheckout(t.Context(), checkoutRequest())

	if !errors.Is(err, payment.ErrUnavailable) || errors.Is(err, payment.ErrOutcomeUnknown) {
		t.Fatalf("malformed OAuth classification = %v", err)
	}

	var cause *jsontext.SyntacticError

	if !errors.As(err, &cause) {
		t.Fatalf("OAuth decoder cause lost: %v", err)
	}
}

// TestMutationServerFailureIsOutcomeUnknown preserves the native safe error
// category while refusing to claim a rejected mutation from an HTTP 5xx.
func TestMutationServerFailureIsOutcomeUnknown(t *testing.T) {
	t.Parallel()

	attempts := 0
	backend := backendWith(t, authenticated(func(_ *http.Request) (*http.Response, error) {
		attempts++

		return response(503, `{"name":"SERVICE_UNAVAILABLE","debug_id":"debug123","message":"secret-body"}`), nil
	}))

	_, err := backend.CreateCheckout(t.Context(), checkoutRequest())

	if !errors.Is(err, payment.ErrOutcomeUnknown) || !errors.Is(err, payment.ErrUnavailable) {
		t.Fatal(err)
	}

	var native *paypalpayment.Error

	if !errors.As(err, &native) || native.Code != "SERVICE_UNAVAILABLE" || native.DebugID != "debug123" {
		t.Fatal("safe provider diagnostics lost")
	}

	if strings.Contains(err.Error(), "secret-body") || attempts != 1 {
		t.Fatal("provider text leaked or mutation retried")
	}
}

// TestInvalidOAuthPayloadIsUnavailable rejects a non-bearer token without
// treating the never-dispatched checkout as an uncertain financial mutation.
func TestInvalidOAuthPayloadIsUnavailable(t *testing.T) {
	t.Parallel()

	backend := backendWith(t, transportFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Path != "/v1/oauth2/token" {
			t.Fatal("invalid token dispatched a business operation")
		}

		return response(http.StatusOK,
			`{"access_token":"private-token","token_type":"invalid","expires_in":3600}`), nil
	}))

	_, err := backend.CreateCheckout(t.Context(), checkoutRequest())

	if !errors.Is(err, payment.ErrUnavailable) || errors.Is(err, payment.ErrOutcomeUnknown) {
		t.Fatalf("invalid OAuth payload classification = %v", err)
	}

	var native *paypalpayment.Error

	if !errors.As(err, &native) || errors.Unwrap(native) == nil {
		t.Fatal("OAuth failure cause lost")
	}

	if strings.Contains(err.Error(), "private-token") {
		t.Fatal("OAuth token leaked")
	}
}

// TestReadServerFailureIsNotOutcomeUnknown keeps observation failures distinct
// from ambiguous side effects, even though both can be unavailable.
func TestReadServerFailureIsNotOutcomeUnknown(t *testing.T) {
	t.Parallel()

	backend := backendWith(t, authenticated(func(_ *http.Request) (*http.Response, error) {
		return response(503, `{"name":"SERVICE_UNAVAILABLE"}`), nil
	}))

	_, err := backend.GetPayment(t.Context(), "capture:CAPTURE")

	if !errors.Is(err, payment.ErrUnavailable) || errors.Is(err, payment.ErrOutcomeUnknown) {
		t.Fatal(err)
	}
}

// TestDispatchedCancellationPreservesContext marks uncertain mutations while
// preserving cancellation identity for the caller's own reconciliation policy.
func TestDispatchedCancellationPreservesContext(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)

	backend := backendWith(t, authenticated(func(request *http.Request) (*http.Response, error) {
		cancel()

		return nil, request.Context().Err()
	}))

	_, err := backend.CreateCheckout(ctx, checkoutRequest())

	if !errors.Is(err, payment.ErrOutcomeUnknown) || !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

// transportFailure is an unsafe external diagnostic used to prove that the
// safe error wrapper preserves errors.As without copying its text into Error.
type transportFailure struct{}

// Error deliberately contains text that must not reach a driver error message.
func (*transportFailure) Error() string {
	return "transport secret-body"
}

// TestTransportCauseRemainsAvailable checks typed-cause preservation without
// exposing arbitrary transport error text in an outcome-unknown error.
func TestTransportCauseRemainsAvailable(t *testing.T) {
	t.Parallel()

	backend := backendWith(t, authenticated(func(_ *http.Request) (*http.Response, error) {
		return nil, &transportFailure{}
	}))

	_, err := backend.CreateCheckout(t.Context(), checkoutRequest())

	var failure *transportFailure

	if !errors.As(err, &failure) || !errors.Is(err, payment.ErrOutcomeUnknown) {
		t.Fatal(err)
	}

	if strings.Contains(err.Error(), "secret-body") {
		t.Fatal("underlying transport text leaked")
	}
}

// TestMalformedMutationResponseIsOutcomeUnknown catches both JSON and semantic
// result failures; a missing native result ID is not a successful checkout.
func TestMalformedMutationResponseIsOutcomeUnknown(t *testing.T) {
	t.Parallel()

	backend := backendWith(t, authenticated(func(_ *http.Request) (*http.Response, error) {
		return response(201, `{`), nil
	}))

	_, err := backend.CreateCheckout(t.Context(), checkoutRequest())

	if !errors.Is(err, payment.ErrOutcomeUnknown) || errors.Is(err, payment.ErrUnavailable) {
		t.Fatal(err)
	}

	var native *paypalpayment.Error

	if !errors.As(err, &native) || errors.Unwrap(native) == nil {
		t.Fatal("decoder cause was not retained")
	}
}

// TestMissingMutationIdentityIsOutcomeUnknown rejects a syntactically valid
// response that cannot identify the created resource for reconciliation.
func TestMissingMutationIdentityIsOutcomeUnknown(t *testing.T) {
	t.Parallel()

	backend := backendWith(t, authenticated(func(_ *http.Request) (*http.Response, error) {
		return response(201, `{"status":"CREATED"}`), nil
	}))

	_, err := backend.CreateCheckout(t.Context(), checkoutRequest())

	if !errors.Is(err, payment.ErrOutcomeUnknown) {
		t.Fatal(err)
	}
}

// trackedBody records the precise number of bytes consumed and body closure.
// It detects silent truncation and unbounded draining, not just error strings.
type trackedBody struct {
	// reader supplies the simulated oversized provider payload.
	reader io.Reader
	// read counts bytes actually pulled by the production client.
	read int
	// closed records mandatory response cleanup after bounded reading.
	closed bool
}

// Read counts provider bytes without changing reader behavior.
func (body *trackedBody) Read(buffer []byte) (int, error) {
	count, err := body.reader.Read(buffer)
	body.read += count

	return count, err
}

// Close records cleanup without simulating a second provider operation.
func (body *trackedBody) Close() error {
	body.closed = true

	return nil
}

// TestSuccessfulResponseBoundUsesLimitPlusOne checks a one-MiB business bound,
// outcome ambiguity, and cleanup without reading the entire provider stream.
func TestSuccessfulResponseBoundUsesLimitPlusOne(t *testing.T) {
	t.Parallel()

	body := &trackedBody{reader: strings.NewReader(strings.Repeat("x", 2<<20))}
	backend := backendWith(t, authenticated(func(_ *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 201, Header: make(http.Header), Body: body}, nil
	}))

	_, err := backend.CreateCheckout(t.Context(), checkoutRequest())

	if !errors.Is(err, payment.ErrOutcomeUnknown) || body.read != (1<<20)+1 || !body.closed {
		t.Fatal("successful response was not bounded, closed, and marked ambiguous")
	}
}

// TestErrorResponseBoundDoesNotHideConfirmedRejection uses the smaller 64-KiB
// bound while preserving a confirmed forbidden category without blind replay.
func TestErrorResponseBoundDoesNotHideConfirmedRejection(t *testing.T) {
	t.Parallel()

	body := &trackedBody{reader: strings.NewReader(strings.Repeat("x", 1<<20))}
	backend := backendWith(t, authenticated(func(_ *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 403, Header: make(http.Header), Body: body}, nil
	}))

	_, err := backend.CreateCheckout(t.Context(), checkoutRequest())

	if !errors.Is(err, payment.ErrForbidden) || errors.Is(err, payment.ErrOutcomeUnknown) {
		t.Fatal(err)
	}

	if body.read != (64<<10)+1 || !body.closed {
		t.Fatal("error response did not use the smaller bounded read")
	}
}

// TestUnauthorizedMutationDoesNotRefreshOrReplay verifies there is exactly one
// OAuth request and one failed business attempt, never a hidden mutation retry.
func TestUnauthorizedMutationDoesNotRefreshOrReplay(t *testing.T) {
	t.Parallel()

	authCalls, businessCalls := 0, 0
	backend := backendWith(t, transportFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Path == "/v1/oauth2/token" {
			authCalls++

			return response(200, `{"access_token":"token","token_type":"Bearer","expires_in":3600}`), nil
		}

		businessCalls++

		return response(401, `{"name":"AUTHENTICATION_FAILURE"}`), nil
	}))

	_, err := backend.CreateCheckout(t.Context(), checkoutRequest())

	if !errors.Is(err, payment.ErrUnauthorized) || errors.Is(err, payment.ErrOutcomeUnknown) {
		t.Fatal(err)
	}

	if authCalls != 1 || businessCalls != 1 {
		t.Fatal("401 caused refresh or mutation replay")
	}
}

// TestDeclineDoesNotBecomeOutcomeUnknown keeps confirmed payer rejection out
// of an ambiguous retry path and preserves its machine-readable issue.
func TestDeclineDoesNotBecomeOutcomeUnknown(t *testing.T) {
	t.Parallel()

	backend := backendWith(t, authenticated(func(_ *http.Request) (*http.Response, error) {
		return response(422, `{"name":"UNPROCESSABLE_ENTITY",
			"details":[{"issue":"INSTRUMENT_DECLINED","description":"secret-body"}]}`), nil
	}))

	_, err := backend.CreateCheckout(t.Context(), checkoutRequest())

	if !errors.Is(err, payment.ErrDeclined) || errors.Is(err, payment.ErrOutcomeUnknown) {
		t.Fatal(err)
	}
}

// TestOAuthBoundFailsBeforeBusinessDispatch applies the 64-KiB OAuth bound
// without marking the never-dispatched checkout mutation outcome-unknown.
func TestOAuthBoundFailsBeforeBusinessDispatch(t *testing.T) {
	t.Parallel()

	attempts := 0
	backend := backendWith(t, transportFunc(func(_ *http.Request) (*http.Response, error) {
		attempts++

		return response(200, strings.Repeat("x", 65<<10)), nil
	}))

	_, err := backend.CreateCheckout(t.Context(), checkoutRequest())

	if err == nil || errors.Is(err, payment.ErrOutcomeUnknown) || attempts != 1 {
		t.Fatal("OAuth failure dispatched business mutation or lost its pre-dispatch semantics")
	}
}

// TestTokenRefreshUsesExpirySkew advances only synctest time, not wall time.
// The cache refreshes a sixty-second token at fifty-four seconds.
func TestTokenRefreshUsesExpirySkew(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		authCalls := 0
		backend := backendWith(t, transportFunc(func(request *http.Request) (*http.Response, error) {
			if request.URL.Path == "/v1/oauth2/token" {
				authCalls++

				return response(200, `{"access_token":"token","token_type":"Bearer","expires_in":60}`), nil
			}

			return response(200, `{"id":"CAPTURE","status":"PENDING","amount":{"currency_code":"USD","value":"12.00"}}`), nil
		}))

		if _, err := backend.GetPayment(t.Context(), "capture:CAPTURE"); err != nil {
			t.Fatal(err)
		}

		time.Sleep(55 * time.Second)

		if _, err := backend.GetPayment(t.Context(), "capture:CAPTURE"); err != nil {
			t.Fatal(err)
		}

		if authCalls != 2 {
			t.Fatal("expiring token was not refreshed with skew")
		}
	})
}

// TestWaitingForTokenRespectsCancellation holds a cold refresh at the provider
// boundary, then cancels a second caller while it waits for the same token.
// All blocking is deterministic under synctest; no worker survives the test.
func TestWaitingForTokenRespectsCancellation(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		started, release := make(chan struct{}), make(chan struct{})
		backend := backendWith(t, transportFunc(func(request *http.Request) (*http.Response, error) {
			if request.URL.Path == "/v1/oauth2/token" {
				close(started)
				<-release

				return response(200, `{"access_token":"token","token_type":"Bearer","expires_in":3600}`), nil
			}

			return response(200, `{"id":"CAPTURE","status":"PENDING","amount":{"currency_code":"USD","value":"12.00"}}`), nil
		}))

		var workers sync.WaitGroup
		first := make(chan error, 1)

		workers.Go(func() {
			_, err := backend.GetPayment(t.Context(), "capture:CAPTURE")
			first <- err
		})
		<-started

		ctx, cancel := context.WithCancel(t.Context())
		t.Cleanup(cancel)
		second := make(chan error, 1)

		workers.Go(func() {
			_, err := backend.GetPayment(ctx, "capture:CAPTURE")
			second <- err
		})
		synctest.Wait()

		cancel()
		err := <-second
		close(release)
		workers.Wait()

		if !errors.Is(err, context.Canceled) || errors.Is(err, payment.ErrOutcomeUnknown) {
			t.Fatal("token waiter lost cancellation or was treated as a dispatched mutation")
		}

		if err := <-first; err != nil {
			t.Fatal(err)
		}
	})
}

// transportFunc substitutes only the external PayPal transport. Logical URLs
// remain fixed provider origins rather than test-only public endpoint settings.
type transportFunc func(*http.Request) (*http.Response, error)

// RoundTrip dispatches to the test's deterministic protocol function.
func (transport transportFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return transport(request)
}

// response constructs a body that the production client must read and close.
func response(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
}

// call records the actual outbound wire data, separate from simulator state.
type call struct {
	// method distinguishes pure reads from dispatched mutations.
	method string
	// path is the exact provider endpoint, including its API version.
	path string
	// header is detached from the live request before recording.
	header http.Header
	// body is the transmitted JSON or OAuth form, not a driver DTO.
	body []byte
}

// merchant simulates only the provider endpoints used by the payment driver.
// A mutex protects state and observations so concurrent token tests are real.
type merchant struct {
	// mutex protects every field below, including fixture-control hooks.
	mutex sync.Mutex
	// sent records OAuth and business attempts in dispatch order.
	sent []call
	// orderStatus changes only by explicit fixture approval or capture.
	orderStatus string
	// captureStatus changes independently from order completion.
	captureStatus string
	// subscriptionStatus models approval and lifecycle, never payment.
	subscriptionStatus string
	// refundAmount retains the explicit provider request amount as JSON.
	refundAmount jsontext.Value
}

// newMerchant initializes a network-free provider and returns its public driver.
// The same fixture powers conformance journeys and provider wire assertions.
func newMerchant(t *testing.T) (*merchant, *paypalpayment.Driver) {
	t.Helper()

	provider := &merchant{orderStatus: "CREATED", captureStatus: "PENDING", subscriptionStatus: "APPROVAL_PENDING"}
	backend, err := paypalpayment.New("client", "secret", func(account *paypalpayment.Builder) {
		account.HTTPClient(&http.Client{Transport: provider})
	})

	if err != nil {
		t.Fatal(err)
	}

	return provider, backend
}

// RoundTrip validates the fixed origin and records requests before routing.
// It never connects to a network or follows response links.
func (provider *merchant) RoundTrip(request *http.Request) (*http.Response, error) {
	if request.URL.Scheme != "https" || request.URL.Host != "api-m.sandbox.paypal.com" {
		return nil, errors.New("unexpected logical PayPal origin")
	}

	body, err := io.ReadAll(request.Body)

	if err != nil {
		return nil, err
	}

	provider.mutex.Lock()
	defer provider.mutex.Unlock()
	provider.sent = append(provider.sent, call{method: request.Method, path: request.URL.Path,
		header: request.Header.Clone(), body: body})

	if request.URL.Path == "/v1/oauth2/token" {
		return response(200, `{"access_token":"cached-token","token_type":"Bearer","expires_in":3600}`), nil
	}

	if request.Header.Get("Authorization") != "Bearer cached-token" {
		return response(401, `{"name":"AUTHENTICATION_FAILURE"}`), nil
	}

	switch {
	case strings.HasPrefix(request.URL.Path, "/v2/checkout/orders"):
		return provider.orders(request)
	case strings.HasPrefix(request.URL.Path, "/v2/payments/"):
		return provider.payments(request, body)
	case strings.HasPrefix(request.URL.Path, "/v1/billing/subscriptions"):
		return provider.subscriptions(request)
	default:
		return nil, errors.New("unexpected provider route")
	}
}

// orders separates order completion from capture settlement in every response.
// This catches accidental order.COMPLETED-to-payment.Succeeded mappings.
func (provider *merchant) orders(request *http.Request) (*http.Response, error) {
	if strings.HasSuffix(request.URL.Path, "/capture") {
		provider.orderStatus = "COMPLETED"
	}

	capture := ""

	if provider.orderStatus == "COMPLETED" {
		capture = fmt.Sprintf(`,"payments":{"captures":[{"id":"CAPTURE","status":%q,
			"amount":{"currency_code":"USD","value":"12.00"}}]}`,
			provider.captureStatus)
	}

	body := fmt.Sprintf(`{"id":"ORDER","status":%q,"purchase_units":[{"custom_id":"order-123",
		"amount":{"currency_code":"USD","value":"12.00"}%s}],
		"links":[{"rel":"payer-action","href":"https://www.sandbox.paypal.com/approve"}]}`,
		provider.orderStatus, capture)

	return response(200, body), nil
}

// payments implements capture reads and explicit refunds. It never invents
// remaining-balance behavior when the request omits its amount.
func (provider *merchant) payments(request *http.Request, body []byte) (*http.Response, error) {
	if strings.HasSuffix(request.URL.Path, "/refund") {
		var payload struct {
			Amount jsontext.Value `json:"amount"`
		}

		if err := json.Unmarshal(body, &payload); err != nil {
			return nil, err
		}

		provider.refundAmount = payload.Amount

		return provider.refund(), nil
	}

	if strings.Contains(request.URL.Path, "/refunds/") {
		return provider.refund(), nil
	}

	return response(200, fmt.Sprintf(`{"id":"CAPTURE","status":%q,"amount":{"currency_code":"USD","value":"12.00"}}`,
		provider.captureStatus)), nil
}

// refund emits the actual explicitly requested amount and a trusted capture
// relationship; successful mutation callers must not hydrate this response.
func (provider *merchant) refund() *http.Response {
	return response(200, fmt.Sprintf(`{"id":"REFUND","status":"PENDING","amount":%s,
		"links":[{"rel":"up","href":"https://api-m.sandbox.paypal.com/v2/payments/captures/CAPTURE"}]}`,
		provider.refundAmount))
}

// subscriptions returns lifecycle snapshots and a genuine empty cancel 204.
// There is intentionally no activate endpoint in this provider fixture.
func (provider *merchant) subscriptions(request *http.Request) (*http.Response, error) {
	if strings.HasSuffix(request.URL.Path, "/cancel") {
		provider.subscriptionStatus = "CANCELLED"

		return response(http.StatusNoContent, ""), nil
	}

	return response(200, fmt.Sprintf(`{"id":"I-SUB","status":%q,"plan_id":"P-MONTHLY","custom_id":"signup-123",
		"billing_info":{"next_billing_time":"2027-01-02T03:04:05Z"},
		"links":[{"rel":"approve","href":"https://www.sandbox.paypal.com/subscribe"}]}`,
		provider.subscriptionStatus)), nil
}

// calls returns detached wire observations for one exact provider route.
func (provider *merchant) calls(path string) []call {
	provider.mutex.Lock()
	defer provider.mutex.Unlock()

	var result []call

	for _, sent := range provider.sent {
		if sent.path == path {
			result = append(result, sent)
		}
	}

	return result
}

// approve performs only fixture approval. No driver mutation is used to fake
// the hosted customer action that the conformance suite must control.
func (provider *merchant) approve(_ context.Context, id payment.CheckoutID) error {
	provider.mutex.Lock()
	defer provider.mutex.Unlock()

	if strings.HasPrefix(string(id), "subscription:") {
		provider.subscriptionStatus = "APPROVED"

		return nil
	}

	provider.orderStatus = "APPROVED"

	return nil
}

// settle independently resolves a capture after explicit completion.
func (provider *merchant) settle(_ context.Context, _ payment.ID) error {
	provider.mutex.Lock()
	defer provider.mutex.Unlock()

	provider.captureStatus = "COMPLETED"

	return nil
}

// activate makes the simulated approved subscription active without claiming
// that a payment occurred or exposing an activate operation on the driver.
func (provider *merchant) activate(_ context.Context, _ payment.SubscriptionID) error {
	provider.mutex.Lock()
	defer provider.mutex.Unlock()

	provider.subscriptionStatus = "ACTIVE"

	return nil
}

// TestDriverConformance runs the complete contract against the real
// provider HTTP protocol adapter, including explicit fixture approval hooks.
func TestDriverConformance(t *testing.T) {
	t.Parallel()

	paymenttest.Run(t, func(t *testing.T) paymenttest.Harness {
		provider, backend := newMerchant(t)

		return paymenttest.Harness{Driver: backend, PlanReference: "P-MONTHLY", Approve: provider.approve,
			ResolvePayment: provider.settle, ActivateSubscription: provider.activate}
	})
}

// backendWith injects a provider transport while retaining production origin,
// timeout, authentication and redirect behavior in the real HTTP client.
func backendWith(t *testing.T, transport http.RoundTripper) *paypalpayment.Driver {
	t.Helper()

	backend, err := paypalpayment.New("client", "secret", func(account *paypalpayment.Builder) {
		account.HTTPClient(&http.Client{Transport: transport})
	})

	if err != nil {
		t.Fatal(err)
	}

	return backend
}

// authenticated supplies the common OAuth response, leaving business endpoint
// behavior under each scenario's control. It does not bypass the token client.
func authenticated(business transportFunc) transportFunc {
	return func(request *http.Request) (*http.Response, error) {
		if request.URL.Path == "/v1/oauth2/token" {
			return response(200, `{"access_token":"token","token_type":"Bearer","expires_in":3600}`), nil
		}

		return business(request)
	}
}

// TestLiveSelectionAndDraftSnapshot ensures environment choice is explicit and
// publication detaches the retained construction draft from the live client.
func TestLiveSelectionAndDraftSnapshot(t *testing.T) {
	t.Parallel()

	var retained *paypalpayment.Builder
	var hosts []string
	transport := transportFunc(func(request *http.Request) (*http.Response, error) {
		hosts = append(hosts, request.URL.Host)

		if request.URL.Path == "/v1/oauth2/token" {
			return response(200, `{"access_token":"token","token_type":"Bearer","expires_in":3600}`), nil
		}

		return response(200, `{"id":"CAPTURE","status":"COMPLETED","amount":{"currency_code":"USD","value":"12.50"}}`), nil
	})

	backend, err := paypalpayment.New("client", "secret", func(account *paypalpayment.Builder) {
		retained = account
		account.Live(true)
		account.HTTPClient(&http.Client{Transport: transport})
	})

	if err != nil {
		t.Fatal(err)
	}

	retained.Live(false)

	if _, err := backend.GetPayment(t.Context(), "capture:CAPTURE"); err != nil {
		t.Fatal(err)
	}

	for _, host := range hosts {
		if host != "api-m.paypal.com" {
			t.Fatal("retained draft changed published production origin")
		}
	}
}

// TestRedirectDoesNotForwardCredentials exercises an actual redirect response
// through the HTTP client. A second outbound business request would reveal a
// redirect-policy regression even if a custom transport accepted that host.
func TestRedirectDoesNotForwardCredentials(t *testing.T) {
	t.Parallel()

	attempts := 0
	backend := backendWith(t, authenticated(func(_ *http.Request) (*http.Response, error) {
		attempts++
		redirect := response(http.StatusTemporaryRedirect, "")
		redirect.Header.Set("Location", "https://untrusted.example/collect")

		return redirect, nil
	}))

	_, err := backend.GetPayment(t.Context(), "capture:CAPTURE")

	if !errors.Is(err, payment.ErrInvalidRequest) || attempts != 1 {
		t.Fatal("client followed an untrusted redirect")
	}
}
