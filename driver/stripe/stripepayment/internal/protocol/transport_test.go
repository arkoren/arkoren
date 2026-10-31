package protocol

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// requestProbe retains the delegated request so transport-boundary tests can
// inspect rewind eligibility without replacing net/http in the network regression.
type requestProbe struct {
	// request is retained only until the synchronous test finishes inspecting it.
	// The test consumes and closes its body explicitly after RoundTrip returns.
	request *http.Request
}

// RoundTrip captures one request and supplies an empty, resource-free response.
// Body consumption is deliberately deferred to the boundary assertion.
//
// Example:
//
//	probe := &requestProbe{}
//	transport := mutationTransport{next: probe}
//	_ = transport
func (probe *requestProbe) RoundTrip(request *http.Request) (*http.Response, error) {
	probe.request = request

	return &http.Response{StatusCode: http.StatusOK, Body: http.NoBody}, nil
}

// checkEmptyMutation verifies both replay eligibility checks used by net/http:
// GetBody/Body identity and outgoing length. The stream must stay actually empty,
// not introduce a payload byte merely to discourage transparent retries.
func checkEmptyMutation(t *testing.T, body io.ReadCloser) {
	t.Helper()

	request := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "https://api.stripe.com/v1/example", nil)
	request.Body = body
	probe := &requestProbe{}

	_, err := (mutationTransport{next: probe}).RoundTrip(request)

	if err != nil {
		t.Fatal(err)
	}

	outbound := probe.request

	if outbound.GetBody != nil || outbound.Body == nil || outbound.Body == http.NoBody {
		t.Fatal("empty mutation remained eligible for transparent replay")
	}

	if outbound.ContentLength != -1 {
		t.Fatal("empty mutation did not use an unknown-length stream")
	}

	encoded, err := io.ReadAll(outbound.Body)

	if err != nil || len(encoded) != 0 {
		t.Fatal("empty mutation acquired a payload")
	}

	if err := outbound.Body.Close(); err != nil {
		t.Fatal(err)
	}
}

// TestMutationTransportNilBodyUsesNonReplayableEmptyStream guards truly nil
// request bodies, not only the SDK's non-nil reader over an empty form buffer.
func TestMutationTransportNilBodyUsesNonReplayableEmptyStream(t *testing.T) {
	t.Parallel()

	checkEmptyMutation(t, nil)
}

// TestMutationTransportNoBodyUsesNonReplayableEmptyStream guards http.NoBody,
// which the standard HTTP transport otherwise treats like an absent body.
func TestMutationTransportNoBodyUsesNonReplayableEmptyStream(t *testing.T) {
	t.Parallel()

	checkEmptyMutation(t, http.NoBody)
}

// TestMutationTransportClonesWithoutChangingPayloadOrIdempotency preserves the
// caller's request and its mutation semantics while removing only replay support.
func TestMutationTransportClonesWithoutChangingPayloadOrIdempotency(t *testing.T) {
	t.Parallel()

	request := httptest.NewRequestWithContext(t.Context(), http.MethodPost,
		"https://api.stripe.com/v1/payment_intents", strings.NewReader("amount=2500&currency=usd"))
	request.GetBody = func() (io.ReadCloser, error) {
		return io.NopCloser(strings.NewReader("amount=2500&currency=usd")), nil
	}
	request.Header.Set("Idempotency-Key", "mobile-order-123")
	probe := &requestProbe{}

	_, err := (mutationTransport{next: probe}).RoundTrip(request)

	if err != nil {
		t.Fatal(err)
	}

	if probe.request == request || request.GetBody == nil {
		t.Fatal("transport modified the caller's request")
	}

	if probe.request.GetBody != nil || probe.request.Header.Get("Idempotency-Key") != "mobile-order-123" {
		t.Fatal("transport did not preserve idempotency while disabling replay")
	}

	encoded, err := io.ReadAll(probe.request.Body)

	if err != nil || string(encoded) != "amount=2500&currency=usd" {
		t.Fatal("transport changed the mutation payload")
	}

	if err := probe.request.Body.Close(); err != nil {
		t.Fatal(err)
	}
}
