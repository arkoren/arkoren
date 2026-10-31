package cloudflarecaptcha_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"arkoren.dev/driver/cloudflare/cloudflarecaptcha"
	"arkoren.dev/foundation/problem"
)

// TestMiddlewareLeavesJSONBodyUntouched checks explicit header extraction, one
// provider call, no proxy-header forwarding and next only after verification.
func TestMiddlewareLeavesJSONBodyUntouched(t *testing.T) {
	t.Parallel()

	calls := 0

	verifier, err := cloudflarecaptcha.New("secret", []string{"example.com"}, func(connection *cloudflarecaptcha.Builder) {
		connection.HTTPClient(&http.Client{Transport: transport(func(request *http.Request) (*http.Response, error) {
			calls++

			if err := request.ParseForm(); err != nil {
				t.Fatal(err)
			}

			if request.Form.Has("remoteip") {
				t.Fatal("middleware trusted proxy headers")
			}

			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(validResponse))}, nil
		})})
	})

	if err != nil {
		t.Fatal(err)
	}

	extractions := 0
	var retained *cloudflarecaptcha.MiddlewareBuilder
	order := ""

	protect := cloudflarecaptcha.Middleware(verifier, func(request *http.Request) (string, error) {
		extractions++

		return request.Header.Get("X-Turnstile-Token"), nil
	}, func(policy *cloudflarecaptcha.MiddlewareBuilder) {
		retained = policy
		order += "first"
		policy.Action("login")
	}, func(policy *cloudflarecaptcha.MiddlewareBuilder) {
		order += "second"
		policy.Action("signup")
	})

	retained.Action("different")
	nextCalls := 0

	handler := protect(func(_ http.ResponseWriter, request *http.Request) error {
		nextCalls++

		body, err := io.ReadAll(request.Body)

		if err != nil {
			t.Fatal(err)
		}

		if string(body) != `{"message":"untouched"}` {
			t.Fatalf("body changed: %s", body)
		}

		return nil
	})

	request := httptest.NewRequest(http.MethodPost, "/signup", strings.NewReader(`{"message":"untouched"}`))
	request.Header.Set("X-Turnstile-Token", "token")
	request.Header.Set("X-Forwarded-For", "192.0.2.1")
	request.Header.Set("CF-Connecting-IP", "192.0.2.1")

	err = handler(httptest.NewRecorder(), request)

	if err != nil || calls != 1 || extractions != 1 {
		t.Fatalf("error=%v calls=%d extractions=%d", err, calls, extractions)
	}

	if nextCalls != 1 || order != "firstsecond" {
		t.Fatalf("next=%d order=%s", nextCalls, order)
	}
}

// checkHTTPFailure checks HTTP rendering and classification without exposing
// sensitive causes. It also confirms the error retains problem.Details identity.
func checkHTTPFailure(t *testing.T, err error, status int, class error) {
	t.Helper()

	if !errors.Is(err, class) {
		t.Fatalf("classification lost: %v", err)
	}

	var details problem.Details

	if !errors.As(err, &details) {
		t.Fatalf("not a problem: %v", err)
	}

	recorder := httptest.NewRecorder()
	details.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/signup", nil))

	if recorder.Code != status {
		t.Fatalf("status=%d expected=%d", recorder.Code, status)
	}

	if strings.Contains(recorder.Body.String(), "sensitive") || strings.Contains(err.Error(), "sensitive") {
		t.Fatalf("diagnostic leaked: %s %v", recorder.Body, err)
	}
}

// TestMiddlewareRejected checks a real provider rejection stops next with 403.
func TestMiddlewareRejected(t *testing.T) {
	t.Parallel()

	verifier := fixture(t, `{"success":false,"error-codes":["invalid-input-response"]}`)
	protect := cloudflarecaptcha.Middleware(verifier, func(*http.Request) (string, error) {
		return "sensitive-token", nil
	})

	err := protect(func(http.ResponseWriter, *http.Request) error {
		t.Fatal("next called after rejection")

		return nil
	})(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/", nil))

	checkHTTPFailure(t, err, http.StatusForbidden, cloudflarecaptcha.ErrRejected)
}

// TestMiddlewareUnavailable checks malformed upstream bodies stop next with 503.
func TestMiddlewareUnavailable(t *testing.T) {
	t.Parallel()

	protect := cloudflarecaptcha.Middleware(fixture(t, "sensitive malformed response"), func(*http.Request) (string, error) {
		return "token", nil
	})

	err := protect(func(http.ResponseWriter, *http.Request) error {
		t.Fatal("next called after unavailable")

		return nil
	})(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/", nil))

	checkHTTPFailure(t, err, http.StatusServiceUnavailable, cloudflarecaptcha.ErrUnavailable)
}

// TestMiddlewareConfiguration checks provider credential faults stop next with 500.
func TestMiddlewareConfiguration(t *testing.T) {
	t.Parallel()

	protect := cloudflarecaptcha.Middleware(fixture(t, `{"success":false,"error-codes":["invalid-input-secret"]}`), func(*http.Request) (string, error) {
		return "token", nil
	})

	err := protect(func(http.ResponseWriter, *http.Request) error {
		t.Fatal("next called after server configuration error")

		return nil
	})(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/", nil))

	checkHTTPFailure(t, err, http.StatusInternalServerError, cloudflarecaptcha.ErrConfiguration)
}

// TestMiddlewareExtractionError preserves original cause identity with safe 403.
func TestMiddlewareExtractionError(t *testing.T) {
	t.Parallel()

	cause := errors.New("sensitive extraction diagnostic")
	protect := cloudflarecaptcha.Middleware(&cloudflarecaptcha.Verifier{}, func(*http.Request) (string, error) {
		return "", cause
	})

	err := protect(func(http.ResponseWriter, *http.Request) error {
		t.Fatal("next called after extraction error")

		return nil
	})(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/", nil))

	checkHTTPFailure(t, err, http.StatusForbidden, cloudflarecaptcha.ErrRejected)

	if !errors.Is(err, cause) {
		t.Fatal("lost extraction cause")
	}
}

// TestMiddlewareExtractionUnavailable preserves explicitly classified failures.
func TestMiddlewareExtractionUnavailable(t *testing.T) {
	t.Parallel()

	cause := errors.Join(cloudflarecaptcha.ErrUnavailable, errors.New("sensitive cause"))
	protect := cloudflarecaptcha.Middleware(&cloudflarecaptcha.Verifier{}, func(*http.Request) (string, error) {
		return "", cause
	})

	err := protect(func(http.ResponseWriter, *http.Request) error {
		t.Fatal("next called after extraction unavailable")

		return nil
	})(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/", nil))

	checkHTTPFailure(t, err, http.StatusServiceUnavailable, cloudflarecaptcha.ErrUnavailable)

	if !errors.Is(err, cause) {
		t.Fatal("lost extraction cause")
	}
}

// TestMiddlewareExtractionCanceled treats interrupted extraction as unavailable,
// not evidence of an invalid token, and never consumes a provider token.
func TestMiddlewareExtractionCanceled(t *testing.T) {
	t.Parallel()

	verifier, err := cloudflarecaptcha.New("secret", []string{"example.com"}, func(connection *cloudflarecaptcha.Builder) {
		connection.HTTPClient(&http.Client{Transport: transport(func(*http.Request) (*http.Response, error) {
			t.Fatal("provider called after extraction cancellation")

			return nil, errors.New("unexpected provider call")
		})})
	})

	if err != nil {
		t.Fatal(err)
	}

	cause := errors.Join(context.Canceled, errors.New("sensitive extraction diagnostic"))
	protect := cloudflarecaptcha.Middleware(verifier, func(*http.Request) (string, error) {
		return "sensitive-token", cause
	})

	err = protect(func(http.ResponseWriter, *http.Request) error {
		t.Fatal("next called after extraction cancellation")

		return nil
	})(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/", nil))

	checkHTTPFailure(t, err, http.StatusServiceUnavailable, cloudflarecaptcha.ErrUnavailable)

	if !errors.Is(err, cause) || !errors.Is(err, context.Canceled) {
		t.Fatalf("lost extraction cancellation cause: %v", err)
	}

	if errors.Is(err, cloudflarecaptcha.ErrRejected) {
		t.Fatalf("cancellation classified as rejection: %v", err)
	}
}

// TestMiddlewareExtractionDeadline treats expired extraction as unavailable,
// preserving the original cause without calling the provider or next handler.
func TestMiddlewareExtractionDeadline(t *testing.T) {
	t.Parallel()

	verifier, err := cloudflarecaptcha.New("secret", []string{"example.com"}, func(connection *cloudflarecaptcha.Builder) {
		connection.HTTPClient(&http.Client{Transport: transport(func(*http.Request) (*http.Response, error) {
			t.Fatal("provider called after extraction deadline")

			return nil, errors.New("unexpected provider call")
		})})
	})

	if err != nil {
		t.Fatal(err)
	}

	cause := errors.Join(context.DeadlineExceeded, errors.New("sensitive extraction diagnostic"))
	protect := cloudflarecaptcha.Middleware(verifier, func(*http.Request) (string, error) {
		return "sensitive-token", cause
	})

	err = protect(func(http.ResponseWriter, *http.Request) error {
		t.Fatal("next called after extraction deadline")

		return nil
	})(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/", nil))

	checkHTTPFailure(t, err, http.StatusServiceUnavailable, cloudflarecaptcha.ErrUnavailable)

	if !errors.Is(err, cause) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("lost extraction deadline cause: %v", err)
	}

	if errors.Is(err, cloudflarecaptcha.ErrRejected) {
		t.Fatalf("deadline classified as rejection: %v", err)
	}
}

// TestMiddlewareExtractionConfiguration preserves server fault classification.
func TestMiddlewareExtractionConfiguration(t *testing.T) {
	t.Parallel()

	cause := errors.Join(cloudflarecaptcha.ErrConfiguration, context.Canceled, errors.New("sensitive cause"))
	protect := cloudflarecaptcha.Middleware(&cloudflarecaptcha.Verifier{}, func(*http.Request) (string, error) {
		return "", cause
	})

	err := protect(func(http.ResponseWriter, *http.Request) error {
		t.Fatal("next called after extraction configuration failure")

		return nil
	})(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/", nil))

	checkHTTPFailure(t, err, http.StatusInternalServerError, cloudflarecaptcha.ErrConfiguration)
}

// TestMiddlewareNextErrorUnchanged avoids rewriting downstream application errors.
func TestMiddlewareNextErrorUnchanged(t *testing.T) {
	t.Parallel()

	cause := errors.New("application error")
	protect := cloudflarecaptcha.Middleware(fixture(t, validResponse), func(*http.Request) (string, error) {
		return "token", nil
	})

	err := protect(func(http.ResponseWriter, *http.Request) error {
		return cause
	})(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/", nil))

	if !errors.Is(err, cause) {
		t.Fatalf("downstream error changed: %v", err)
	}
}

// TestMiddlewareNilVerifierPanics protects the required verifier boundary.
func TestMiddlewareNilVerifierPanics(t *testing.T) {
	t.Parallel()

	defer func() {
		if recover() == nil {
			t.Fatal("expected nil verifier panic")
		}
	}()

	cloudflarecaptcha.Middleware(nil, func(*http.Request) (string, error) {
		return "", nil
	})
}

// TestMiddlewareNilExtractorPanics prohibits implicit request parsing defaults.
func TestMiddlewareNilExtractorPanics(t *testing.T) {
	t.Parallel()

	defer func() {
		if recover() == nil {
			t.Fatal("expected nil extractor panic")
		}
	}()

	cloudflarecaptcha.Middleware(&cloudflarecaptcha.Verifier{}, nil)
}

// TestMiddlewareNilCallbackPanics ensures construction misuse is not ignored.
func TestMiddlewareNilCallbackPanics(t *testing.T) {
	t.Parallel()

	defer func() {
		if recover() == nil {
			t.Fatal("expected nil callback panic")
		}
	}()

	cloudflarecaptcha.Middleware(&cloudflarecaptcha.Verifier{}, func(*http.Request) (string, error) {
		return "", nil
	}, nil)
}

// TestMiddlewareNilNextPanics rejects an invalid router chain at wrapping time.
func TestMiddlewareNilNextPanics(t *testing.T) {
	t.Parallel()

	defer func() {
		if recover() == nil {
			t.Fatal("expected nil next panic")
		}
	}()

	cloudflarecaptcha.Middleware(&cloudflarecaptcha.Verifier{}, func(*http.Request) (string, error) {
		return "", nil
	})(nil)
}
