package client_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"

	"arkoren.dev/core/http/client"
	"arkoren.dev/core/service/telemetry"
	"arkoren.dev/core/service/telemetry/telemetrytest"
)

// TestConstructionStoresHooksWithoutReplayingPolicies checks callback storage
// and retry-to-telemetry delegation without replaying construction policies.
func TestConstructionStoresHooksWithoutReplayingPolicies(t *testing.T) {
	t.Parallel()

	instance := telemetrytest.New().Telemetry(t)
	hooks := [3]int{}
	requests := 0
	base := roundTripFunc(func(*http.Request) (*http.Response, error) {
		requests++
		status := http.StatusServiceUnavailable

		if requests == 2 {
			status = http.StatusOK
		}

		return &http.Response{StatusCode: status, Body: http.NoBody, Header: make(http.Header)}, nil
	})

	policies := 0
	var retained *client.Builder
	transport := client.Transport(base, func(transport *client.Builder) {
		retained = transport
		policies++
		transport.Attempts(2)
		transport.Telemetry(instance)
		transport.Backoff(func(int) time.Duration {
			hooks[0]++

			return 0
		})
		transport.RetryIf(func(response *http.Response, _ error) bool {
			hooks[1]++

			return response.StatusCode == http.StatusServiceUnavailable
		})
		transport.Propagate(func(*http.Request) bool {
			hooks[2]++

			return false
		})
	})
	retained.Attempts(1)

	if hooks != [3]int{} || policies != 1 {
		t.Fatalf("construction hooks = %v, policy calls = %d", hooks, policies)
	}

	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "https://example.com", nil)

	if err != nil {
		t.Fatal(err)
	}

	response, err := do(t, transport, request)

	if err != nil {
		t.Fatal(err)
	}

	if response.StatusCode != http.StatusOK || requests != 2 {
		t.Fatalf("status = %d, requests = %d", response.StatusCode, requests)
	}

	if hooks != [3]int{1, 2, 2} || policies != 1 {
		t.Fatalf("request hooks = %v, policy calls = %d", hooks, policies)
	}
}

// ExampleTransport_directRoundTrip uses an in-memory protocol without networking.
func ExampleTransport_directRoundTrip() {
	files := fstest.MapFS{"health": {Data: []byte("ready")}}
	base := &http.Transport{}
	base.RegisterProtocol("file", http.NewFileTransportFS(files))

	transport := client.Transport(base)

	request, err := http.NewRequest(http.MethodGet, "file:///health", nil)

	if err != nil {
		fmt.Println(err)

		return
	}

	response, err := transport.RoundTrip(request)

	if err != nil {
		fmt.Println(err)

		return
	}

	data, err := io.ReadAll(response.Body)

	err = errors.Join(err, response.Body.Close())

	if err != nil {
		fmt.Println(err)

		return
	}

	fmt.Println(response.StatusCode, string(data))
	// Output: 200 ready
}

// ExampleTransport retries a GET that first fails with 503.
func ExampleTransport() {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) == 1 {
			writer.WriteHeader(http.StatusServiceUnavailable)

			return
		}

		writer.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	sender := &http.Client{
		Transport: client.Transport(nil,
			func(transport *client.Builder) {
				transport.Attempts(5)
			},
			func(transport *client.Builder) {
				transport.Backoff(client.Backoff(10*time.Millisecond, 2, time.Second))
			},
		),
	}

	response, err := sender.Get(server.URL)

	if err != nil {
		fmt.Println(err)

		return
	}

	defer func() {
		// Closing this empty test response is best-effort cleanup.
		_ = response.Body.Close()
	}()
	fmt.Println(response.StatusCode, calls.Load())
	// Output: 200 2
}

// noDelay is a backoff that never waits, so tests do not sleep.
func noDelay(int) time.Duration {
	return 0
}

// roundTripFunc adapts a function to [http.RoundTripper].
type roundTripFunc func(*http.Request) (*http.Response, error)

// RoundTrip delegates to the wrapped function, letting tests supply
// deterministic responses and transport failures without a network client.
func (roundtripfunc roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return roundtripfunc(req)
}

// sequence serves statuses in order, repeating the last, with header on every
// response, and counts requests.
func sequence(t *testing.T, header http.Header, statuses ...int) (*httptest.Server, *atomic.Int32) {
	t.Helper()

	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := int(calls.Add(1))

		for key, values := range header {
			w.Header()[key] = values
		}

		w.WriteHeader(statuses[min(n, len(statuses))-1])
	}))
	t.Cleanup(server.Close)

	return server, &calls
}

// do sends req through transport and closes the response when the test ends.
func do(t *testing.T, transport http.RoundTripper, req *http.Request) (*http.Response, error) {
	t.Helper()

	resp, err := (&http.Client{
		Transport: transport,
	}).Do(req)

	if resp != nil {
		t.Cleanup(func() {
			_ = resp.Body.Close()
		})
	}

	return resp, err
}

// TestRetry checks that a service-unavailable response is retried and the next
// successful response is returned.
func TestRetry(t *testing.T) {
	server, calls := sequence(t, nil, 503, 200)

	req, err := http.NewRequest("GET", server.URL, nil)

	if err != nil {
		t.Fatal(err)
	}

	resp, err := do(t, client.Transport(nil, func(transport *client.Builder) {
		transport.Backoff(noDelay)

	}), req)

	if err != nil {
		t.Fatal(err)
	}

	if resp.StatusCode != 200 || calls.Load() != 2 {
		t.Fatalf("status %d after %d calls, want %d after %d", resp.StatusCode, calls.Load(), 200, 2)
	}
}

// TestRetrySkipsInternalServerError checks that the default response policy
// returns HTTP 500 without retrying it.
func TestRetrySkipsInternalServerError(t *testing.T) {
	server, calls := sequence(t, nil, 500, 200)

	req, err := http.NewRequest("GET", server.URL, nil)

	if err != nil {
		t.Fatal(err)
	}

	resp, err := do(t, client.Transport(nil, func(transport *client.Builder) {
		transport.Backoff(noDelay)

	}), req)

	if err != nil {
		t.Fatal(err)
	}

	if resp.StatusCode != 500 || calls.Load() != 1 {
		t.Fatalf("status %d after %d calls, want %d after %d", resp.StatusCode, calls.Load(), 500, 1)
	}
}

// TestRetryReturnsLastResponseWhenExhausted checks that exhausting the default
// attempt budget returns the final service-unavailable response.
func TestRetryReturnsLastResponseWhenExhausted(t *testing.T) {
	server, calls := sequence(t, nil, 503)

	req, err := http.NewRequest("GET", server.URL, nil)

	if err != nil {
		t.Fatal(err)
	}

	resp, err := do(t, client.Transport(nil, func(transport *client.Builder) {
		transport.Backoff(noDelay)

	}), req)

	if err != nil {
		t.Fatal(err)
	}

	if resp.StatusCode != 503 || calls.Load() != 3 {
		t.Fatalf("status %d after %d calls, want %d after %d", resp.StatusCode, calls.Load(), 503, 3)
	}
}

// TestRetryUsesConfiguredAttempts checks that an explicit attempt budget
// replaces the default number of retry attempts.
func TestRetryUsesConfiguredAttempts(t *testing.T) {
	server, calls := sequence(t, nil, 503)

	req, err := http.NewRequest("GET", server.URL, nil)

	if err != nil {
		t.Fatal(err)
	}

	resp, err := do(t, client.Transport(nil, func(transport *client.Builder) {
		transport.Backoff(noDelay)
		transport.Attempts(5)
	}), req)

	if err != nil {
		t.Fatal(err)
	}

	if resp.StatusCode != 503 || calls.Load() != 5 {
		t.Fatalf("status %d after %d calls, want %d after %d", resp.StatusCode, calls.Load(), 503, 5)
	}
}

// TestRetryNegativeAttemptsDisablesRetries checks that a negative attempt
// setting permits only the initial request.
func TestRetryNegativeAttemptsDisablesRetries(t *testing.T) {
	server, calls := sequence(t, nil, 503, 200)

	req, err := http.NewRequest("GET", server.URL, nil)

	if err != nil {
		t.Fatal(err)
	}

	resp, err := do(t, client.Transport(nil, func(transport *client.Builder) {
		transport.Backoff(noDelay)
		transport.Attempts(-1)
	}), req)

	if err != nil {
		t.Fatal(err)
	}

	if resp.StatusCode != 503 || calls.Load() != 1 {
		t.Fatalf("status %d after %d calls, want %d after %d", resp.StatusCode, calls.Load(), 503, 1)
	}
}

// TestRetryAfterBeyondCapStopsRetries checks that Retry-After beyond the
// configured cap returns the current response without a resend.
func TestRetryAfterBeyondCapStopsRetries(t *testing.T) {
	server, calls := sequence(t, http.Header{
		"Retry-After": {"3600"},
	}, 429, 200)

	req, err := http.NewRequest("GET", server.URL, nil)

	if err != nil {
		t.Fatal(err)
	}

	resp, err := do(t, client.Transport(nil, func(transport *client.Builder) {
		transport.Backoff(noDelay)

	}), req)

	if err != nil {
		t.Fatal(err)
	}

	if resp.StatusCode != 429 || calls.Load() != 1 {
		t.Fatalf("status %d after %d calls, want %d after %d", resp.StatusCode, calls.Load(), 429, 1)
	}
}

// TestRetryAfterZeroRetriesImmediately checks that a zero Retry-After delay
// permits an immediate resend of an eligible request.
func TestRetryAfterZeroRetriesImmediately(t *testing.T) {
	server, calls := sequence(t, http.Header{
		"Retry-After": {"0"},
	}, 503, 200)

	req, err := http.NewRequest("GET", server.URL, nil)

	if err != nil {
		t.Fatal(err)
	}

	resp, err := do(t, client.Transport(nil, func(transport *client.Builder) {
		transport.Backoff(noDelay)

	}), req)

	if err != nil {
		t.Fatal(err)
	}

	if resp.StatusCode != 200 || calls.Load() != 2 {
		t.Fatalf("status %d after %d calls, want %d after %d", resp.StatusCode, calls.Load(), 200, 2)
	}
}

// TestRetrySkipsUnsafePost checks that an ordinary POST is not resent even when
// its response would otherwise allow a retry.
func TestRetrySkipsUnsafePost(t *testing.T) {
	server, calls := sequence(t, nil, 503, 200)

	req, err := http.NewRequest("POST", server.URL, nil)

	if err != nil {
		t.Fatal(err)
	}

	resp, err := do(t, client.Transport(nil, func(transport *client.Builder) {
		transport.Backoff(noDelay)

	}), req)

	if err != nil {
		t.Fatal(err)
	}

	if resp.StatusCode != 503 || calls.Load() != 1 {
		t.Fatalf("status %d after %d calls, want %d after %d", resp.StatusCode, calls.Load(), 503, 1)
	}
}

// TestRetryPostWithIdempotencyKey checks that a POST carrying an idempotency key
// can be resent after a retryable response.
func TestRetryPostWithIdempotencyKey(t *testing.T) {
	server, calls := sequence(t, nil, 503, 200)

	req, err := http.NewRequest("POST", server.URL, nil)

	if err != nil {
		t.Fatal(err)
	}

	req.Header.Set("Idempotency-Key", "k")

	resp, err := do(t, client.Transport(nil, func(transport *client.Builder) {
		transport.Backoff(noDelay)

	}), req)

	if err != nil {
		t.Fatal(err)
	}

	if resp.StatusCode != 200 || calls.Load() != 2 {
		t.Fatalf("status %d after %d calls, want %d after %d", resp.StatusCode, calls.Load(), 200, 2)
	}
}

// TestRetryPredicateCannotOverrideMethodSafety checks that an always-true
// response predicate cannot authorize replay of an unsafe POST.
func TestRetryPredicateCannotOverrideMethodSafety(t *testing.T) {
	server, calls := sequence(t, nil, 500, 200)

	req, err := http.NewRequest("POST", server.URL, nil)

	if err != nil {
		t.Fatal(err)
	}

	resp, err := do(t, client.Transport(nil, func(transport *client.Builder) {
		transport.Backoff(noDelay)
		transport.RetryIf(func(*http.Response, error) bool {
			return true
		})
	}), req)

	if err != nil {
		t.Fatal(err)
	}

	if resp.StatusCode != 500 || calls.Load() != 1 {
		t.Fatalf("status %d after %d calls, want %d after %d", resp.StatusCode, calls.Load(), 500, 1)
	}
}

// TestRetryCustomPredicateAllowsInternalServerError checks that a custom
// response predicate can retry HTTP 500 for an otherwise replay-safe DELETE.
func TestRetryCustomPredicateAllowsInternalServerError(t *testing.T) {
	server, calls := sequence(t, nil, 500, 200)

	req, err := http.NewRequest("DELETE", server.URL, nil)

	if err != nil {
		t.Fatal(err)
	}

	resp, err := do(t, client.Transport(nil, func(transport *client.Builder) {
		transport.Backoff(noDelay)
		transport.RetryIf(func(response *http.Response, _ error) bool {
			return response != nil && response.StatusCode == 500
		})
	}), req)

	if err != nil {
		t.Fatal(err)
	}

	if resp.StatusCode != 200 || calls.Load() != 2 {
		t.Fatalf("status %d after %d calls, want %d after %d", resp.StatusCode, calls.Load(), 200, 2)
	}
}

// TestRetryBody checks body replay against one shared server. Requests with
// GetBody replay their payload; non-replayable bodies stop after the first
// response.
func TestRetryBody(t *testing.T) {
	var bodies []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(body))

		if len(bodies) == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
		}
	}))
	t.Cleanup(server.Close)

	t.Run("replayed with GetBody", func(t *testing.T) {
		bodies = nil
		req, _ := http.NewRequest(http.MethodPut, server.URL, strings.NewReader("payload"))

		resp, err := do(t, client.Transport(nil, func(transport *client.Builder) {
			transport.Backoff(noDelay)
		}), req)

		if err != nil || resp.StatusCode != 200 {
			t.Fatalf("got %v, %v", resp, err)
		}

		if !slices.Equal(bodies, []string{"payload", "payload"}) {
			t.Fatalf("bodies = %q", bodies)
		}
	})

	t.Run("not retried without GetBody", func(t *testing.T) {
		bodies = nil
		req, _ := http.NewRequest(http.MethodPut, server.URL, io.NopCloser(strings.NewReader("payload")))

		resp, err := do(t, client.Transport(nil, func(transport *client.Builder) {
			transport.Backoff(noDelay)
		}), req)

		if err != nil || resp.StatusCode != 503 || len(bodies) != 1 {
			t.Fatalf("got %v, %v after %d requests", resp, err, len(bodies))
		}
	})
}

// TestRetryTransportError checks that connection failures consume the
// configured attempt budget before returning an error.
func TestRetryTransportError(t *testing.T) {
	var attempts atomic.Int32
	base := roundTripFunc(func(*http.Request) (*http.Response, error) {
		attempts.Add(1)

		return nil, errors.New("connection refused")
	})

	req, _ := http.NewRequest(http.MethodGet, "http://example.invalid", nil)

	_, err := (&http.Client{
		Transport: client.Transport(base, func(transport *client.Builder) {
			transport.Backoff(noDelay)
		}),
	}).Do(req)

	if err == nil || attempts.Load() != 3 {
		t.Fatalf("err = %v after %d attempts, want error after 3", err, attempts.Load())
	}
}

// TestCancelDuringBackoff checks that cancellation interrupts backoff without
// sending another request, even when the selected delay is long.
func TestCancelDuringBackoff(t *testing.T) {
	server, calls := sequence(t, nil, 503)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	// Cancel when the transport asks how long to wait, then offer a wait that
	// would never end.
	waiting := func(int) time.Duration {
		cancel()

		return time.Hour
	}

	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, server.URL, nil)

	_, err := do(t, client.Transport(nil, func(transport *client.Builder) {
		transport.Backoff(waiting)
	}), req)

	if !errors.Is(err, context.Canceled) || calls.Load() != 1 {
		t.Fatalf("err = %v after %d calls", err, calls.Load())
	}
}

// TestRetryAfterBeyondDeadline checks that a Retry-After delay outside the
// request deadline returns the last response without another attempt.
func TestRetryAfterBeyondDeadline(t *testing.T) {
	server, calls := sequence(t, http.Header{
		"Retry-After": {"5"},
	}, 503, 200)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	t.Cleanup(cancel)

	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, server.URL, nil)

	resp, err := do(t, client.Transport(nil), req)

	if err != nil || resp.StatusCode != 503 || calls.Load() != 1 {
		t.Fatalf("got %v, %v after %d calls", resp, err, calls.Load())
	}
}

// TestAttemptTimeout checks that an individual attempt can time out while a
// later attempt still completes the overall request.
func TestAttemptTimeout(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			<-r.Context().Done()
		}
	}))
	t.Cleanup(server.Close)

	req, _ := http.NewRequest(http.MethodGet, server.URL, nil)
	transport := client.Transport(nil, func(transport *client.Builder) {
		transport.Backoff(noDelay)
	}, func(transport *client.Builder) {
		transport.AttemptTimeout(50 * time.Millisecond)
	})

	resp, err := do(t, transport, req)

	if err != nil || resp.StatusCode != 200 || calls.Load() != 2 {
		t.Fatalf("got %v, %v after %d calls", resp, err, calls.Load())
	}
}

// TestTelemetry checks that each retry attempt has its own span and that only
// resends carry the resend-count attribute.
func TestTelemetry(t *testing.T) {
	fake := telemetrytest.New()
	server, _ := sequence(t, nil, 503, 503, 200)
	req, _ := http.NewRequest(http.MethodGet, server.URL, nil)

	transport := client.Transport(nil, func(transport *client.Builder) {
		transport.Backoff(noDelay)
	}, func(transport *client.Builder) {
		transport.Telemetry(fake.Telemetry(t))
	},
		func(transport *client.Builder) {
			transport.Logger(slog.New(slog.DiscardHandler))
		})

	resp, err := do(t, transport, req)

	if err != nil {
		t.Fatal(err)
	}

	_ = resp.Body.Close()

	spans := fake.Spans()

	if len(spans) != 3 {
		t.Fatalf("got %d spans, want one per attempt", len(spans))
	}

	var resends []int64

	for _, span := range spans {
		for _, attr := range span.Attrs {
			if attr.Key() == "http.request.resend_count" {
				resends = append(resends, attr.AsInt64())
			}
		}
	}

	slices.Sort(resends)

	if !slices.Equal(resends, []int64{1, 2}) {
		t.Fatalf("resend counts = %v, want [1 2] (absent on the first attempt)", resends)
	}
}

// TestPropagateOption verifies Builder.Propagate gates trace context injection per request.
func TestPropagateOption(t *testing.T) {
	t.Parallel()

	fake := telemetrytest.New()
	var got http.Header
	base := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		got = req.Header.Clone()

		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       http.NoBody,
			Request:    req,
		}, nil
	})

	external, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "https://api.example.com/x", nil)

	if err != nil {
		t.Fatal(err)
	}

	instance := fake.Telemetry(t)
	ctx, span := instance.Tracer("test").Start(t.Context(), "parent")
	t.Cleanup(func() {
		span.End()
	})

	external = external.WithContext(ctx)

	t.Run("default keeps external trace context private", func(t *testing.T) {
		transport := client.Transport(base, func(transport *client.Builder) {
			transport.Telemetry(instance)
			transport.Backoff(noDelay)
		})

		if _, err := do(t, transport, external); err != nil {
			t.Fatal(err)
		}

		if has := got.Get("Traceparent") != ""; has {
			t.Errorf("traceparent injected = %v, want %v", has, false)
		}
	})
	t.Run("explicit propagation includes trace context", func(t *testing.T) {
		transport := client.Transport(base, func(transport *client.Builder) {
			transport.Telemetry(instance)
			transport.Propagate(telemetry.PropagateAll)
		})

		if _, err := do(t, transport, external); err != nil {
			t.Fatal(err)
		}

		if has := got.Get("Traceparent") != ""; !has {
			t.Errorf("traceparent injected = %v, want %v", has, true)
		}
	})
}
