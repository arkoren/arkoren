package gcpcaptcha_test

import (
	"context"
	"errors"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"arkoren.dev/core/http/router"
	"arkoren.dev/driver/gcp/gcpcaptcha"
	"arkoren.dev/foundation/problem"
)

// extractionFailure exercises a real route with a failing application extractor;
// transport and next are traps so operational failures cannot advance the flow.
func extractionFailure(t *testing.T, cause error) error {
	t.Helper()

	verifier := newVerifier(t, roundTrip(func(*http.Request) (*http.Response, error) {
		t.Error("verification ran after extraction failed")

		return nil, errors.New("unexpected verification")
	}))
	var calls int
	handler := gcpcaptcha.Middleware(verifier, func(*http.Request) (string, error) {
		calls++

		return "", cause
	}, func(policy *gcpcaptcha.MiddlewareBuilder) {
		policy.MinScore(0.7)
	})(deniedHandler(t))

	err := handler(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/login", nil))

	if calls != 1 {
		t.Fatalf("extraction calls=%d", calls)
	}

	return err
}

// TestMiddlewareExtractionCancellationIsUnavailable retains cancellation without
// adding visitor-rejection identity to a failed token extraction.
func TestMiddlewareExtractionCancellationIsUnavailable(t *testing.T) {
	t.Parallel()

	cause := errors.Join(context.Canceled, errors.New("secret extraction diagnostic"))

	err := extractionFailure(t, cause)

	requireProblem(t, err, http.StatusServiceUnavailable, gcpcaptcha.ErrUnavailable)

	if !errors.Is(err, context.Canceled) || errors.Is(err, gcpcaptcha.ErrRejected) {
		t.Fatalf("wrong extraction cancellation identities: %v", err)
	}
}

// TestMiddlewareExtractionDeadlineIsUnavailable preserves an extractor deadline
// independently of the request's own context and keeps verification skipped.
func TestMiddlewareExtractionDeadlineIsUnavailable(t *testing.T) {
	t.Parallel()

	err := extractionFailure(t, errors.Join(context.DeadlineExceeded, errors.New("secret deadline")))

	requireProblem(t, err, http.StatusServiceUnavailable, gcpcaptcha.ErrUnavailable)

	if !errors.Is(err, context.DeadlineExceeded) || errors.Is(err, gcpcaptcha.ErrRejected) {
		t.Fatalf("wrong extraction deadline identities: %v", err)
	}
}

// TestMiddlewareExtractionUnavailableIsNotRejected honors explicit application
// operational classification without adding a contradictory rejection category.
func TestMiddlewareExtractionUnavailableIsNotRejected(t *testing.T) {
	t.Parallel()

	cause := errors.Join(gcpcaptcha.ErrUnavailable, errors.New("secret unavailable"))

	err := extractionFailure(t, cause)

	requireProblem(t, err, http.StatusServiceUnavailable, cause)

	if errors.Is(err, gcpcaptcha.ErrRejected) {
		t.Fatal("unavailable extraction acquired rejection identity")
	}
}

// TestMiddlewareExtractionConfigurationIsNotRejected honors server faults from
// application extraction without turning them into visitor token rejection.
func TestMiddlewareExtractionConfigurationIsNotRejected(t *testing.T) {
	t.Parallel()

	cause := errors.Join(gcpcaptcha.ErrConfiguration, errors.New("secret configuration"))

	err := extractionFailure(t, cause)

	requireProblem(t, err, http.StatusInternalServerError, cause)

	if errors.Is(err, gcpcaptcha.ErrRejected) || errors.Is(err, gcpcaptcha.ErrUnavailable) {
		t.Fatal("configuration extraction acquired an unrelated identity")
	}
}

// TestMiddlewareUnavailablePrecedesRejection preserves multi-category causes but
// selects the operational status rather than the less important visitor denial.
func TestMiddlewareUnavailablePrecedesRejection(t *testing.T) {
	t.Parallel()

	cause := errors.Join(gcpcaptcha.ErrRejected, gcpcaptcha.ErrUnavailable)

	err := extractionFailure(t, cause)

	requireProblem(t, err, http.StatusServiceUnavailable, cause)
}

// TestMiddlewareConfigurationPrecedesAllFailures keeps server configuration as
// the selected HTTP category even when the original cause has several identities.
func TestMiddlewareConfigurationPrecedesAllFailures(t *testing.T) {
	t.Parallel()

	cause := errors.Join(gcpcaptcha.ErrRejected, gcpcaptcha.ErrUnavailable, gcpcaptcha.ErrConfiguration, context.Canceled)

	err := extractionFailure(t, cause)

	requireProblem(t, err, http.StatusInternalServerError, cause)
}

// scorePolicy runs the same application binding policy for different legitimate
// ProtoJSON score encodings and returns whether the next handler was admitted.
func scorePolicy(t *testing.T, body string, threshold float64) (int, error) {
	t.Helper()

	verifier := newVerifier(t, roundTrip(func(*http.Request) (*http.Response, error) {
		return response(http.StatusOK, body), nil
	}))
	var calls int
	handler := gcpcaptcha.Middleware(verifier, headerToken, func(policy *gcpcaptcha.MiddlewareBuilder) {
		policy.Action("login")
		policy.MinScore(threshold)
	})(func(http.ResponseWriter, *http.Request) error {
		calls++

		return nil
	})
	request := httptest.NewRequest(http.MethodPost, "/login", nil)
	request.Header.Set("X-Captcha-Token", "token")

	err := handler(httptest.NewRecorder(), request)

	return calls, err
}

// TestMiddlewareOmittedScoreAcceptsExplicitZero applies a deliberate zero policy
// to the provider's ordinary omitted-default score representation.
func TestMiddlewareOmittedScoreAcceptsExplicitZero(t *testing.T) {
	t.Parallel()

	calls, err := scorePolicy(t, `{"tokenProperties":{"valid":true,"hostname":"shop.example","action":"login"},"riskAnalysis":{}}`, 0)

	if err != nil || calls != 1 {
		t.Fatalf("default zero rejected: calls=%d err=%v", calls, err)
	}
}

// TestMiddlewareNullScoreAcceptsExplicitZero treats scalar null as proto3 zero,
// while still applying the same required origin and action checks.
func TestMiddlewareNullScoreAcceptsExplicitZero(t *testing.T) {
	t.Parallel()

	calls, err := scorePolicy(t, `{"tokenProperties":{"valid":true,"hostname":"shop.example","action":"login"},"riskAnalysis":{"score":null}}`, 0)

	if err != nil || calls != 1 {
		t.Fatalf("null default zero rejected: calls=%d err=%v", calls, err)
	}
}

// TestMiddlewareExplicitScoreAcceptsExplicitZero uses the exact same action,
// origin and threshold policy as the omitted-default score fixture above.
func TestMiddlewareExplicitScoreAcceptsExplicitZero(t *testing.T) {
	t.Parallel()

	calls, err := scorePolicy(t, `{"tokenProperties":{"valid":true,"hostname":"shop.example","action":"login"},"riskAnalysis":{"score":0}}`, 0)

	if err != nil || calls != 1 {
		t.Fatalf("explicit zero rejected: calls=%d err=%v", calls, err)
	}
}

// TestMiddlewareOmittedScoreFailsPositiveThreshold rejects the legitimate global
// score zero through application policy, not through provider unavailability.
func TestMiddlewareOmittedScoreFailsPositiveThreshold(t *testing.T) {
	t.Parallel()

	calls, err := scorePolicy(t, `{"tokenProperties":{"valid":true,"hostname":"shop.example","action":"login"},"riskAnalysis":{}}`, 0.7)

	requireProblem(t, err, http.StatusForbidden, gcpcaptcha.ErrRejected)

	if calls != 0 || errors.Is(err, gcpcaptcha.ErrUnavailable) {
		t.Fatal("default zero was admitted or misclassified as an outage")
	}
}

// TestMiddlewareExplicitScoreFailsPositiveThreshold applies the identical policy
// outcome to explicit score zero, not just the omitted-default representation.
func TestMiddlewareExplicitScoreFailsPositiveThreshold(t *testing.T) {
	t.Parallel()

	calls, err := scorePolicy(t, `{"tokenProperties":{"valid":true,"hostname":"shop.example","action":"login"},"riskAnalysis":{"score":0}}`, 0.7)

	requireProblem(t, err, http.StatusForbidden, gcpcaptcha.ErrRejected)

	if calls != 0 || errors.Is(err, gcpcaptcha.ErrUnavailable) {
		t.Fatal("explicit zero was admitted or misclassified as an outage")
	}
}

// TestMiddlewareZeroThresholdStillRequiresRiskMessage does not confuse a missing
// enclosing message with an omitted nonoptional score inside a present message.
func TestMiddlewareZeroThresholdStillRequiresRiskMessage(t *testing.T) {
	t.Parallel()

	calls, err := scorePolicy(t, `{"tokenProperties":{"valid":true,"hostname":"shop.example","action":"login"}}`, 0)

	requireProblem(t, err, http.StatusServiceUnavailable, gcpcaptcha.ErrUnavailable)

	if calls != 0 {
		t.Fatal("missing risk message admitted")
	}
}

// TestMiddlewareZeroThresholdStillRequiresOrigin keeps a zero score policy from
// becoming a bypass for the required trusted website binding.
func TestMiddlewareZeroThresholdStillRequiresOrigin(t *testing.T) {
	t.Parallel()

	calls, err := scorePolicy(t, `{"tokenProperties":{"valid":true,"hostname":"evil.example","action":"login"},"riskAnalysis":{}}`, 0)

	requireProblem(t, err, http.StatusForbidden, gcpcaptcha.ErrRejected)

	if calls != 0 {
		t.Fatal("wrong origin admitted")
	}
}

// headerToken is an application extraction policy that leaves bodies untouched.
func headerToken(request *http.Request) (string, error) {
	return request.Header.Get("X-Captcha-Token"), nil
}

// deniedHandler makes unintended handler admission observable in failure tests.
func deniedHandler(t *testing.T) router.Handler {
	t.Helper()

	return func(http.ResponseWriter, *http.Request) error {
		t.Error("next handler must not run")

		return nil
	}
}

// requireProblem verifies safe HTTP policy and preserved domain identity.
func requireProblem(t *testing.T, err error, status int, cause error) {
	t.Helper()

	var details problem.Details

	if !errors.As(err, &details) {
		t.Fatalf("not problem details: %v", err)
	}

	if details.Status() != status || !errors.Is(err, cause) {
		t.Fatalf("status=%d err=%v", details.Status(), err)
	}

	if details.Detail() != "" || strings.Contains(err.Error(), "secret") {
		t.Fatalf("unsafe diagnostics: %v", err)
	}
}

// TestMiddlewareAcceptsPolicyAndLeavesBody verifies once-only extraction and next.
func TestMiddlewareAcceptsPolicyAndLeavesBody(t *testing.T) {
	t.Parallel()

	var providerCalls, extractionCalls, nextCalls int
	verifier := newVerifier(t, roundTrip(func(request *http.Request) (*http.Response, error) {
		providerCalls++

		return response(http.StatusOK, validResponse), nil
	}))
	protect := gcpcaptcha.Middleware(verifier, func(request *http.Request) (string, error) {
		extractionCalls++

		return headerToken(request)
	}, func(policy *gcpcaptcha.MiddlewareBuilder) { policy.Action("login"); policy.MinScore(0.1) })
	handler := protect(func(_ http.ResponseWriter, request *http.Request) error {
		nextCalls++
		body, err := io.ReadAll(request.Body)

		if err != nil {
			return err
		}

		if string(body) != `{"business":"payload"}` {
			t.Fatalf("body altered: %s", body)
		}

		return nil
	})
	request := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(`{"business":"payload"}`))
	request.Header.Set("X-Captcha-Token", "token")
	request.Header.Set("X-Forwarded-For", "192.0.2.4")

	if err := handler(httptest.NewRecorder(), request); err != nil {
		t.Fatal(err)
	}

	if providerCalls != 1 || extractionCalls != 1 || nextCalls != 1 {
		t.Fatalf("calls: %d %d %d", providerCalls, extractionCalls, nextCalls)
	}
}

// TestMiddlewareActionWithoutImplicitSignals sends policy but no inferred PII.
func TestMiddlewareActionWithoutImplicitSignals(t *testing.T) {
	t.Parallel()

	verifier := newVerifier(t, roundTrip(func(request *http.Request) (*http.Response, error) {
		body, err := io.ReadAll(request.Body)

		if err != nil {
			t.Fatal(err)
		}

		if !strings.Contains(string(body), `"expectedAction":"login"`) {
			t.Fatal("action missing")
		}

		if strings.Contains(string(body), "userIpAddress") || strings.Contains(string(body), "userAgent") {
			t.Fatal("implicit signals sent")
		}

		return response(http.StatusOK, validResponse), nil
	}))
	handler := gcpcaptcha.Middleware(verifier, headerToken, func(policy *gcpcaptcha.MiddlewareBuilder) {
		policy.Action("login")
		policy.MinScore(0.7)
	})(deniedHandler(t))
	request := httptest.NewRequest(http.MethodPost, "/login", nil)
	request.Header.Set("X-Captcha-Token", "token")
	request.Header.Set("X-Forwarded-For", "192.0.2.4")

	requireProblem(t, handler(httptest.NewRecorder(), request), http.StatusForbidden, gcpcaptcha.ErrRejected)
}

// TestMiddlewareMissingThresholdIsConfiguration prevents silent zero policy.
func TestMiddlewareMissingThresholdIsConfiguration(t *testing.T) {
	t.Parallel()

	verifier := newVerifier(t, roundTrip(func(*http.Request) (*http.Response, error) { t.Fatal("unexpected provider call"); return nil, nil }))
	handler := gcpcaptcha.Middleware(verifier, func(*http.Request) (string, error) { t.Fatal("unexpected extraction"); return "", nil })(deniedHandler(t))

	err := handler(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/login", nil))

	requireProblem(t, err, http.StatusInternalServerError, gcpcaptcha.ErrConfiguration)
}

// TestMiddlewareExplicitZeroAdmitsValidZeroScore preserves presence semantics.
func TestMiddlewareExplicitZeroAdmitsValidZeroScore(t *testing.T) {
	t.Parallel()

	verifier := newVerifier(t, roundTrip(func(*http.Request) (*http.Response, error) {
		return response(http.StatusOK, strings.ReplaceAll(validResponse, `"score":0.1`, `"score":0`)), nil
	}))
	var calls int
	handler := gcpcaptcha.Middleware(verifier, headerToken, func(policy *gcpcaptcha.MiddlewareBuilder) { policy.MinScore(0) })(func(http.ResponseWriter, *http.Request) error { calls++; return nil })
	request := httptest.NewRequest(http.MethodPost, "/login", nil)
	request.Header.Set("X-Captcha-Token", "token")

	if err := handler(httptest.NewRecorder(), request); err != nil {
		t.Fatal(err)
	}

	if calls != 1 {
		t.Fatalf("next calls=%d", calls)
	}
}

// TestMiddlewareNaNThresholdFailsClosed rejects NaN rather than admitting all.
func TestMiddlewareNaNThresholdFailsClosed(t *testing.T) {
	t.Parallel()

	handler := gcpcaptcha.Middleware(&gcpcaptcha.Verifier{}, headerToken, func(policy *gcpcaptcha.MiddlewareBuilder) { policy.MinScore(math.NaN()) })(deniedHandler(t))

	err := handler(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/login", nil))

	requireProblem(t, err, http.StatusInternalServerError, gcpcaptcha.ErrConfiguration)
}

// TestMiddlewareOutOfRangeThresholdsFailClosed covers uniformly invalid policies.
func TestMiddlewareOutOfRangeThresholdsFailClosed(t *testing.T) {
	t.Parallel()

	for _, threshold := range []float64{-0.1, 1.1, math.Inf(1), math.Inf(-1)} {
		handler := gcpcaptcha.Middleware(&gcpcaptcha.Verifier{}, headerToken, func(policy *gcpcaptcha.MiddlewareBuilder) { policy.MinScore(threshold) })(deniedHandler(t))

		err := handler(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/login", nil))

		requireProblem(t, err, http.StatusInternalServerError, gcpcaptcha.ErrConfiguration)
	}
}

// TestMiddlewareLowScoreIsRejected applies policy after provider authenticity.
func TestMiddlewareLowScoreIsRejected(t *testing.T) {
	t.Parallel()

	verifier := newVerifier(t, roundTrip(func(*http.Request) (*http.Response, error) { return response(http.StatusOK, validResponse), nil }))
	handler := gcpcaptcha.Middleware(verifier, headerToken, func(policy *gcpcaptcha.MiddlewareBuilder) { policy.MinScore(0.7) })(deniedHandler(t))
	request := httptest.NewRequest(http.MethodPost, "/login", nil)
	request.Header.Set("X-Captcha-Token", "token")

	requireProblem(t, handler(httptest.NewRecorder(), request), http.StatusForbidden, gcpcaptcha.ErrRejected)
}

// TestMiddlewareExtractionErrorKeepsCause hides application parser diagnostics.
func TestMiddlewareExtractionErrorKeepsCause(t *testing.T) {
	t.Parallel()

	cause := errors.New("secret extraction details")
	handler := gcpcaptcha.Middleware(&gcpcaptcha.Verifier{}, func(*http.Request) (string, error) { return "", cause }, func(policy *gcpcaptcha.MiddlewareBuilder) { policy.MinScore(0.7) })(deniedHandler(t))

	err := handler(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/login", nil))

	requireProblem(t, err, http.StatusForbidden, gcpcaptcha.ErrRejected)

	if !errors.Is(err, cause) {
		t.Fatal("extraction cause lost")
	}
}

// TestMiddlewareMissingTokenDoesNotReadForm prevents an implicit second source.
func TestMiddlewareMissingTokenDoesNotReadForm(t *testing.T) {
	t.Parallel()

	verifier := newVerifier(t, roundTrip(func(*http.Request) (*http.Response, error) { t.Fatal("unexpected provider call"); return nil, nil }))
	handler := gcpcaptcha.Middleware(verifier, headerToken, func(policy *gcpcaptcha.MiddlewareBuilder) { policy.MinScore(0.7) })(deniedHandler(t))
	request := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader("g-recaptcha-response=token"))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	requireProblem(t, handler(httptest.NewRecorder(), request), http.StatusForbidden, gcpcaptcha.ErrRejected)

	body, err := io.ReadAll(request.Body)

	if err != nil {
		t.Fatal(err)
	}

	if string(body) != "g-recaptcha-response=token" {
		t.Fatal("form body consumed")
	}
}

// TestMiddlewareOperationalFailureReturns503 never admits unknown outcomes.
func TestMiddlewareOperationalFailureReturns503(t *testing.T) {
	t.Parallel()

	cause := errors.New("secret upstream diagnostic")
	verifier := newVerifier(t, roundTrip(func(*http.Request) (*http.Response, error) { return nil, cause }))
	handler := gcpcaptcha.Middleware(verifier, headerToken, func(policy *gcpcaptcha.MiddlewareBuilder) { policy.MinScore(0.7) })(deniedHandler(t))
	request := httptest.NewRequest(http.MethodPost, "/login", nil)
	request.Header.Set("X-Captcha-Token", "token")

	err := handler(httptest.NewRecorder(), request)

	requireProblem(t, err, http.StatusServiceUnavailable, gcpcaptcha.ErrUnavailable)

	if !errors.Is(err, cause) {
		t.Fatal("transport cause lost")
	}

	writer := httptest.NewRecorder()
	var details problem.Details

	if !errors.As(err, &details) {
		t.Fatal("missing details")
	}

	details.ServeHTTP(writer, request)

	if writer.Code != http.StatusServiceUnavailable || strings.Contains(writer.Body.String(), "secret") {
		t.Fatalf("unsafe HTTP failure: %s", writer.Body)
	}
}

// TestMiddlewareAuthenticationFailureReturns500 signals a server-side fault.
func TestMiddlewareAuthenticationFailureReturns500(t *testing.T) {
	t.Parallel()

	verifier := newVerifier(t, roundTrip(func(*http.Request) (*http.Response, error) {
		return response(http.StatusForbidden, `secret credentials`), nil
	}))
	handler := gcpcaptcha.Middleware(verifier, headerToken, func(policy *gcpcaptcha.MiddlewareBuilder) { policy.MinScore(0.7) })(deniedHandler(t))
	request := httptest.NewRequest(http.MethodPost, "/login", nil)
	request.Header.Set("X-Captcha-Token", "token")

	requireProblem(t, handler(httptest.NewRecorder(), request), http.StatusInternalServerError, gcpcaptcha.ErrConfiguration)
}

// TestMiddlewareCancellationPreservesIdentity retains cancellation through HTTP.
func TestMiddlewareCancellationPreservesIdentity(t *testing.T) {
	t.Parallel()

	verifier := newVerifier(t, roundTrip(func(request *http.Request) (*http.Response, error) { return nil, request.Context().Err() }))
	handler := gcpcaptcha.Middleware(verifier, headerToken, func(policy *gcpcaptcha.MiddlewareBuilder) { policy.MinScore(0.7) })(deniedHandler(t))
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	request := httptest.NewRequestWithContext(ctx, http.MethodPost, "/login", nil)
	request.Header.Set("X-Captcha-Token", "token")

	err := handler(httptest.NewRecorder(), request)

	requireProblem(t, err, http.StatusServiceUnavailable, gcpcaptcha.ErrUnavailable)

	if !errors.Is(err, context.Canceled) {
		t.Fatal("cancellation cause lost")
	}
}

// TestMiddlewareDraftPublication preserves callback order without retaining drafts.
func TestMiddlewareDraftPublication(t *testing.T) {
	t.Parallel()

	verifier := newVerifier(t, roundTrip(func(*http.Request) (*http.Response, error) { return response(http.StatusOK, validResponse), nil }))
	var retained *gcpcaptcha.MiddlewareBuilder
	var edits, nextCalls int
	handler := gcpcaptcha.Middleware(verifier, headerToken,
		func(policy *gcpcaptcha.MiddlewareBuilder) { retained = policy; edits++; policy.MinScore(0.7) },
		func(policy *gcpcaptcha.MiddlewareBuilder) {
			if edits != 1 {
				t.Fatal("callback order")
			}

			edits++
			policy.MinScore(0.1)
			policy.Action("login")
		},
	)(func(http.ResponseWriter, *http.Request) error { nextCalls++; return nil })

	retained.MinScore(1).Action("changed")
	request := httptest.NewRequest(http.MethodPost, "/login", nil)
	request.Header.Set("X-Captcha-Token", "token")

	if err := handler(httptest.NewRecorder(), request); err != nil {
		t.Fatal(err)
	}

	if edits != 2 || nextCalls != 1 {
		t.Fatalf("edits=%d next=%d", edits, nextCalls)
	}
}

// TestMiddlewareNilDependenciesFailClosed returns configuration before extraction.
func TestMiddlewareNilDependenciesFailClosed(t *testing.T) {
	t.Parallel()

	handler := gcpcaptcha.Middleware(nil, nil, func(policy *gcpcaptcha.MiddlewareBuilder) { policy.MinScore(0) })(deniedHandler(t))

	requireProblem(t, handler(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/login", nil)), http.StatusInternalServerError, gcpcaptcha.ErrConfiguration)
}
