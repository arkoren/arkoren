package cloudflarecaptcha_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"arkoren.dev/driver/cloudflare/cloudflarecaptcha"
)

// ExampleVerifier_Verify demonstrates successful verification without external
// network access. Production callers use their widget token and server secret.
func ExampleVerifier_Verify() {
	client := &http.Client{Transport: transport(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(validResponse))}, nil
	})}
	verifier, err := cloudflarecaptcha.New("server-secret", []string{"example.com"}, func(connection *cloudflarecaptcha.Builder) {
		connection.HTTPClient(client)
	})

	if err != nil {
		fmt.Println("invalid configuration")

		return
	}

	evidence, err := verifier.Verify(context.Background(), cloudflarecaptcha.Input{
		Token: "widget-token", ExpectedAction: "signup",
	})

	if err != nil {
		fmt.Println("verification failed")

		return
	}

	fmt.Println(evidence.Hostname, evidence.Action)
	// Output: example.com signup
}

// transport substitutes deterministic wire behavior without real network access.
type transport func(*http.Request) (*http.Response, error)

// RoundTrip delegates one request to the test's wire assertion.
func (transport transport) RoundTrip(request *http.Request) (*http.Response, error) {
	return transport(request)
}

// validResponse is provider-native evidence shared by success scenarios.
const validResponse = `{"success":true,"hostname":"example.com","action":"signup","challenge_ts":"2026-10-24T10:00:00Z","cdata":"opaque"}`

// fixture constructs a verifier over a fixed HTTP response. Each call owns its
// client and settings, allowing independent tests to run in parallel.
func fixture(t *testing.T, body string) *cloudflarecaptcha.Verifier {
	t.Helper()

	verifier, err := cloudflarecaptcha.New("server-secret", []string{"example.com"}, func(connection *cloudflarecaptcha.Builder) {
		connection.HTTPClient(&http.Client{Transport: transport(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body))}, nil
		})})
	})

	if err != nil {
		t.Fatal(err)
	}

	return verifier
}

// TestNewNoIOAndDetachedDraft verifies publication, positional slice ownership,
// final-state validation, and synchronous ordered callbacks without wire traffic.
func TestNewNoIOAndDetachedDraft(t *testing.T) {
	t.Parallel()

	hostnames := []string{"example.com"}
	var retained *cloudflarecaptcha.Builder
	calls := 0
	client := &http.Client{Transport: transport(func(*http.Request) (*http.Response, error) {
		calls++

		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(validResponse))}, nil
	})}
	order := ""

	verifier, err := cloudflarecaptcha.New("secret", hostnames, func(connection *cloudflarecaptcha.Builder) {
		order += "first"
		retained = connection
		connection.HTTPClient(client).Timeout(-time.Second)
	}, func(connection *cloudflarecaptcha.Builder) {
		order += "second"
		connection.Timeout(time.Second)
	})

	if err != nil || calls != 0 || order != "firstsecond" {
		t.Fatalf("construction: %v, calls=%d, order=%s", err, calls, order)
	}

	hostnames[0] = "attacker.example"
	retained.HTTPClient(nil).Timeout(-time.Second)
	client.Transport = transport(func(*http.Request) (*http.Response, error) {
		t.Fatal("published verifier retained mutable client fields")

		return nil, errors.New("unexpected")
	})

	_, err = verifier.Verify(t.Context(), cloudflarecaptcha.Input{Token: "token"})

	if err != nil || calls != 1 {
		t.Fatalf("verify: %v, calls=%d", err, calls)
	}
}

// TestVerifyWireAndEvidence checks the fixed endpoint, form encoding, optional
// remote IP, deadline, replay prevention and provider-native typed result.
func TestVerifyWireAndEvidence(t *testing.T) {
	t.Parallel()

	calls := 0

	verifier, err := cloudflarecaptcha.New("secret&value", []string{"example.com"}, func(connection *cloudflarecaptcha.Builder) {
		connection.HTTPClient(&http.Client{Transport: transport(func(request *http.Request) (*http.Response, error) {
			calls++

			if request.URL.String() != "https://challenges.cloudflare.com/turnstile/v0/siteverify" {
				t.Fatalf("endpoint: %s", request.URL)
			}

			if request.Method != http.MethodPost || request.GetBody != nil {
				t.Fatal("request must be a nonreplayable POST")
			}

			if request.Header.Get("Content-Type") != "application/x-www-form-urlencoded" {
				t.Fatal("wrong content type")
			}

			if _, ok := request.Context().Deadline(); !ok {
				t.Fatal("missing deadline")
			}

			if err := request.ParseForm(); err != nil {
				t.Fatal(err)
			}

			if request.Form.Encode() != "remoteip=192.0.2.1&response=token%26value&secret=secret%26value" {
				t.Fatal("form not encoded correctly")
			}

			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(validResponse))}, nil
		})})
	})

	if err != nil {
		t.Fatal(err)
	}

	result, err := verifier.Verify(t.Context(), cloudflarecaptcha.Input{
		Token: "token&value", ExpectedAction: "signup", RemoteIP: netip.MustParseAddr("192.0.2.1"),
	})

	if err != nil || calls != 1 {
		t.Fatalf("verify: %v calls=%d", err, calls)
	}

	if result.Hostname != "example.com" || result.Action != "signup" || result.CData != "opaque" {
		t.Fatalf("evidence: %#v", result)
	}

	if !result.ChallengeAt.Equal(time.Date(2026, 10, 24, 10, 0, 0, 0, time.UTC)) {
		t.Fatalf("challenge timestamp: %v", result.ChallengeAt)
	}
}

// TestVerifyVisitorRejection covers the provider's documented token failures.
func TestVerifyVisitorRejection(t *testing.T) {
	t.Parallel()

	for _, code := range []string{"invalid-input-response", "missing-input-response", "timeout-or-duplicate"} {
		verifier := fixture(t, `{"success":false,"error-codes":["`+code+`"]}`)

		result, err := verifier.Verify(t.Context(), cloudflarecaptcha.Input{Token: "token"})

		if !errors.Is(err, cloudflarecaptcha.ErrRejected) || result != (cloudflarecaptcha.Result{}) {
			t.Fatalf("code %s: %#v, %v", code, result, err)
		}
	}
}

// TestVerifyCredentialsFault distinguishes invalid server credentials from a
// visitor rejection, including mixed provider code responses.
func TestVerifyCredentialsFault(t *testing.T) {
	t.Parallel()

	for _, code := range []string{"missing-input-secret", "invalid-input-secret", "bad-request"} {
		verifier := fixture(t, `{"success":false,"error-codes":["internal-error","`+code+`"]}`)

		_, err := verifier.Verify(t.Context(), cloudflarecaptcha.Input{Token: "token"})

		if !errors.Is(err, cloudflarecaptcha.ErrConfiguration) {
			t.Fatalf("code %s: %v", code, err)
		}
	}
}

// TestVerifyWrongHostname proves provider success alone cannot authorize.
func TestVerifyWrongHostname(t *testing.T) {
	t.Parallel()

	verifier := fixture(t, strings.ReplaceAll(validResponse, "example.com", "attacker.example"))

	_, err := verifier.Verify(t.Context(), cloudflarecaptcha.Input{Token: "token"})

	if !errors.Is(err, cloudflarecaptcha.ErrRejected) {
		t.Fatal(err)
	}
}

// TestVerifyMissingHostname proves missing origin evidence cannot authorize.
func TestVerifyMissingHostname(t *testing.T) {
	t.Parallel()

	verifier := fixture(t, strings.ReplaceAll(validResponse, `"hostname":"example.com",`, ""))

	_, err := verifier.Verify(t.Context(), cloudflarecaptcha.Input{Token: "token"})

	if !errors.Is(err, cloudflarecaptcha.ErrRejected) {
		t.Fatal(err)
	}
}

// TestVerifyWrongAction enforces the server-selected operation boundary.
func TestVerifyWrongAction(t *testing.T) {
	t.Parallel()

	_, err := fixture(t, validResponse).Verify(t.Context(), cloudflarecaptcha.Input{Token: "token", ExpectedAction: "login"})

	if !errors.Is(err, cloudflarecaptcha.ErrRejected) {
		t.Fatal(err)
	}
}

// TestVerifyMissingAction rejects absent actions when server policy requires one.
func TestVerifyMissingAction(t *testing.T) {
	t.Parallel()

	verifier := fixture(t, strings.ReplaceAll(validResponse, `"action":"signup",`, ""))

	_, err := verifier.Verify(t.Context(), cloudflarecaptcha.Input{Token: "token", ExpectedAction: "signup"})

	if !errors.Is(err, cloudflarecaptcha.ErrRejected) {
		t.Fatal(err)
	}
}

// TestVerifyMalformedResponses checks fail-closed decoding and bounded parsing.
func TestVerifyMalformedResponses(t *testing.T) {
	t.Parallel()

	for _, body := range []string{
		`null`, `{}`, `[]`, `{"success":null}`, `{"success":"true"}`, `{"success":true}`,
		`{"success":false}`, `{"success":false,"error-codes":["future-error"]}`,
		`{"success":false,"error-codes":["internal-error"]}`,
		strings.ReplaceAll(validResponse, `"success":true`, `"success":true,"error-codes":["internal-error"]`),
		strings.ReplaceAll(validResponse, "2026-10-24T10:00:00Z", "not-a-date"),
		validResponse + `{}`, strings.Repeat(" ", 65537),
		`{"success":false,"success":true}`, `{"success":true,"hostname":null}`,
	} {
		result, err := fixture(t, body).Verify(t.Context(), cloudflarecaptcha.Input{Token: "token"})

		if !errors.Is(err, cloudflarecaptcha.ErrUnavailable) || result != (cloudflarecaptcha.Result{}) {
			t.Fatalf("body %.60q: %#v, %v", body, result, err)
		}
	}
}

// TestVerifyInputBounds checks no I/O occurs for invalid local token bounds.
func TestVerifyInputBounds(t *testing.T) {
	t.Parallel()

	verifier, err := cloudflarecaptcha.New("secret", []string{"example.com"}, func(connection *cloudflarecaptcha.Builder) {
		connection.HTTPClient(&http.Client{Transport: transport(func(*http.Request) (*http.Response, error) {
			t.Fatal("invalid input reached network")

			return nil, errors.New("unexpected")
		})})
	})

	if err != nil {
		t.Fatal(err)
	}

	for _, token := range []string{"", strings.Repeat("x", 2049)} {
		_, err := verifier.Verify(t.Context(), cloudflarecaptcha.Input{Token: token})

		if !errors.Is(err, cloudflarecaptcha.ErrRejected) {
			t.Fatal(err)
		}
	}
}

// TestVerifyMaximumToken accepts the inclusive 2048-byte boundary.
func TestVerifyMaximumToken(t *testing.T) {
	t.Parallel()

	_, err := fixture(t, validResponse).Verify(t.Context(), cloudflarecaptcha.Input{Token: strings.Repeat("x", 2048)})

	if err != nil {
		t.Fatal(err)
	}
}

// TestVerifyUpstreamStatuses checks server/status failures never authorize.
func TestVerifyUpstreamStatuses(t *testing.T) {
	t.Parallel()

	for _, status := range []int{http.StatusTooManyRequests, http.StatusInternalServerError, http.StatusBadGateway, http.StatusBadRequest} {
		verifier, err := cloudflarecaptcha.New("secret", []string{"example.com"}, func(connection *cloudflarecaptcha.Builder) {
			connection.HTTPClient(&http.Client{Transport: transport(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(validResponse))}, nil
			})})
		})

		if err != nil {
			t.Fatal(err)
		}

		_, err = verifier.Verify(t.Context(), cloudflarecaptcha.Input{Token: "token"})

		if !errors.Is(err, cloudflarecaptcha.ErrUnavailable) {
			t.Fatalf("status %d: %v", status, err)
		}
	}
}

// TestVerifyAuthStatuses classifies HTTP authentication failures as server faults.
func TestVerifyAuthStatuses(t *testing.T) {
	t.Parallel()

	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden} {
		verifier, err := cloudflarecaptcha.New("secret", []string{"example.com"}, func(connection *cloudflarecaptcha.Builder) {
			connection.HTTPClient(&http.Client{Transport: transport(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(validResponse))}, nil
			})})
		})

		if err != nil {
			t.Fatal(err)
		}

		_, err = verifier.Verify(t.Context(), cloudflarecaptcha.Input{Token: "token"})

		if !errors.Is(err, cloudflarecaptcha.ErrConfiguration) {
			t.Fatal(err)
		}
	}
}

// TestVerifyNoRedirectOrRetry ensures borrowed redirect hooks cannot leak form
// credentials and no second verification attempt is made.
func TestVerifyNoRedirectOrRetry(t *testing.T) {
	t.Parallel()

	calls := 0
	client := &http.Client{
		Transport: transport(func(*http.Request) (*http.Response, error) {
			calls++

			return &http.Response{StatusCode: http.StatusTemporaryRedirect,
				Header: http.Header{"Location": {"https://attacker.example/"}},
				Body:   io.NopCloser(strings.NewReader(""))}, nil
		}),
		CheckRedirect: func(*http.Request, []*http.Request) error {
			t.Fatal("borrowed redirect hook called")

			return nil
		},
	}

	verifier, err := cloudflarecaptcha.New("secret", []string{"example.com"}, func(connection *cloudflarecaptcha.Builder) {
		connection.HTTPClient(client)
	})

	if err != nil {
		t.Fatal(err)
	}

	_, err = verifier.Verify(t.Context(), cloudflarecaptcha.Input{Token: "token"})

	if !errors.Is(err, cloudflarecaptcha.ErrUnavailable) || calls != 1 {
		t.Fatalf("error=%v calls=%d", err, calls)
	}

	if client.CheckRedirect == nil {
		t.Fatal("borrowed client mutated")
	}
}

// TestVerifyCancellationAndRedaction preserves cancellation and transport cause
// identity without including secret, token or raw native diagnostics in Error.
func TestVerifyCancellationAndRedaction(t *testing.T) {
	t.Parallel()

	cause := errors.New("server-secret token-secret raw response")
	calls := 0

	verifier, err := cloudflarecaptcha.New("server-secret", []string{"example.com"}, func(connection *cloudflarecaptcha.Builder) {
		connection.HTTPClient(&http.Client{Transport: transport(func(request *http.Request) (*http.Response, error) {
			calls++

			return nil, errors.Join(cause, request.Context().Err())
		})})
	})

	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	_, err = verifier.Verify(ctx, cloudflarecaptcha.Input{Token: "token-secret"})

	if !errors.Is(err, context.Canceled) || !errors.Is(err, cause) || !errors.Is(err, cloudflarecaptcha.ErrUnavailable) {
		t.Fatalf("lost error identity: %v", err)
	}

	if strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "raw response") || calls != 1 {
		t.Fatalf("unsafe error=%v calls=%d", err, calls)
	}
}

// TestVerifyExpiredDeadline is deterministic and performs no real-time sleeps.
func TestVerifyExpiredDeadline(t *testing.T) {
	t.Parallel()

	verifier, err := cloudflarecaptcha.New("secret", []string{"example.com"}, func(connection *cloudflarecaptcha.Builder) {
		connection.HTTPClient(&http.Client{Transport: transport(func(request *http.Request) (*http.Response, error) {
			return nil, request.Context().Err()
		})})
	})

	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithDeadline(t.Context(), time.Unix(1, 0))
	t.Cleanup(cancel)

	_, err = verifier.Verify(ctx, cloudflarecaptcha.Input{Token: "token"})

	if !errors.Is(err, context.DeadlineExceeded) || !errors.Is(err, cloudflarecaptcha.ErrUnavailable) {
		t.Fatal(err)
	}
}

// TestVerifyConcurrent uses one published verifier across independent requests.
func TestVerifyConcurrent(t *testing.T) {
	t.Parallel()

	verifier := fixture(t, validResponse)
	var workers sync.WaitGroup
	var failures atomic.Int32

	for range 16 {
		workers.Go(func() {
			_, err := verifier.Verify(t.Context(), cloudflarecaptcha.Input{Token: "token"})

			if err != nil {
				failures.Add(1)
			}
		})
	}

	workers.Wait()

	if failures.Load() != 0 {
		t.Fatalf("failed calls: %d", failures.Load())
	}
}

// TestNewInvalidConfiguration checks non-sensitive configuration diagnostics.
func TestNewInvalidConfiguration(t *testing.T) {
	t.Parallel()

	_, err := cloudflarecaptcha.New("", []string{"example.com"})

	if !errors.Is(err, cloudflarecaptcha.ErrConfiguration) {
		t.Fatal(err)
	}

	_, err = cloudflarecaptcha.New("secret", nil)

	if !errors.Is(err, cloudflarecaptcha.ErrConfiguration) {
		t.Fatal(err)
	}

	_, err = cloudflarecaptcha.New("secret", []string{"https://example.com"})

	if !errors.Is(err, cloudflarecaptcha.ErrConfiguration) {
		t.Fatal(err)
	}

	_, err = cloudflarecaptcha.New("secret", []string{"example.com"}, func(connection *cloudflarecaptcha.Builder) {
		connection.Timeout(-time.Second)
	})

	if !errors.Is(err, cloudflarecaptcha.ErrConfiguration) {
		t.Fatal(err)
	}
}

// TestNewNilCallbackPanics confirms nil callback misuse is not silently skipped.
func TestNewNilCallbackPanics(t *testing.T) {
	t.Parallel()

	defer func() {
		if recover() == nil {
			t.Fatal("expected programmer misuse panic")
		}
	}()

	_, _ = cloudflarecaptcha.New("secret", []string{"example.com"}, nil)
}

// TestVerifyZeroValueFailsClosed ensures unconstructed verifiers never authorize.
func TestVerifyZeroValueFailsClosed(t *testing.T) {
	t.Parallel()

	var verifier cloudflarecaptcha.Verifier

	_, err := verifier.Verify(t.Context(), cloudflarecaptcha.Input{Token: "token"})

	if !errors.Is(err, cloudflarecaptcha.ErrConfiguration) {
		t.Fatal(err)
	}
}

// TestVerifyConfiguredTimeout uses virtual time to check the complete operation
// deadline without sleeping or depending on wall-clock scheduler timing.
func TestVerifyConfiguredTimeout(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		verifier, err := cloudflarecaptcha.New("secret", []string{"example.com"}, func(connection *cloudflarecaptcha.Builder) {
			connection.Timeout(2 * time.Second)
			connection.HTTPClient(&http.Client{Transport: transport(func(request *http.Request) (*http.Response, error) {
				<-request.Context().Done()

				return nil, request.Context().Err()
			})})
		})

		if err != nil {
			t.Fatal(err)
		}

		startedAt := time.Now()

		_, err = verifier.Verify(t.Context(), cloudflarecaptcha.Input{Token: "token"})

		if !errors.Is(err, context.DeadlineExceeded) || !errors.Is(err, cloudflarecaptcha.ErrUnavailable) {
			t.Fatal(err)
		}

		if time.Since(startedAt) != 2*time.Second {
			t.Fatalf("operation elapsed: %v", time.Since(startedAt))
		}
	})
}

// trackedTransport records lifecycle misuse independently from response-body
// cleanup. The verifier borrows it and must not close its idle connections.
type trackedTransport struct {
	// closed records borrowed transport shutdown attempts.
	closed bool
	// body is owned by the individual verification response.
	body *trackedBody
}

// RoundTrip returns one deterministic body without consuming the request.
func (transport *trackedTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return &http.Response{StatusCode: http.StatusOK, Body: transport.body}, nil
}

// CloseIdleConnections records an action reserved for the transport's owner.
func (transport *trackedTransport) CloseIdleConnections() {
	transport.closed = true
}

// trackedBody supplies a read failure while recording required response cleanup.
type trackedBody struct {
	// cause is returned verbatim to check identity and redacted diagnostics.
	cause error
	// closed records verification-owned response cleanup.
	closed bool
}

// Read fails deterministically without exposing response bytes.
func (body *trackedBody) Read([]byte) (int, error) {
	return 0, body.cause
}

// Close marks body cleanup, independently of the borrowed client's lifecycle.
func (body *trackedBody) Close() error {
	body.closed = true

	return nil
}

// TestVerifyReadErrorAndOwnership preserves native read causes, closes response
// bodies, and leaves the borrowed transport running on failure.
func TestVerifyReadErrorAndOwnership(t *testing.T) {
	t.Parallel()

	cause := errors.New("sensitive-token sensitive-secret raw response")
	wire := &trackedTransport{body: &trackedBody{cause: cause}}

	verifier, err := cloudflarecaptcha.New("sensitive-secret", []string{"example.com"}, func(connection *cloudflarecaptcha.Builder) {
		connection.HTTPClient(&http.Client{Transport: wire})
	})

	if err != nil {
		t.Fatal(err)
	}

	_, err = verifier.Verify(t.Context(), cloudflarecaptcha.Input{Token: "sensitive-token"})

	if !errors.Is(err, cause) || !errors.Is(err, cloudflarecaptcha.ErrUnavailable) {
		t.Fatalf("lost read failure: %v", err)
	}

	if strings.Contains(err.Error(), "sensitive") || strings.Contains(err.Error(), "raw response") {
		t.Fatalf("unsafe diagnostics: %v", err)
	}

	if !wire.body.closed || wire.closed {
		t.Fatalf("body closed=%t transport closed=%t", wire.body.closed, wire.closed)
	}
}
