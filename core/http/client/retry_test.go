package client_test

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"testing"
	"testing/iotest"
	"testing/synctest"
	"time"

	"arkoren.dev/core/event"
	"arkoren.dev/core/http/client"
)

// upgradeReadCloser observes native closure in retry phase-order tests.
type upgradeReadCloser struct {
	// Reader supplies scripted native bytes and failures.
	io.Reader
	// close records the unguarded native closure operation.
	close func() error
}

// Close invokes native closure without adding idempotence in the fixture.
func (body *upgradeReadCloser) Close() error {
	return body.close()
}

// upgradeWriterFunc supplies an observable native write capability.
type upgradeWriterFunc func([]byte) (int, error)

// Write forwards the scripted result unchanged.
func (write upgradeWriterFunc) Write(data []byte) (int, error) {
	return write(data)
}

// TestRetryAfterUnboundedDecimalStopsRetries checks saturation before the cap.
func TestRetryAfterUnboundedDecimalStopsRetries(t *testing.T) {
	t.Parallel()

	calls := 0
	base := roundTripFunc(func(*http.Request) (*http.Response, error) {
		calls++

		return &http.Response{
			StatusCode: http.StatusServiceUnavailable,
			Header:     http.Header{"Retry-After": {"999999999999999999999999999999999999999999"}},
			Body:       http.NoBody,
		}, nil
	})

	transport := client.Transport(base, func(transport *client.Builder) {
		transport.Attempts(2)
		transport.Backoff(noDelay)
	})

	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://local.invalid", nil)

	if err != nil {
		t.Fatal(err)
	}

	response, err := do(t, transport, request)

	if err != nil {
		t.Fatal(err)
	}

	if calls != 1 || response.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("calls = %d, status = %d; oversized delay was ignored", calls, response.StatusCode)
	}
}

// TestRetryAfterSignedDecimalUsesBackoff rejects signed delay-seconds.
func TestRetryAfterSignedDecimalUsesBackoff(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		calls := 0
		base := roundTripFunc(func(*http.Request) (*http.Response, error) {
			calls++

			return &http.Response{
				StatusCode: http.StatusServiceUnavailable,
				Header:     http.Header{"Retry-After": {"+10"}}, Body: http.NoBody,
			}, nil
		})

		transport := client.Transport(base, func(transport *client.Builder) {
			transport.Attempts(2)
			transport.Backoff(noDelay)
		})

		request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://local.invalid", nil)

		if err != nil {
			t.Fatal(err)
		}

		startedAt := time.Now()

		if _, err := do(t, transport, request); err != nil {
			t.Fatal(err)
		}

		if calls != 2 || !time.Now().Equal(startedAt) {
			t.Fatalf("calls = %d, elapsed = %v; malformed delay was honored", calls, time.Since(startedAt))
		}
	})
}

// retryCountingReader records discarded response bytes without changing native
// read results. Tests can distinguish bounded draining from reading through EOF.
type retryCountingReader struct {
	// Reader supplies more data than the transport's documented drain limit.
	io.Reader

	// bytes records the successfully read byte count, including partial reads.
	bytes int
}

// Read counts bytes while preserving the wrapped reader's count and error.
// It does not add buffering, so tests observe only transport-initiated reads.
func (reader *retryCountingReader) Read(buffer []byte) (int, error) {
	count, err := reader.Reader.Read(buffer)
	reader.bytes += count

	return count, err
}

// requireRetryTrace checks the ordered public callbacks and I/O operations.
// Comparing the whole trace also detects duplicate hooks and unexpected cleanup.
func requireRetryTrace(t *testing.T, actual []string, expected ...string) {
	t.Helper()

	if !slices.Equal(actual, expected) {
		t.Fatalf("retry trace = %v, want %v", actual, expected)
	}
}

// requireRetryLog checks one JSON debug record without its clock timestamp.
// Exact remaining members protect both observation values and metadata privacy.
func requireRetryLog(t *testing.T, logs *bytes.Buffer, expected map[string]any) {
	t.Helper()

	var actual map[string]any

	if err := json.Unmarshal(logs.Bytes(), &actual); err != nil {
		t.Fatal(err)
	}

	delete(actual, "time")

	if !reflect.DeepEqual(actual, expected) {
		t.Fatalf("retry log = %#v, want %#v", actual, expected)
	}
}

// consumeRetryRequest implements the native transport's request-body ownership
// in replay fixtures. Every sent body must contain the same payload and close.
func consumeRetryRequest(t *testing.T, request *http.Request) {
	t.Helper()

	data, err := io.ReadAll(request.Body)

	if err != nil {
		t.Fatal(err)
	}

	if err := request.Body.Close(); err != nil {
		t.Fatal(err)
	}

	if string(data) != "payload" {
		t.Fatalf("sent body = %q, want payload", data)
	}
}

// TestRetryCharacterizationReplayOrder fixes the pre-refactor sequence: policy,
// backoff, logging, bounded draining/closure, waiting, then rewind and resend.
// Failed cleanup must not prevent the resend or consume the returned body.
func TestRetryCharacterizationReplayOrder(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		// drainedBytes is the documented four-KiB response reuse budget.
		const drainedBytes = 4 << 10
		var trace []string
		calls, rewinds, closes := 0, 0, 0
		reader := &retryCountingReader{Reader: strings.NewReader(strings.Repeat("x", 2*drainedBytes))}
		finalReader := &retryCountingReader{Reader: strings.NewReader("returned body")}
		final := &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(finalReader)}

		request, err := http.NewRequestWithContext(t.Context(), http.MethodPut,
			"http://user:secret@api.example/private?token=secret", strings.NewReader("payload"))

		if err != nil {
			t.Fatal(err)
		}

		request.GetBody = func() (io.ReadCloser, error) {
			rewinds++
			trace = append(trace, "rewind")

			return io.NopCloser(strings.NewReader("payload")), nil
		}

		base := roundTripFunc(func(outgoing *http.Request) (*http.Response, error) {
			if outgoing == request || outgoing.Header.Get("X-Attempt") != "" {
				t.Fatal("request was not independently cloned")
			}

			outgoing.Header.Set("X-Attempt", "native mutation")
			consumeRetryRequest(t, outgoing)
			calls++
			trace = append(trace, fmt.Sprintf("send%d", calls))

			if calls != 1 {
				return final, nil
			}

			return &http.Response{
				StatusCode: http.StatusServiceUnavailable,
				Body: &upgradeReadCloser{Reader: reader, close: func() error {
					trace = append(trace, "close")
					closes++

					return errors.New("discarded close failure")
				}},
			}, nil
		})

		logs := &bytes.Buffer{}
		logger := slog.New(slog.NewJSONHandler(upgradeWriterFunc(func(data []byte) (int, error) {
			trace = append(trace, "log")

			return logs.Write(data)
		}), &slog.HandlerOptions{Level: slog.LevelDebug}))

		transport := client.Transport(base, func(transport *client.Builder) {
			transport.Attempts(2)
			transport.AttemptTimeout(time.Hour)
			transport.Logger(logger)
			transport.RetryIf(func(response *http.Response, _ error) bool {
				trace = append(trace, fmt.Sprintf("policy%d", calls))

				return response.StatusCode == http.StatusServiceUnavailable
			})
			transport.Backoff(func(number int) time.Duration {
				trace = append(trace, fmt.Sprintf("backoff%d", number))

				return 2 * time.Second
			})
		})

		startedAt := time.Now()

		response, err := do(t, transport, request)

		if err != nil {
			t.Fatal(err)
		}

		if response != final {
			t.Fatal("returned response identity changed")
		}

		requireRetryTrace(t, trace, "send1", "policy1", "backoff2", "log", "close", "rewind", "send2", "policy2")

		if counts := [3]int{calls, rewinds, closes}; counts != [3]int{2, 1, 1} {
			t.Fatalf("send/rewind/close counts = %v", counts)
		}

		if finalReader.bytes != 0 {
			t.Fatalf("returned body bytes read = %d, want zero", finalReader.bytes)
		}

		if time.Since(startedAt) != 2*time.Second {
			t.Fatalf("retry elapsed = %v, want 2s", time.Since(startedAt))
		}

		requireRetryLog(t, logs, map[string]any{
			"level": "DEBUG", "msg": "client: retrying request",
			"http.request.method": http.MethodPut, "server.address": "api.example",
			"attempt": float64(1), "delay": float64(2 * time.Second),
			"http.response.status_code": float64(http.StatusServiceUnavailable),
		})
	})
}

// TestRetryCharacterizationDiscardOwnership isolates the existing bounded-drain
// and attempt-lifetime observations from replay order. Native Close must run
// while the attempt is active, then its context must be canceled before resend.
func TestRetryCharacterizationDiscardOwnership(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		// drainedBytes is the response reuse budget, not the entire native body.
		const drainedBytes = 4 << 10
		reader := &retryCountingReader{Reader: strings.NewReader(strings.Repeat("x", 2*drainedBytes))}
		var attempt context.Context
		closedWhileActive := false
		calls, closes := 0, 0
		transport := client.Transport(roundTripFunc(func(outgoing *http.Request) (*http.Response, error) {
			calls++

			if calls != 1 {
				return &http.Response{StatusCode: http.StatusOK, Body: http.NoBody}, nil
			}

			attempt = outgoing.Context()

			return &http.Response{
				StatusCode: http.StatusServiceUnavailable,
				Body: &upgradeReadCloser{Reader: reader, close: func() error {
					closes++
					closedWhileActive = attempt.Err() == nil

					return nil
				}},
			}, nil
		}), func(transport *client.Builder) {
			transport.Attempts(2)
			transport.AttemptTimeout(time.Hour)
			transport.Backoff(noDelay)
		})

		request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://api.example", nil)

		if err != nil {
			t.Fatal(err)
		}

		if _, err := do(t, transport, request); err != nil {
			t.Fatal(err)
		}

		if [3]int{calls, closes, reader.bytes} != [3]int{2, 1, drainedBytes} {
			t.Fatalf("send/close/drain counts = %d/%d/%d", calls, closes, reader.bytes)
		}

		if !closedWhileActive || !errors.Is(attempt.Err(), context.Canceled) {
			t.Fatal("native Close/attempt cancellation ordering changed")
		}
	})
}

// characterizeStoppedRetryAfter verifies the common ownership and hook behavior
// when a valid Retry-After exceeds either the fixed cap or the caller's budget.
// A rejected wait still calls Backoff first, but never logs, drains, or closes.
func characterizeStoppedRetryAfter(t *testing.T, header string, budget time.Duration) {
	t.Helper()

	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithTimeout(t.Context(), budget)
		t.Cleanup(cancel)

		var trace []string
		var failed []client.RequestFailedEvent
		bus := event.New()

		unsubscribe, err := bus.Listen(client.RequestFailed,
			func(_ context.Context, payload client.RequestFailedEvent) error {
				trace = append(trace, "event")
				failed = append(failed, payload)

				return nil
			})

		if err != nil {
			t.Fatal(err)
		}

		t.Cleanup(unsubscribe)

		reader := &retryCountingReader{Reader: strings.NewReader("caller-owned response")}
		closes := 0
		native := &http.Response{
			StatusCode: http.StatusServiceUnavailable,
			Header:     http.Header{"Retry-After": {header}},
			Body: &upgradeReadCloser{Reader: reader, close: func() error {
				closes++

				return nil
			}},
		}

		logs := &bytes.Buffer{}
		transport := client.Transport(roundTripFunc(func(*http.Request) (*http.Response, error) {
			trace = append(trace, "send")

			return native, nil
		}), func(transport *client.Builder) {
			transport.Events(bus)
			transport.Logger(slog.New(slog.NewJSONHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
			transport.RetryIf(func(*http.Response, error) bool {
				trace = append(trace, "policy")

				return true
			})
			transport.Backoff(func(int) time.Duration {
				trace = append(trace, "backoff")

				return 4 * time.Second
			})
		})

		request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://api.example/private", nil)

		if err != nil {
			t.Fatal(err)
		}

		startedAt := time.Now()

		response, err := do(t, transport, request)

		if err != nil {
			t.Fatal(err)
		}

		if response != native || !time.Now().Equal(startedAt) {
			t.Fatal("stopped Retry-After changed the returned response or waited")
		}

		requireRetryTrace(t, trace, "send", "policy", "backoff", "event")

		if [3]int{reader.bytes, closes, logs.Len()} != [3]int{} {
			t.Fatal("stopped Retry-After consumed, closed, or logged a retry")
		}

		expected := []client.RequestFailedEvent{{
			Method: http.MethodGet, Host: "api.example", Attempts: 1,
			StatusCode: http.StatusServiceUnavailable,
		}}

		if !reflect.DeepEqual(failed, expected) {
			t.Fatalf("failure events = %+v, want %+v", failed, expected)
		}
	})
}

// TestRetryCharacterizationRetryAfterCap retains the response rather than
// transferring its ownership to cleanup when the fixed wait cap is exceeded.
func TestRetryCharacterizationRetryAfterCap(t *testing.T) {
	t.Parallel()

	characterizeStoppedRetryAfter(t, "11", time.Hour)
}

// TestRetryCharacterizationRetryAfterDeadline exercises the distinct caller
// deadline boundary with the same observable final-response ownership contract.
func TestRetryCharacterizationRetryAfterDeadline(t *testing.T) {
	t.Parallel()

	characterizeStoppedRetryAfter(t, "2", time.Second)
}

// TestRetryCharacterizationCancellationKeepsNativeResult characterizes a native
// transport that completes while the caller cancels: return its result without
// calling policy/backoff, publishing failure, or consuming the response body.
func TestRetryCharacterizationCancellationKeepsNativeResult(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)

	bus, failed := failedRequests(t)
	reader := &retryCountingReader{Reader: strings.NewReader("returned after cancellation")}
	native := &http.Response{StatusCode: http.StatusServiceUnavailable, Body: io.NopCloser(reader)}
	transport := client.Transport(roundTripFunc(func(*http.Request) (*http.Response, error) {
		cancel()

		return native, nil
	}), func(transport *client.Builder) {
		transport.Events(bus)
		transport.RetryIf(func(*http.Response, error) bool {
			t.Fatal("RetryIf called after native completion canceled the caller")

			return false
		})
		transport.Backoff(func(int) time.Duration {
			t.Fatal("Backoff called after cancellation")

			return 0
		})
	})

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://api.example", nil)

	if err != nil {
		t.Fatal(err)
	}

	response, err := transport.RoundTrip(request)

	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		// The caller still owns the native result even though its context is done.
		_ = response.Body.Close()
	})

	if response != native || reader.bytes != 0 || len(*failed) != 0 {
		t.Fatal("canceled native result was replaced, consumed, or reported as a failure")
	}
}

// TestRetryCharacterizationRejectedErrorStillPublishes keeps failure publication
// independent of RetryIf's refusal to retry a native transport error.
func TestRetryCharacterizationRejectedErrorStillPublishes(t *testing.T) {
	t.Parallel()

	expected := errors.New("native transport failure")
	bus, failed := failedRequests(t)
	transport := client.Transport(roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, expected
	}), func(transport *client.Builder) {
		transport.Events(bus)
		transport.RetryIf(func(*http.Response, error) bool {
			return false
		})
		transport.Backoff(func(int) time.Duration {
			t.Fatal("Backoff called after policy rejected a retry")

			return 0
		})
	})

	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://api.example", nil)

	if err != nil {
		t.Fatal(err)
	}

	response, err := transport.RoundTrip(request)

	if response != nil || !errors.Is(err, expected) {
		t.Fatalf("native failure result = %v, %v", response, err)
	}

	if len(*failed) != 1 {
		t.Fatalf("failure events = %d, want one", len(*failed))
	}

	payload := (*failed)[0]

	if payload.Attempts != 1 || payload.StatusCode != 0 || !errors.Is(payload.Err, expected) {
		t.Fatalf("failure payload = %+v", payload)
	}
}

// TestRetryCharacterizationRewindFailureCountsAttempt distinguishes attempt
// identity from native-send count: rewind failure is attempt two and is reported
// with its wrapped cause, without sending or retrying a third native request.
func TestRetryCharacterizationRewindFailureCountsAttempt(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		expected := errors.New("rewind unavailable")
		bus, failed := failedRequests(t)
		calls, closes := 0, 0
		transport := client.Transport(roundTripFunc(func(outgoing *http.Request) (*http.Response, error) {
			calls++
			consumeRetryRequest(t, outgoing)

			return &http.Response{
				StatusCode: http.StatusServiceUnavailable,
				Body: &upgradeReadCloser{Reader: strings.NewReader("discarded"), close: func() error {
					closes++

					return nil
				}},
			}, nil
		}), func(transport *client.Builder) {
			transport.Attempts(2)
			transport.Events(bus)
			transport.Backoff(noDelay)
		})

		request, err := http.NewRequestWithContext(t.Context(), http.MethodPut,
			"http://api.example", strings.NewReader("payload"))

		if err != nil {
			t.Fatal(err)
		}

		request.GetBody = func() (io.ReadCloser, error) {
			return nil, expected
		}

		response, err := transport.RoundTrip(request)

		if response != nil || !errors.Is(err, expected) {
			t.Fatalf("rewind result = %v, %v", response, err)
		}

		if counts := [3]int{calls, closes, len(*failed)}; counts != [3]int{1, 1, 1} {
			t.Fatalf("native-send/close/event counts = %v", counts)
		}

		payload := (*failed)[0]

		if payload.Attempts != 2 || !errors.Is(payload.Err, expected) {
			t.Fatalf("rewind failure payload = %+v", payload)
		}
	})
}

// TestRetryCharacterizationErrorLogUsesTypeOnly preserves the transport-error
// retry record's schema without logging native error text or adding a status.
func TestRetryCharacterizationErrorLogUsesTypeOnly(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		expected := errors.New("private diagnostic must not be logged")
		logs := &bytes.Buffer{}
		calls := 0
		transport := client.Transport(roundTripFunc(func(*http.Request) (*http.Response, error) {
			calls++

			if calls == 1 {
				return nil, expected
			}

			return &http.Response{StatusCode: http.StatusOK, Body: http.NoBody}, nil
		}), func(transport *client.Builder) {
			transport.Attempts(2)
			transport.Backoff(noDelay)
			transport.Logger(slog.New(slog.NewJSONHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
		})

		request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://api.example", nil)

		if err != nil {
			t.Fatal(err)
		}

		if _, err := do(t, transport, request); err != nil {
			t.Fatal(err)
		}

		requireRetryLog(t, logs, map[string]any{
			"level": "DEBUG", "msg": "client: retrying request",
			"http.request.method": http.MethodGet, "server.address": "api.example",
			"attempt": float64(1), "delay": float64(0), "error.type": fmt.Sprintf("%T", expected),
		})
	})
}

// characterizeRetryAfterWait observes the selected delay through virtual time.
// Backoff still runs once when Retry-After overrides it; other statuses ignore
// that header even when a custom RetryIf permits their retry.
func characterizeRetryAfterWait(t *testing.T, status int, expected time.Duration) {
	t.Helper()

	synctest.Test(t, func(t *testing.T) {
		calls, backoffs := 0, 0
		transport := client.Transport(roundTripFunc(func(*http.Request) (*http.Response, error) {
			calls++

			if calls == 1 {
				return &http.Response{
					StatusCode: status, Body: http.NoBody,
					Header: http.Header{"Retry-After": {"2"}},
				}, nil
			}

			return &http.Response{StatusCode: http.StatusOK, Body: http.NoBody}, nil
		}), func(transport *client.Builder) {
			transport.Attempts(2)
			transport.RetryIf(func(response *http.Response, _ error) bool {
				return response.StatusCode != http.StatusOK
			})
			transport.Backoff(func(int) time.Duration {
				backoffs++

				return 5 * time.Second
			})
		})

		request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://api.example", nil)

		if err != nil {
			t.Fatal(err)
		}

		startedAt := time.Now()

		response, err := do(t, transport, request)

		if err != nil {
			t.Fatal(err)
		}

		if [3]int{calls, backoffs, response.StatusCode} != [3]int{2, 1, http.StatusOK} {
			t.Fatal("retry call count or final response changed")
		}

		if elapsed := time.Since(startedAt); elapsed != expected {
			t.Fatalf("status %d retry delay = %v, want %v", status, elapsed, expected)
		}
	})
}

// TestRetryCharacterizationTooManyRequestsWait preserves Retry-After precedence
// on 429 responses without skipping the configured Backoff callback.
func TestRetryCharacterizationTooManyRequestsWait(t *testing.T) {
	t.Parallel()

	characterizeRetryAfterWait(t, http.StatusTooManyRequests, 2*time.Second)
}

// TestRetryCharacterizationServiceUnavailableWait covers the second status
// eligible for Retry-After precedence through the public transport.
func TestRetryCharacterizationServiceUnavailableWait(t *testing.T) {
	t.Parallel()

	characterizeRetryAfterWait(t, http.StatusServiceUnavailable, 2*time.Second)
}

// TestRetryCharacterizationOtherStatusUsesBackoff keeps RetryIf extension
// independent of Retry-After's narrower 429/503 applicability rule.
func TestRetryCharacterizationOtherStatusUsesBackoff(t *testing.T) {
	t.Parallel()

	characterizeRetryAfterWait(t, http.StatusInternalServerError, 5*time.Second)
}

// TestRetryCharacterizationCanceledWaitStillCleansUp preserves the order when
// Backoff cancels the caller: log and best-effort cleanup still precede the
// interruptible wait, which returns no response and publishes no failure event.
func TestRetryCharacterizationCanceledWaitStillCleansUp(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		t.Cleanup(cancel)

		bus, failed := failedRequests(t)
		var trace []string
		reader := &retryCountingReader{
			Reader: io.MultiReader(strings.NewReader("prefix"), iotest.ErrReader(errors.New("drain failure"))),
		}
		logs := &bytes.Buffer{}
		logger := slog.New(slog.NewJSONHandler(upgradeWriterFunc(func(data []byte) (int, error) {
			trace = append(trace, "log")

			return logs.Write(data)
		}), &slog.HandlerOptions{Level: slog.LevelDebug}))

		transport := client.Transport(roundTripFunc(func(*http.Request) (*http.Response, error) {
			trace = append(trace, "send")

			return &http.Response{
				StatusCode: http.StatusServiceUnavailable,
				Body: &upgradeReadCloser{Reader: reader, close: func() error {
					trace = append(trace, "close")

					return errors.New("close failure")
				}},
			}, nil
		}), func(transport *client.Builder) {
			transport.Events(bus)
			transport.Logger(logger)
			transport.RetryIf(func(*http.Response, error) bool {
				trace = append(trace, "policy")

				return true
			})
			transport.Backoff(func(int) time.Duration {
				trace = append(trace, "backoff")
				cancel()

				return time.Hour
			})
		})

		request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://api.example", nil)

		if err != nil {
			t.Fatal(err)
		}

		response, err := transport.RoundTrip(request)

		if response != nil || !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled wait result = %v, %v", response, err)
		}

		requireRetryTrace(t, trace, "send", "policy", "backoff", "log", "close")

		if reader.bytes != len("prefix") || len(*failed) != 0 {
			t.Fatal("canceled wait skipped draining or published a failure")
		}
	})
}
