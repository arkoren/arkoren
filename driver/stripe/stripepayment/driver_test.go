package stripepayment_test

import (
	"context"
	"encoding/json/v2"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stripe/stripe-go/v87"

	"arkoren.dev/core/service/payment"
	"arkoren.dev/core/service/payment/paymenttest"
	"arkoren.dev/driver/stripe/stripepayment"
)

// protocolFixture holds remote resource snapshots for one serial conformance
// scenario. Approval hooks modify these plain DTOs, never the driver or SDK.
// Each suite factory gets fresh maps; no global state or network is involved.
type protocolFixture struct {
	// resources maps native API paths to snapshots independently read by the SDK.
	resources map[string]map[string]any
}

func TestPaymentContract(t *testing.T) {
	t.Parallel()

	paymenttest.Run(t, func(t *testing.T) paymenttest.Harness {
		fixture := &protocolFixture{resources: make(map[string]map[string]any)}
		backend := newBackend(t, func(request *http.Request) (*http.Response, error) {
			return fixture.exchange(t, request)
		})

		return paymenttest.Harness{
			Driver: backend, PlanReference: "price_monthly", Approve: fixture.approve,
			ResolvePayment: func(ctx context.Context, id payment.ID) error {
				return fixture.setStatus(ctx, "/v1/payment_intents/"+string(id), "succeeded")
			},
			ActivateSubscription: func(ctx context.Context, id payment.SubscriptionID) error {
				return fixture.setStatus(ctx, "/v1/subscriptions/"+string(id), "active")
			},
		}
	})
}

// exchange simulates only the native API paths exercised by the suite. The real
// SDK still builds requests, decodes JSON, and supplies LastResponse.RawJSON.
func (fixture *protocolFixture) exchange(t *testing.T, request *http.Request) (*http.Response, error) {
	t.Helper()

	switch request.Method + " " + request.URL.Path {
	case "POST /v1/checkout/sessions":
		return jsonResponse(t, fixture.createSession(t, request)), nil
	case "POST /v1/refunds":
		return jsonResponse(t, fixture.createRefund(t, request)), nil
	case "POST /v1/payment_intents":
		return jsonResponse(t, fixture.createIntent(t, request)), nil
	case "DELETE /v1/subscriptions/sub_suite":
		if err := fixture.setStatus(request.Context(), request.URL.Path, "canceled"); err != nil {
			return nil, err
		}
	case "GET /v1/checkout/sessions/cs_suite", "GET /v1/payment_intents/pi_suite",
		"GET /v1/refunds/re_suite", "GET /v1/subscriptions/sub_suite":
	default:
		t.Fatalf("unexpected conformance request: %s %s", request.Method, request.URL.Path)
	}

	return jsonResponse(t, fixture.resources[request.URL.Path]), nil
}

// jsonResponse encodes only test-owned JSON DTOs; SDK v1 resource marshal hooks
// are not used to manufacture provider replies.
func jsonResponse(t *testing.T, snapshot map[string]any) *http.Response {
	t.Helper()

	body, err := json.Marshal(snapshot)

	if err != nil {
		t.Fatal(err)
	}

	return response(http.StatusOK, string(body))
}

// createSession snapshots the requested hosted mode and observed one-time total.
// Subscription prices are external catalog entries and carry no inline amount.
func (fixture *protocolFixture) createSession(t *testing.T, request *http.Request) map[string]any {
	t.Helper()

	values := form(t, request)
	session := map[string]any{
		"id": "cs_suite", "object": "checkout.session", "status": "open",
		"mode": values.Get("mode"), "payment_status": "unpaid",
		"client_reference_id": values.Get("client_reference_id"),
		"url":                 "https://checkout.stripe.com/fixture",
	}

	if values.Get("mode") == "payment" {
		amount, err := strconv.ParseInt(values.Get("line_items[0][price_data][unit_amount]"), 10, 64)

		if err != nil {
			t.Fatal(err)
		}

		session["amount_total"] = amount
		session["currency"] = values.Get("line_items[0][price_data][currency]")
	}

	fixture.resources["/v1/checkout/sessions/cs_suite"] = session

	return session
}

// approve simulates hosted buyer approval and establishes only the native
// relationship for the selected mode. Settlement and activation remain separate.
func (fixture *protocolFixture) approve(ctx context.Context, id payment.CheckoutID) error {
	if err := fixture.setStatus(ctx, "/v1/checkout/sessions/"+string(id), "complete"); err != nil {
		return err
	}

	session := fixture.resources["/v1/checkout/sessions/"+string(id)]

	switch session["mode"] {
	case "payment":
		session["payment_intent"] = "pi_suite"
		fixture.resources["/v1/payment_intents/pi_suite"] = map[string]any{
			"id": "pi_suite", "object": "payment_intent", "status": "requires_payment_method",
			"amount": session["amount_total"], "currency": session["currency"],
		}
	case "subscription":
		session["subscription"] = "sub_suite"
		fixture.resources["/v1/subscriptions/sub_suite"] = map[string]any{
			"id": "sub_suite", "object": "subscription", "status": "incomplete",
			"items": map[string]any{"data": []any{map[string]any{
				"price": map[string]any{"id": "price_monthly"}, "current_period_end": 1700000000,
			}}},
		}
	}

	return nil
}

// setStatus changes a fixture resource only after a valid context and identity.
// It backs independent settlement, activation, and cancellation transitions.
func (fixture *protocolFixture) setStatus(ctx context.Context, path, status string) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	snapshot, ok := fixture.resources[path]

	if !ok {
		return payment.ErrNotFound
	}

	snapshot["status"] = status

	return nil
}

// createRefund records an independently readable native refund with the exact
// requested partial amount and original PaymentIntent identity.
func (fixture *protocolFixture) createRefund(t *testing.T, request *http.Request) map[string]any {
	t.Helper()

	values := form(t, request)

	amount, err := strconv.ParseInt(values.Get("amount"), 10, 64)

	if err != nil {
		t.Fatal(err)
	}

	refund := map[string]any{
		"id": "re_suite", "object": "refund", "status": "succeeded",
		"amount": amount, "currency": "usd", "payment_intent": values.Get("payment_intent"),
	}
	fixture.resources["/v1/refunds/re_suite"] = refund

	return refund
}

// createIntent stores a guest PaymentIntent from the exact initialization form.
// Its initial state requires client authorization; the suite's independent
// ResolvePayment hook supplies settlement without inventing checkout relations.
func (fixture *protocolFixture) createIntent(t *testing.T, request *http.Request) map[string]any {
	t.Helper()

	values := form(t, request)

	amount, err := strconv.ParseInt(values.Get("amount"), 10, 64)

	if err != nil {
		t.Fatal(err)
	}

	intent := map[string]any{
		"id": "pi_suite", "object": "payment_intent", "status": "requires_payment_method",
		"amount": amount, "currency": values.Get("currency"),
		"client_secret": "pi_suite_secret_fixture",
		"metadata":      map[string]string{"reference": values.Get("metadata[reference]")},
	}
	fixture.resources["/v1/payment_intents/pi_suite"] = intent

	return intent
}

// transportFunc substitutes only HTTP I/O; all requests use the real Stripe SDK.
type transportFunc func(*http.Request) (*http.Response, error)

// RoundTrip forwards one SDK request to a deterministic local script.
func (transport transportFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return transport(request)
}

// newBackend guards the logical Stripe origin and API version on every request.
// It never permits real network I/O and fails immediately on an unexpected host.
func newBackend(t *testing.T, exchange transportFunc) *stripepayment.Driver {
	t.Helper()

	backend, err := stripepayment.New("sk_test_fixture", func(connection *stripepayment.Builder) {
		connection.HTTPClient(&http.Client{Transport: transportFunc(func(request *http.Request) (*http.Response, error) {
			if request.URL.Host != "api.stripe.com" || request.URL.Scheme != "https" {
				t.Fatalf("unexpected origin: %s", request.URL.Host)
			}

			if request.Header.Get("Stripe-Version") != stripe.APIVersion {
				t.Fatal("SDK API version not pinned")
			}

			if request.Header.Get("X-Stripe-Client-Telemetry") != "" {
				t.Fatal("SDK telemetry enabled")
			}

			return exchange(request)
		})})
	})

	if err != nil {
		t.Fatal(err)
	}

	return backend
}

// response supplies a body that the real SDK must read and decode.
func response(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)),
	}
}

// form reads the exact SDK-encoded POST body for wire assertions.
func form(t *testing.T, request *http.Request) url.Values {
	t.Helper()

	body, err := io.ReadAll(request.Body)

	if err != nil {
		t.Fatal(err)
	}

	values, err := url.ParseQuery(string(body))

	if err != nil {
		t.Fatal(err)
	}

	return values
}

// expectRequest verifies the native HTTP operation at the SDK boundary and gives
// one useful diagnostic for the many transport fixtures sharing this assertion.
func expectRequest(t *testing.T, request *http.Request, method, path string) {
	t.Helper()

	if request.Method != method || request.URL.Path != path {
		t.Fatalf("request: %s %s; want %s %s", request.Method, request.URL.Path, method, path)
	}
}

func TestNewNetworkFreeAndNativeOwnership(t *testing.T) {
	t.Parallel()

	client := &http.Client{Timeout: 2 * time.Second, Transport: transportFunc(func(*http.Request) (*http.Response, error) {
		t.Fatal("constructor performed I/O")

		return nil, errors.New("unexpected request")
	})}
	var retained *stripepayment.Builder
	backend, err := stripepayment.New("sk_test_fixture", func(connection *stripepayment.Builder) {
		retained = connection
		connection.HTTPClient(client)
	})

	if err != nil || backend.Client() == nil {
		t.Fatalf("New: %v", err)
	}

	retained.HTTPClient(nil)

	if client.Timeout != 2*time.Second {
		t.Fatal("New modified caller HTTP client")
	}

	native := stripe.NewClient("sk_test_fixture")

	borrowed, err := stripepayment.NewFrom(native)

	if err != nil || borrowed.Client() != native {
		t.Fatalf("native ownership changed: %v", err)
	}

	var contract payment.Driver = borrowed
	_ = contract
}

func TestNewRejectsInvalidConfiguration(t *testing.T) {
	t.Parallel()

	_, err := stripepayment.New("secret\nfixture")

	if !errors.Is(err, payment.ErrInvalidRequest) || strings.Contains(err.Error(), "fixture") {
		t.Fatalf("unsafe key error: %v", err)
	}

	_, err = stripepayment.NewFrom(nil)

	if !errors.Is(err, payment.ErrInvalidRequest) {
		t.Fatalf("nil client: %v", err)
	}

	_, err = stripepayment.New("sk_test_fixture", func(connection *stripepayment.Builder) {
		connection.HTTPClient(&http.Client{Timeout: -time.Second})
	})

	if !errors.Is(err, payment.ErrInvalidRequest) {
		t.Fatalf("negative timeout: %v", err)
	}
}

// TestInvalidKeySkipsConstructionCallbacks characterizes the credential guard:
// a supplied nil policy is not invoked until the required key is valid.
func TestInvalidKeySkipsConstructionCallbacks(t *testing.T) {
	t.Parallel()

	_, err := stripepayment.New("", nil)

	if !errors.Is(err, payment.ErrInvalidRequest) {
		t.Fatalf("invalid key classification = %v", err)
	}
}

// TestValidKeyPropagatesNilConstructionCallback keeps callback misuse distinct
// from a normal configuration error once the credential guard succeeds.
func TestValidKeyPropagatesNilConstructionCallback(t *testing.T) {
	t.Parallel()

	defer func() {
		if recover() == nil {
			t.Fatal("nil construction callback did not panic")
		}
	}()

	_, _ = stripepayment.New("sk_test_fixture", nil)
}

func TestNewFromPreservesCallerRetryPolicy(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		attempts := 0
		client := &http.Client{Transport: transportFunc(func(*http.Request) (*http.Response, error) {
			attempts++

			if attempts == 1 {
				result := response(http.StatusOK, "")
				result.Body = brokenBody{}

				return result, nil
			}

			return response(http.StatusOK, `{"id":"cs_native","object":"checkout.session","mode":"payment","status":"open"}`), nil
		})}
		backends := stripe.NewBackendsWithConfig(&stripe.BackendConfig{
			HTTPClient: client, MaxNetworkRetries: stripe.Int64(1), EnableTelemetry: stripe.Bool(false),
			LeveledLogger: &stripe.LeveledLogger{Level: stripe.LevelNull},
		})
		native := stripe.NewClient("sk_test_fixture", stripe.WithBackends(backends))

		backend, err := stripepayment.NewFrom(native)

		if err != nil {
			t.Fatal(err)
		}

		_, err = backend.CreateCheckout(t.Context(), checkoutRequest())

		if err != nil || attempts != 2 {
			t.Fatalf("caller retry policy changed: %v; attempts=%d", err, attempts)
		}

		if backend.Client() != native || client.Timeout != 0 {
			t.Fatal("borrowed native or HTTP client modified")
		}
	})
}

func TestZeroTimeoutGetsFiniteCopyWithoutChangingCaller(t *testing.T) {
	t.Parallel()

	client := &http.Client{Transport: transportFunc(func(request *http.Request) (*http.Response, error) {
		if _, ok := request.Context().Deadline(); !ok {
			t.Fatal("zero timeout allowed an unbounded SDK request")
		}

		return response(http.StatusOK, `{"id":"pi_native","object":"payment_intent","status":"processing","amount":2500,"currency":"usd"}`), nil
	})}

	backend, err := stripepayment.New("sk_test_fixture", func(connection *stripepayment.Builder) {
		connection.HTTPClient(client)
	})

	if err != nil {
		t.Fatal(err)
	}

	_, err = backend.GetPayment(t.Context(), "pi_native")

	if err != nil || client.Timeout != 0 {
		t.Fatalf("default timeout mutated caller: %v; timeout=%s", err, client.Timeout)
	}
}

func TestNewRefusesRedirectWithoutChangingCallerClient(t *testing.T) {
	t.Parallel()

	attempts := 0
	redirects := 0
	client := &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error {
			redirects++

			return nil
		},
		Transport: transportFunc(func(request *http.Request) (*http.Response, error) {
			attempts++

			if request.URL.Host != "api.stripe.com" {
				t.Fatal("redirect followed to another origin; credentials could be forwarded")
			}

			if request.Header.Get("Authorization") != "Bearer sk_test_fixture" {
				t.Fatal("initial Stripe request did not use the fixture credential")
			}

			result := response(http.StatusFound, "")
			// A subdomain redirect would retain Authorization under Go's default policy.
			result.Header.Set("Location", "https://redirect.api.stripe.com/leak")

			return result, nil
		}),
	}

	backend, err := stripepayment.New("sk_test_fixture", func(connection *stripepayment.Builder) {
		connection.HTTPClient(client)
	})

	if err != nil {
		t.Fatal(err)
	}

	_, err = backend.GetPayment(t.Context(), "pi_native")

	if err == nil || attempts != 1 || redirects != 0 {
		t.Fatalf("redirect refusal: %v; attempts=%d; caller redirects=%d", err, attempts, redirects)
	}

	if client.Timeout != 0 || client.CheckRedirect == nil {
		t.Fatal("original HTTP client was modified")
	}

	if err := client.CheckRedirect(nil, nil); err != nil {
		t.Fatal("original redirect policy was replaced")
	}
}

// http1Intent supplies a complete warmup read and a usable response if the HTTP
// transport unexpectedly replays a mutation. A replay must fail the count check,
// even if the operation ultimately returns a successful initialization result.
const http1Intent = `{"id":"pi_native","object":"payment_intent","status":"requires_payment_method","amount":2500,"currency":"usd","client_secret":"pi_native_secret_fixture"}`

// lostResponseFixture simulates Stripe applying a mutation and losing its reply.
// It closes the first warmed HTTP/1 connection after consuming the entire body,
// the exact situation where net/http can replay an SDK idempotent POST internally.
type lostResponseFixture struct {
	// testing reports protocol failures from the local server goroutine.
	testing *testing.T

	// attempts counts server-observed mutations, not RoundTrip invocations.
	attempts atomic.Int32

	// bytes records consumed mutation bytes, including genuinely empty bodies.
	bytes atomic.Int64

	// warmAddress records the peer of the successful GET to prove connection reuse.
	warmAddress atomic.Value
}

// ServeHTTP warms one persistent connection, then drops its mutation response.
// Subsequent mutations succeed so hidden net/http replay cannot pass unnoticed.
//
// Example:
//
//	server := httptest.NewServer(fixture)
//	t.Cleanup(server.Close)
func (fixture *lostResponseFixture) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	if request.Method == http.MethodGet {
		fixture.warmAddress.Store(request.RemoteAddr)

		if _, err := io.WriteString(writer, http1Intent); err != nil {
			fixture.testing.Error("writing warmup response failed")
		}

		return
	}

	count, err := io.Copy(io.Discard, request.Body)

	if err != nil {
		fixture.testing.Error("reading mutation body failed")

		return
	}

	fixture.bytes.Store(count)

	if fixture.attempts.Add(1) > 1 {
		if _, err := io.WriteString(writer, http1Intent); err != nil {
			fixture.testing.Error("writing replay response failed")
		}

		return
	}

	if request.RemoteAddr != fixture.warmAddress.Load() {
		fixture.testing.Error("mutation did not use the warmed persistent connection")
	}

	connection, _, err := writer.(http.Hijacker).Hijack()

	if err != nil {
		fixture.testing.Error("hijacking HTTP/1 connection failed")

		return
	}

	if err := connection.Close(); err != nil {
		fixture.testing.Error("closing consumed mutation connection failed")
	}
}

// localHTTP1Client routes the logical Stripe origin to a real local HTTP/1
// transport without changing GetBody, Body, or idempotency headers. This is not a
// scripted RoundTripper: retry decisions occur inside the standard HTTP transport.
func localHTTP1Client(t *testing.T, handler http.Handler) *http.Client {
	t.Helper()

	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	transport := &http.Transport{}
	t.Cleanup(transport.CloseIdleConnections)

	return &http.Client{Transport: transportFunc(func(request *http.Request) (*http.Response, error) {
		local := request.Clone(request.Context())
		local.URL.Scheme = "http"
		local.URL.Host = server.Listener.Addr().String()

		return transport.RoundTrip(local)
	})}
}

// TestNewHTTP1PreparationMutationLostResponseNeverReplayed detects internal net/http
// retries, which fake RoundTrippers and SDK retry counters cannot observe.
func TestNewHTTP1PreparationMutationLostResponseNeverReplayed(t *testing.T) {
	t.Parallel()

	fixture := &lostResponseFixture{testing: t}
	client := localHTTP1Client(t, fixture)

	backend, err := stripepayment.New("sk_test_fixture", func(connection *stripepayment.Builder) {
		connection.HTTPClient(client)
	})

	if err != nil {
		t.Fatal(err)
	}

	if _, err := backend.GetPayment(t.Context(), "pi_native"); err != nil {
		t.Fatal(err)
	}

	_, err = payment.New(backend).PreparePayment(t.Context(), preparationRequest())

	if !errors.Is(err, payment.ErrOutcomeUnknown) || fixture.attempts.Load() != 1 {
		t.Fatalf("lost mutation response: %v; server mutations=%d", err, fixture.attempts.Load())
	}

	if fixture.bytes.Load() == 0 {
		t.Fatal("preparation did not send its nonempty SDK request body")
	}
}

// TestNewHTTP1EmptyMutationLostResponseNeverReplayed covers native SDK capture
// requests with no form parameters: clearing GetBody alone does not stop replay
// when net/http regards a nil or NoBody body as replayable.
func TestNewHTTP1EmptyMutationLostResponseNeverReplayed(t *testing.T) {
	t.Parallel()

	fixture := &lostResponseFixture{testing: t}
	client := localHTTP1Client(t, fixture)

	backend, err := stripepayment.New("sk_test_fixture", func(connection *stripepayment.Builder) {
		connection.HTTPClient(client)
	})

	if err != nil {
		t.Fatal(err)
	}

	if _, err := backend.GetPayment(t.Context(), "pi_native"); err != nil {
		t.Fatal(err)
	}

	_, err = backend.Client().V1PaymentIntents.Capture(t.Context(), "pi_native", nil)

	if err == nil || fixture.attempts.Load() != 1 {
		t.Fatalf("lost empty mutation response: server mutations=%d", fixture.attempts.Load())
	}

	if fixture.bytes.Load() != 0 {
		t.Fatal("capture must send no form parameters or invented payload")
	}
}

// TestNewFromPreservesHTTP1MutationReplayPolicy proves the borrowed client's HTTP
// transport still makes its original replay decision with SDK retries disabled.
func TestNewFromPreservesHTTP1MutationReplayPolicy(t *testing.T) {
	t.Parallel()

	fixture := &lostResponseFixture{testing: t}
	client := localHTTP1Client(t, fixture)
	backends := stripe.NewBackendsWithConfig(&stripe.BackendConfig{
		HTTPClient: client, MaxNetworkRetries: stripe.Int64(0), EnableTelemetry: stripe.Bool(false),
		LeveledLogger: &stripe.LeveledLogger{Level: stripe.LevelNull},
	})
	native := stripe.NewClient("sk_test_fixture", stripe.WithBackends(backends))

	backend, err := stripepayment.NewFrom(native)

	if err != nil {
		t.Fatal(err)
	}

	if _, err := backend.GetPayment(t.Context(), "pi_native"); err != nil {
		t.Fatal(err)
	}

	_, err = payment.New(backend).PreparePayment(t.Context(), preparationRequest())

	if err != nil || fixture.attempts.Load() != 2 {
		t.Fatalf("borrowed HTTP transport policy changed: %v; server mutations=%d", err, fixture.attempts.Load())
	}
}
