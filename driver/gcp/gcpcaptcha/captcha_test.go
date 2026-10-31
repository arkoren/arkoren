package gcpcaptcha_test

import (
	"context"
	"encoding/json/v2"
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

	"golang.org/x/oauth2"

	"arkoren.dev/driver/gcp/gcpcaptcha"
)

// readFunc controls the successful-read boundary without sleeps or real I/O.
type readFunc func([]byte) (int, error)

// Read delegates one controlled provider-body read.
func (reader readFunc) Read(buffer []byte) (int, error) {
	return reader(buffer)
}

// TestVerifyPreCanceledNeverCallsTransport rejects before any network attempt,
// even when a supplied transport would incorrectly ignore request cancellation.
func TestVerifyPreCanceledNeverCallsTransport(t *testing.T) {
	t.Parallel()

	verifier := newVerifier(t, roundTrip(func(*http.Request) (*http.Response, error) {
		t.Error("transport called for a pre-canceled request")

		return response(http.StatusOK, validResponse), nil
	}))
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	assessment, err := verifier.Verify(ctx, gcpcaptcha.Input{Token: "token"})

	if !errors.Is(err, context.Canceled) || !errors.Is(err, gcpcaptcha.ErrUnavailable) {
		t.Fatalf("cancellation classification lost: %v", err)
	}

	if assessment.Score != 0 || errors.Is(err, gcpcaptcha.ErrRejected) {
		t.Fatal("cancellation published evidence or visitor rejection")
	}
}

// TestVerifyCanceledSuccessfulTransportCannotAuthorize checks a transport that
// returns a valid response while canceling the caller's context.
func TestVerifyCanceledSuccessfulTransportCannotAuthorize(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)

	verifier := newVerifier(t, roundTrip(func(*http.Request) (*http.Response, error) {
		cancel()

		return response(http.StatusOK, validResponse), nil
	}))

	assessment, err := verifier.Verify(ctx, gcpcaptcha.Input{Token: "token"})

	if !errors.Is(err, context.Canceled) || !errors.Is(err, gcpcaptcha.ErrUnavailable) {
		t.Fatalf("successful transport bypassed cancellation: %v", err)
	}

	if assessment.Name != "" {
		t.Fatal("canceled verification returned usable evidence")
	}
}

// TestVerifyCanceledSuccessfulReadCannotAuthorize exercises cancellation during
// a body read that nevertheless returns a complete valid assessment and EOF.
func TestVerifyCanceledSuccessfulReadCannotAuthorize(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)

	verifier := newVerifier(t, roundTrip(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(readFunc(func(buffer []byte) (int, error) {
			cancel()

			return copy(buffer, validResponse), io.EOF
		}))}, nil
	}))

	assessment, err := verifier.Verify(ctx, gcpcaptcha.Input{Token: "token"})

	if !errors.Is(err, context.Canceled) || !errors.Is(err, gcpcaptcha.ErrUnavailable) {
		t.Fatalf("successful read bypassed cancellation: %v", err)
	}

	if assessment.Name != "" || strings.Contains(err.Error(), "secret") {
		t.Fatal("canceled read published evidence or diagnostics")
	}
}

// TestVerifyDeadlineDuringSuccessfulReadCannotAuthorize controls virtual time
// while a reader returns success after the request's ten-second deadline.
func TestVerifyDeadlineDuringSuccessfulReadCannotAuthorize(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		verifier := newVerifier(t, roundTrip(func(request *http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(readFunc(func(buffer []byte) (int, error) {
				<-request.Context().Done()

				return copy(buffer, validResponse), io.EOF
			}))}, nil
		}))

		startedAt := time.Now()
		assessment, err := verifier.Verify(t.Context(), gcpcaptcha.Input{Token: "token"})

		if !errors.Is(err, context.DeadlineExceeded) || !errors.Is(err, gcpcaptcha.ErrUnavailable) {
			t.Fatalf("successful read bypassed deadline: %v", err)
		}

		if assessment.Name != "" || time.Since(startedAt) != 10*time.Second {
			t.Fatal("deadline returned usable evidence or wrong bound")
		}
	})
}

// TestVerifyOmittedFalseIsRejected uses legal ProtoJSON for a false bool field:
// the nonoptional default is omitted, while the message and reason remain.
func TestVerifyOmittedFalseIsRejected(t *testing.T) {
	t.Parallel()

	_, err := verifyResponse(t, `{"tokenProperties":{"invalidReason":"EXPIRED"}}`)

	if !errors.Is(err, gcpcaptcha.ErrRejected) || errors.Is(err, gcpcaptcha.ErrUnavailable) {
		t.Fatalf("proto3 default false misclassified: %v", err)
	}
}

// TestVerifyUnknownInvalidReasonIsRejected needs no recognized reason code when
// the present tokenProperties message carries default false validity.
func TestVerifyUnknownInvalidReasonIsRejected(t *testing.T) {
	t.Parallel()

	_, err := verifyResponse(t, `{"tokenProperties":{"invalidReason":"UNKNOWN_INVALID_REASON"}}`)

	if !errors.Is(err, gcpcaptcha.ErrRejected) || errors.Is(err, gcpcaptcha.ErrUnavailable) {
		t.Fatalf("unknown invalid reason misclassified: %v", err)
	}
}

// TestVerifyEmptyTokenPropertiesIsRejected defaults a present empty message's
// validity to false, unlike an absent tokenProperties message.
func TestVerifyEmptyTokenPropertiesIsRejected(t *testing.T) {
	t.Parallel()

	_, err := verifyResponse(t, `{"tokenProperties":{}}`)

	if !errors.Is(err, gcpcaptcha.ErrRejected) || errors.Is(err, gcpcaptcha.ErrUnavailable) {
		t.Fatalf("empty tokenProperties misclassified: %v", err)
	}
}

// TestVerifyNullValidityIsRejected follows ProtoJSON's unset/default-false rule.
func TestVerifyNullValidityIsRejected(t *testing.T) {
	t.Parallel()

	_, err := verifyResponse(t, `{"tokenProperties":{"valid":null}}`)

	if !errors.Is(err, gcpcaptcha.ErrRejected) || errors.Is(err, gcpcaptcha.ErrUnavailable) {
		t.Fatalf("null validity misclassified: %v", err)
	}
}

// TestVerifyInvalidValidityTypeIsUnavailable never coerces a string into true.
func TestVerifyInvalidValidityTypeIsUnavailable(t *testing.T) {
	t.Parallel()

	_, err := verifyResponse(t, `{"tokenProperties":{"valid":"true"}}`)

	if !errors.Is(err, gcpcaptcha.ErrUnavailable) {
		t.Fatalf("malformed bool accepted: %v", err)
	}
}

// TestVerifyNullTokenPropertiesIsUnavailable preserves message presence, which
// differs from an omitted nonoptional scalar inside a present message.
func TestVerifyNullTokenPropertiesIsUnavailable(t *testing.T) {
	t.Parallel()

	_, err := verifyResponse(t, `{"tokenProperties":null,"riskAnalysis":{"score":0.9}}`)

	if !errors.Is(err, gcpcaptcha.ErrUnavailable) || errors.Is(err, gcpcaptcha.ErrRejected) {
		t.Fatalf("null token message misclassified: %v", err)
	}
}

// TestVerifyEmptyRiskAnalysisReturnsZero represents an actual zero-score
// ProtoJSON message, whose nonoptional default score is omitted by serializers.
func TestVerifyEmptyRiskAnalysisReturnsZero(t *testing.T) {
	t.Parallel()

	assessment, err := verifyResponse(t, `{"tokenProperties":{"valid":true,"hostname":"shop.example","action":"login"},"riskAnalysis":{}}`)

	if err != nil {
		t.Fatal(err)
	}

	if assessment.Score != 0 || assessment.Action != "login" {
		t.Fatalf("lost proto3 zero or bindings: %#v", assessment)
	}
}

// TestVerifyNullRiskAnalysisIsUnavailable does not invent an enclosing message
// when provider validity is affirmative but risk-analysis evidence is absent.
func TestVerifyNullRiskAnalysisIsUnavailable(t *testing.T) {
	t.Parallel()

	_, err := verifyResponse(t, `{"tokenProperties":{"valid":true,"hostname":"shop.example","action":"login"},"riskAnalysis":null}`)

	if !errors.Is(err, gcpcaptcha.ErrUnavailable) {
		t.Fatalf("null risk message accepted: %v", err)
	}
}

// TestVerifyBadRequestIsConfiguration includes Google's invalid-key/site-key
// INVALID_ARGUMENT response; the adapter owns the complete request shape.
func TestVerifyBadRequestIsConfiguration(t *testing.T) {
	t.Parallel()

	verifier := newVerifier(t, roundTrip(func(*http.Request) (*http.Response, error) {
		return response(http.StatusBadRequest, `{"error":{"status":"INVALID_ARGUMENT","message":"secret API key not valid"}}`), nil
	}))

	_, err := verifier.Verify(t.Context(), gcpcaptcha.Input{Token: "token"})

	if !errors.Is(err, gcpcaptcha.ErrConfiguration) || errors.Is(err, gcpcaptcha.ErrRejected) {
		t.Fatalf("bad request misclassified: %v", err)
	}
}

// TestVerifyMissingProjectIsConfiguration treats an unresolvable configured
// project/assessment route as an operator fault, not an invalid visitor token.
func TestVerifyMissingProjectIsConfiguration(t *testing.T) {
	t.Parallel()

	verifier := newVerifier(t, roundTrip(func(*http.Request) (*http.Response, error) {
		return response(http.StatusNotFound, `{"error":{"status":"NOT_FOUND"}}`), nil
	}))

	_, err := verifier.Verify(t.Context(), gcpcaptcha.Input{Token: "token"})

	if !errors.Is(err, gcpcaptcha.ErrConfiguration) || errors.Is(err, gcpcaptcha.ErrUnavailable) {
		t.Fatalf("missing project misclassified: %v", err)
	}
}

// TestVerifyProviderFaultIsUnavailable keeps server incidents separate from
// configuration, even though invalid-request responses are configuration faults.
func TestVerifyProviderFaultIsUnavailable(t *testing.T) {
	t.Parallel()

	verifier := newVerifier(t, roundTrip(func(*http.Request) (*http.Response, error) {
		return response(http.StatusInternalServerError, `{"error":{"status":"INTERNAL"}}`), nil
	}))

	_, err := verifier.Verify(t.Context(), gcpcaptcha.Input{Token: "token"})

	if !errors.Is(err, gcpcaptcha.ErrUnavailable) || errors.Is(err, gcpcaptcha.ErrConfiguration) {
		t.Fatalf("provider failure misclassified: %v", err)
	}
}

// roundTrip lets tests observe Google HTTP requests without network access.
type roundTrip func(*http.Request) (*http.Response, error)

// RoundTrip invokes the test's deterministic response function.
func (transport roundTrip) RoundTrip(request *http.Request) (*http.Response, error) {
	return transport(request)
}

// validResponse includes selected evidence and ignored future fields/event echo.
const validResponse = `{"name":"projects/123/assessments/a1","tokenProperties":{"valid":true,"hostname":"SHOP.example","action":"login","createTime":"2026-10-24T12:00:00Z","clientSignalsFailed":true},"riskAnalysis":{"score":0.1,"reasons":["AUTOMATION","FUTURE_REASON"],"extendedVerdictReasons":["EXTRA"],"challenge":"NOCAPTCHA"},"event":{"token":"secret-echo"},"future":{"field":true}}`

// response creates a fresh provider body so parallel calls never share readers.
func response(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
}

// newVerifier constructs a preauthenticated fake-client verifier. Its callbacks
// exercise the public constructor without ADC discovery or real network I/O.
func newVerifier(t *testing.T, transport http.RoundTripper) *gcpcaptcha.Verifier {
	t.Helper()

	verifier, err := gcpcaptcha.New(t.Context(), "shop-prod", "site-key", []string{"shop.example"},
		func(access *gcpcaptcha.Builder) {
			access.HTTPClient(&http.Client{Transport: transport})
		})

	if err != nil {
		t.Fatal(err)
	}

	return verifier
}

// verifyResponse isolates response interpretation from test setup.
func verifyResponse(t *testing.T, body string) (gcpcaptcha.Assessment, error) {
	t.Helper()

	verifier := newVerifier(t, roundTrip(func(*http.Request) (*http.Response, error) {
		return response(http.StatusOK, body), nil
	}))

	return verifier.Verify(t.Context(), gcpcaptcha.Input{Token: "browser-token", ExpectedAction: "login"})
}

// TestVerifySendsCurrentAssessment checks auth, fixed destination and signals.
func TestVerifySendsCurrentAssessment(t *testing.T) {
	t.Parallel()

	var calls int
	client := &http.Client{Transport: roundTrip(func(request *http.Request) (*http.Response, error) {
		calls++
		requireGoogleRequest(t, request)

		var body struct {
			Event map[string]string `json:"event"`
		}

		if err := json.UnmarshalRead(request.Body, &body); err != nil {
			t.Fatal(err)
		}

		want := map[string]string{"token": "browser-token", "siteKey": "site-key", "expectedAction": "login", "userIpAddress": "192.0.2.4", "userAgent": "test-device"}

		if fmt.Sprint(body.Event) != fmt.Sprint(want) {
			t.Fatalf("unexpected event: %#v", body.Event)
		}

		return response(http.StatusOK, validResponse), nil
	})}

	verifier, err := gcpcaptcha.New(t.Context(), "shop-prod", "site-key", []string{"shop.example"}, func(access *gcpcaptcha.Builder) {
		access.APIKey("api-secret")
		access.HTTPClient(client)
	})

	if err != nil {
		t.Fatal(err)
	}

	if calls != 0 {
		t.Fatal("constructor performed verification")
	}

	assessment, err := verifier.Verify(t.Context(), gcpcaptcha.Input{Token: "browser-token", ExpectedAction: "login", RemoteIP: netip.MustParseAddr("192.0.2.4"), UserAgent: "test-device"})

	if err != nil {
		t.Fatal(err)
	}

	if calls != 1 || assessment.Score != 0.1 {
		t.Fatalf("calls=%d score=%v", calls, assessment.Score)
	}
}

// requireGoogleRequest checks the protocol security boundary independently of
// event contents: POST, fixed destination, header credentials and no replay body.
func requireGoogleRequest(t *testing.T, request *http.Request) {
	t.Helper()

	if request.Method != http.MethodPost || request.URL.String() != "https://recaptchaenterprise.googleapis.com/v1/projects/shop-prod/assessments" {
		t.Fatalf("unexpected destination: %s %s", request.Method, request.URL)
	}

	if request.Header.Get("X-Goog-Api-Key") != "api-secret" || request.Header.Get("Content-Type") != "application/json" {
		t.Fatal("missing API authentication or JSON content type")
	}

	if request.GetBody != nil {
		t.Fatal("token body must not be replayable")
	}
}

// TestVerifyLowScoreReturnsEvidence proves authenticity is not score policy.
func TestVerifyLowScoreReturnsEvidence(t *testing.T) {
	t.Parallel()

	assessment, err := verifyResponse(t, validResponse)

	if err != nil {
		t.Fatal(err)
	}

	if assessment.Score != 0.1 || assessment.Name != "projects/123/assessments/a1" {
		t.Fatalf("unexpected assessment: %#v", assessment)
	}

	if len(assessment.Reasons) != 2 || assessment.Reasons[1] != "FUTURE_REASON" {
		t.Fatal("reason evidence lost")
	}

	if len(assessment.ExtendedVerdictReasons) != 1 || assessment.Challenge != "NOCAPTCHA" {
		t.Fatal("risk evidence lost")
	}
}

// TestVerifyTokenEvidenceExcludesEcho preserves origin evidence without tokens.
func TestVerifyTokenEvidenceExcludesEcho(t *testing.T) {
	t.Parallel()

	assessment, err := verifyResponse(t, validResponse)

	if err != nil {
		t.Fatal(err)
	}

	if assessment.Action != "login" || !assessment.ClientSignalsFailed || assessment.CreatedAt.IsZero() {
		t.Fatal("token evidence lost")
	}

	if strings.Contains(fmt.Sprint(assessment), "secret-echo") {
		t.Fatal("event token exposed")
	}
}

// TestVerifyWrongHostname rejects even an otherwise valid token.
func TestVerifyWrongHostname(t *testing.T) {
	t.Parallel()

	_, err := verifyResponse(t, strings.ReplaceAll(validResponse, "SHOP.example", "evil.example"))

	if !errors.Is(err, gcpcaptcha.ErrRejected) {
		t.Fatalf("got %v", err)
	}
}

// TestVerifyMissingHostname fails closed rather than trusting provider validity.
func TestVerifyMissingHostname(t *testing.T) {
	t.Parallel()

	_, err := verifyResponse(t, strings.ReplaceAll(validResponse, `"hostname":"SHOP.example",`, ""))

	if !errors.Is(err, gcpcaptcha.ErrRejected) {
		t.Fatalf("got %v", err)
	}
}

// TestVerifyWrongAction performs an exact local check, not only expectedAction.
func TestVerifyWrongAction(t *testing.T) {
	t.Parallel()

	_, err := verifyResponse(t, strings.ReplaceAll(validResponse, `"action":"login"`, `"action":"LOGIN"`))

	if !errors.Is(err, gcpcaptcha.ErrRejected) {
		t.Fatalf("got %v", err)
	}
}

// TestVerifyMissingAction rejects action-capable requests without evidence.
func TestVerifyMissingAction(t *testing.T) {
	t.Parallel()

	_, err := verifyResponse(t, strings.ReplaceAll(validResponse, `"action":"login",`, ""))

	if !errors.Is(err, gcpcaptcha.ErrRejected) {
		t.Fatalf("got %v", err)
	}
}

// TestVerifyCheckboxAllowsEmptyAction supports explicitly actionless widgets.
func TestVerifyCheckboxAllowsEmptyAction(t *testing.T) {
	t.Parallel()

	verifier := newVerifier(t, roundTrip(func(request *http.Request) (*http.Response, error) {
		body, err := io.ReadAll(request.Body)

		if err != nil {
			t.Fatal(err)
		}

		if strings.Contains(string(body), "expectedAction") {
			t.Fatal("empty action sent")
		}

		return response(http.StatusOK, strings.ReplaceAll(validResponse, `"action":"login",`, "")), nil
	}))

	if _, err := verifier.Verify(t.Context(), gcpcaptcha.Input{Token: "token"}); err != nil {
		t.Fatal(err)
	}
}

// TestVerifyInvalidToken is rejection even without a risk analysis.
func TestVerifyInvalidToken(t *testing.T) {
	t.Parallel()

	_, err := verifyResponse(t, `{"tokenProperties":{"valid":false,"invalidReason":"EXPIRED"}}`)

	if !errors.Is(err, gcpcaptcha.ErrRejected) {
		t.Fatalf("got %v", err)
	}
}

// TestVerifyMissingScore honors the nonoptional proto3 float default zero.
func TestVerifyMissingScore(t *testing.T) {
	t.Parallel()

	assessment, err := verifyResponse(t, strings.ReplaceAll(validResponse, `"score":0.1,`, ""))

	if err != nil {
		t.Fatal(err)
	}

	if assessment.Score != 0 {
		t.Fatalf("got score %v", assessment.Score)
	}
}

// TestVerifyNullScore treats scalar null as the same default as omission.
func TestVerifyNullScore(t *testing.T) {
	t.Parallel()

	assessment, err := verifyResponse(t, strings.ReplaceAll(validResponse, `"score":0.1`, `"score":null`))

	if err != nil {
		t.Fatal(err)
	}

	if assessment.Score != 0 {
		t.Fatalf("got score %v", assessment.Score)
	}
}

// TestVerifyOutOfRangeScore rejects numeric evidence outside the global range.
func TestVerifyOutOfRangeScore(t *testing.T) {
	t.Parallel()

	_, err := verifyResponse(t, strings.ReplaceAll(validResponse, `"score":0.1`, `"score":1.1`))

	if !errors.Is(err, gcpcaptcha.ErrUnavailable) {
		t.Fatalf("got %v", err)
	}
}

// TestVerifyMalformedScore rejects a nonnumeric string without exposing its text.
func TestVerifyMalformedScore(t *testing.T) {
	t.Parallel()

	_, err := verifyResponse(t, strings.ReplaceAll(validResponse, `"score":0.1`, `"score":"secret-token"`))

	if !errors.Is(err, gcpcaptcha.ErrUnavailable) {
		t.Fatalf("got %v", err)
	}

	if strings.Contains(err.Error(), "secret-token") {
		t.Fatal("decoder exposed token")
	}
}

// TestVerifyUnknownResponse rejects absent validity/risk structures.
func TestVerifyUnknownResponse(t *testing.T) {
	t.Parallel()

	_, err := verifyResponse(t, `{"unknown":true}`)

	if !errors.Is(err, gcpcaptcha.ErrUnavailable) {
		t.Fatalf("got %v", err)
	}
}

// TestVerifyMalformedResponse never renders provider body diagnostics.
func TestVerifyMalformedResponse(t *testing.T) {
	t.Parallel()

	_, err := verifyResponse(t, `secret-token-not-json`)

	if !errors.Is(err, gcpcaptcha.ErrUnavailable) {
		t.Fatalf("got %v", err)
	}

	if strings.Contains(err.Error(), "secret-token") {
		t.Fatal("body exposed")
	}
}

// TestVerifyOversizedResponse enforces a one MiB bounded read.
func TestVerifyOversizedResponse(t *testing.T) {
	t.Parallel()

	_, err := verifyResponse(t, strings.Repeat(" ", (1<<20)+1)+validResponse)

	if !errors.Is(err, gcpcaptcha.ErrUnavailable) {
		t.Fatalf("got %v", err)
	}
}

// TestVerifyEmptyAndOversizedTokens never contacts Google for local rejection.
func TestVerifyEmptyAndOversizedTokens(t *testing.T) {
	t.Parallel()

	verifier := newVerifier(t, roundTrip(func(*http.Request) (*http.Response, error) {
		t.Fatal("unexpected network request")

		return nil, errors.New("unexpected")
	}))

	for _, token := range []string{"", strings.Repeat("x", (64<<10)+1)} {
		_, err := verifier.Verify(t.Context(), gcpcaptcha.Input{Token: token})

		if !errors.Is(err, gcpcaptcha.ErrRejected) {
			t.Fatalf("got %v", err)
		}
	}
}

// TestVerifyTransportCauseIsRedacted preserves identity without native text.
func TestVerifyTransportCauseIsRedacted(t *testing.T) {
	t.Parallel()

	cause := errors.New("secret-token api-secret")
	var calls int
	verifier := newVerifier(t, roundTrip(func(*http.Request) (*http.Response, error) {
		calls++

		return nil, cause
	}))

	_, err := verifier.Verify(t.Context(), gcpcaptcha.Input{Token: "token"})

	if !errors.Is(err, cause) || !errors.Is(err, gcpcaptcha.ErrUnavailable) {
		t.Fatalf("lost cause: %v", err)
	}

	if calls != 1 || strings.Contains(err.Error(), "secret") {
		t.Fatalf("unsafe failure: calls=%d %v", calls, err)
	}
}

// TestVerifyCancellationPreservesIdentity reports an unavailable unknown outcome.
func TestVerifyCancellationPreservesIdentity(t *testing.T) {
	t.Parallel()

	verifier := newVerifier(t, roundTrip(func(request *http.Request) (*http.Response, error) {
		return nil, request.Context().Err()
	}))
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	_, err := verifier.Verify(ctx, gcpcaptcha.Input{Token: "token"})

	if !errors.Is(err, context.Canceled) || !errors.Is(err, gcpcaptcha.ErrUnavailable) {
		t.Fatalf("got %v", err)
	}
}

// TestVerifyBoundedTimeout uses virtual time, not a wall-clock sleep.
func TestVerifyBoundedTimeout(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		verifier := newVerifier(t, roundTrip(func(request *http.Request) (*http.Response, error) {
			<-request.Context().Done()

			return nil, request.Context().Err()
		}))

		startedAt := time.Now()
		_, err := verifier.Verify(t.Context(), gcpcaptcha.Input{Token: "token"})

		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("got %v", err)
		}

		if time.Since(startedAt) != 10*time.Second {
			t.Fatal("incorrect timeout")
		}
	})
}

// TestVerifyPreauthenticatedClientRetainsAuth bypasses ADC discovery explicitly.
func TestVerifyPreauthenticatedClientRetainsAuth(t *testing.T) {
	t.Parallel()

	transport := &oauth2.Transport{Source: oauth2.StaticTokenSource(&oauth2.Token{AccessToken: "access-secret"}), Base: roundTrip(func(request *http.Request) (*http.Response, error) {
		if request.Header.Get("Authorization") != "Bearer access-secret" {
			t.Fatal("borrowed auth lost")
		}

		if request.Header.Get("X-Goog-Api-Key") != "" {
			t.Fatal("unexpected API key")
		}

		return response(http.StatusOK, validResponse), nil
	})}
	verifier := newVerifier(t, transport)

	if _, err := verifier.Verify(t.Context(), gcpcaptcha.Input{Token: "token"}); err != nil {
		t.Fatal(err)
	}
}

// TestNewCopiesAllowlistAndDraft verifies publication detaches mutable inputs.
func TestNewCopiesAllowlistAndDraft(t *testing.T) {
	t.Parallel()

	hostnames := []string{"shop.example"}
	var retained *gcpcaptcha.Builder
	var order []int
	client := &http.Client{Transport: roundTrip(func(request *http.Request) (*http.Response, error) {
		if request.Header.Get("X-Goog-Api-Key") != "final-key" {
			t.Fatal("draft mutation leaked")
		}

		return response(http.StatusOK, validResponse), nil
	})}

	verifier, err := gcpcaptcha.New(t.Context(), "shop-prod", "site-key", hostnames,
		func(access *gcpcaptcha.Builder) {
			order = append(order, 1)
			retained = access
			access.APIKey("initial-key")
			access.HTTPClient(client)
		},
		func(access *gcpcaptcha.Builder) { order = append(order, 2); access.APIKey("final-key") })

	if err != nil {
		t.Fatal(err)
	}

	hostnames[0] = "evil.example"
	retained.APIKey("mutated-key").HTTPClient(nil)
	client.Transport = roundTrip(func(*http.Request) (*http.Response, error) {
		t.Fatal("client mutation leaked")

		return nil, errors.New("unexpected")
	})

	if _, err := verifier.Verify(t.Context(), gcpcaptcha.Input{Token: "token"}); err != nil {
		t.Fatal(err)
	}

	if fmt.Sprint(order) != "[1 2]" {
		t.Fatalf("callbacks: %v", order)
	}
}

// TestNewInvalidConfigStillRunsCallbacks preserves direct once-only construction.
func TestNewInvalidConfigStillRunsCallbacks(t *testing.T) {
	t.Parallel()

	var calls int
	_, err := gcpcaptcha.New(t.Context(), "../secret-project", "", nil, func(access *gcpcaptcha.Builder) { calls++; access.APIKey("key") })

	if calls != 1 || !errors.Is(err, gcpcaptcha.ErrConfiguration) {
		t.Fatalf("calls=%d err=%v", calls, err)
	}

	if strings.Contains(err.Error(), "secret-project") {
		t.Fatal("configuration echoed")
	}
}

// TestNewRequiresTrustedHostnames rejects empty lists and ambiguous DNS forms.
func TestNewRequiresTrustedHostnames(t *testing.T) {
	t.Parallel()

	for _, hostnames := range [][]string{nil, {""}, {"*.example"}, {"https://shop.example"}, {"shop.example:443"}, {"shop.example."}} {
		_, err := gcpcaptcha.New(t.Context(), "shop-prod", "key", hostnames, func(access *gcpcaptcha.Builder) { access.APIKey("key") })

		if !errors.Is(err, gcpcaptcha.ErrConfiguration) {
			t.Fatalf("accepted %v: %v", hostnames, err)
		}
	}
}

// TestVerifyConcurrentReuse exercises immutable shared verifier state under race.
func TestVerifyConcurrentReuse(t *testing.T) {
	t.Parallel()

	var calls atomic.Int32
	verifier := newVerifier(t, roundTrip(func(*http.Request) (*http.Response, error) {
		calls.Add(1)

		return response(http.StatusOK, validResponse), nil
	}))

	var group sync.WaitGroup

	for range 20 {
		group.Go(func() {
			if _, err := verifier.Verify(t.Context(), gcpcaptcha.Input{Token: "token"}); err != nil {
				t.Error(err)
			}
		})
	}

	group.Wait()

	if calls.Load() != 20 {
		t.Fatalf("calls=%d", calls.Load())
	}
}

// TestVerifyRedirectIsNotFollowed prevents credential forwarding or token replay.
func TestVerifyRedirectIsNotFollowed(t *testing.T) {
	t.Parallel()

	var calls int
	verifier := newVerifier(t, roundTrip(func(*http.Request) (*http.Response, error) {
		calls++
		redirect := response(http.StatusTemporaryRedirect, "")
		redirect.Header.Set("Location", "https://evil.example/collect")

		return redirect, nil
	}))

	_, err := verifier.Verify(t.Context(), gcpcaptcha.Input{Token: "token"})

	if calls != 1 || !errors.Is(err, gcpcaptcha.ErrUnavailable) {
		t.Fatalf("calls=%d err=%v", calls, err)
	}
}

// TestVerifyInvalidCredentialsAreConfiguration distinguishes server auth failures.
func TestVerifyInvalidCredentialsAreConfiguration(t *testing.T) {
	t.Parallel()

	verifier := newVerifier(t, roundTrip(func(*http.Request) (*http.Response, error) {
		return response(http.StatusUnauthorized, `secret-api-key`), nil
	}))

	_, err := verifier.Verify(t.Context(), gcpcaptcha.Input{Token: "token"})

	if !errors.Is(err, gcpcaptcha.ErrConfiguration) || strings.Contains(err.Error(), "secret") {
		t.Fatalf("got %v", err)
	}
}

// TestVerifyQuotaIsUnavailable must not classify quota as token rejection.
func TestVerifyQuotaIsUnavailable(t *testing.T) {
	t.Parallel()

	verifier := newVerifier(t, roundTrip(func(*http.Request) (*http.Response, error) {
		return response(http.StatusTooManyRequests, `secret-quota-body`), nil
	}))

	_, err := verifier.Verify(t.Context(), gcpcaptcha.Input{Token: "token"})

	if !errors.Is(err, gcpcaptcha.ErrUnavailable) || strings.Contains(err.Error(), "secret") {
		t.Fatalf("got %v", err)
	}
}

// TestVerifyMissingRiskAnalysis refuses an authentic but unscored assessment.
func TestVerifyMissingRiskAnalysis(t *testing.T) {
	t.Parallel()

	_, err := verifyResponse(t, `{"tokenProperties":{"valid":true,"hostname":"shop.example","action":"login"}}`)

	if !errors.Is(err, gcpcaptcha.ErrUnavailable) {
		t.Fatalf("got %v", err)
	}
}

// TestVerifyMissingValidity honors proto3 default false without adding an outage.
func TestVerifyMissingValidity(t *testing.T) {
	t.Parallel()

	_, err := verifyResponse(t, strings.ReplaceAll(validResponse, `"valid":true,`, ""))

	if !errors.Is(err, gcpcaptcha.ErrRejected) || errors.Is(err, gcpcaptcha.ErrUnavailable) {
		t.Fatalf("got %v", err)
	}
}

// TestVerifyNonfiniteScores rejects malformed or overflowing JSON numbers.
func TestVerifyNonfiniteScores(t *testing.T) {
	t.Parallel()

	for _, score := range []string{"NaN", "Infinity", "1e999", "-0.1"} {
		_, err := verifyResponse(t, strings.ReplaceAll(validResponse, `"score":0.1`, `"score":`+score))

		if !errors.Is(err, gcpcaptcha.ErrUnavailable) {
			t.Fatalf("accepted score %s: %v", score, err)
		}
	}
}

// TestVerifyEarlierDeadlineWins checks virtual-time cancellation before ten seconds.
func TestVerifyEarlierDeadlineWins(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		verifier := newVerifier(t, roundTrip(func(request *http.Request) (*http.Response, error) {
			<-request.Context().Done()

			return nil, request.Context().Err()
		}))
		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		t.Cleanup(cancel)

		startedAt := time.Now()
		_, err := verifier.Verify(ctx, gcpcaptcha.Input{Token: "token"})

		if !errors.Is(err, context.DeadlineExceeded) || time.Since(startedAt) != time.Second {
			t.Fatalf("deadline not preserved: %v", err)
		}
	})
}

// TestNewAPIKeyDoesNotDiscoverCredentials accepts a canceled construction context
// because API-key construction performs neither credential nor verification I/O.
func TestNewAPIKeyDoesNotDiscoverCredentials(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	_, err := gcpcaptcha.New(ctx, "shop-prod", "site-key", []string{"shop.example"}, func(access *gcpcaptcha.Builder) {
		access.APIKey("api-key")
	})

	if err != nil {
		t.Fatal(err)
	}
}

// closingTransport detects accidental ownership transfer of borrowed resources.
type closingTransport struct {
	// closed records whether the package tried to close a caller-owned pool.
	closed bool
}

// RoundTrip provides deterministic valid evidence without a network connection.
func (transport *closingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return response(http.StatusOK, validResponse), nil
}

// CloseIdleConnections records a caller-owned pool cleanup attempt.
func (transport *closingTransport) CloseIdleConnections() {
	transport.closed = true
}

// TestVerifyDoesNotCloseBorrowedTransport preserves caller resource ownership.
func TestVerifyDoesNotCloseBorrowedTransport(t *testing.T) {
	t.Parallel()

	transport := &closingTransport{}
	verifier := newVerifier(t, transport)

	if _, err := verifier.Verify(t.Context(), gcpcaptcha.Input{Token: "token"}); err != nil {
		t.Fatal(err)
	}

	if transport.closed {
		t.Fatal("borrowed transport closed")
	}
}
