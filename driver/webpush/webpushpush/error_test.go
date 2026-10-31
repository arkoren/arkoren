package webpushpush

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"arkoren.dev/core/service/push"
)

// TestProviderGone checks authoritative 404/410 classification and ensures
// provider bodies and optional capability headers never leak into diagnostics.
func TestProviderGone(t *testing.T) {
	t.Parallel()

	for _, status := range []int{404, 410} {
		_, err := finishResponse(&http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader("secret"))})

		if !errors.Is(err, push.ErrNotFound) || strings.Contains(err.Error(), "secret") {
			t.Fatal("gone response misclassified or leaked")
		}
	}
}

// TestProviderRateLimit preserves safe retry metadata without retrying.
func TestProviderRateLimit(t *testing.T) {
	t.Parallel()

	driver, subscription, _ := fixture(t)
	calls := 0
	driver.client = doFunc(func(*http.Request) (*http.Response, error) {
		calls++

		return &http.Response{StatusCode: 429, Header: http.Header{"Retry-After": {"120"}}, Body: http.NoBody}, nil
	})

	_, err := driver.Send(t.Context(), push.NewMessage(subscription, "Ready"), push.SendConfig{})
	var rejected *ProviderError

	if !errors.As(err, &rejected) || !errors.Is(err, push.ErrRateLimited) {
		t.Fatal("missing structured rate-limit error")
	}

	if rejected.StatusCode != 429 || rejected.RetryAfter != 2*time.Minute || calls != 1 {
		t.Fatal("retry metadata changed or request repeated")
	}
}

// TestProviderUnavailable keeps server failures distinct from expired
// subscriptions and does not infer success from a response body.
func TestProviderUnavailable(t *testing.T) {
	t.Parallel()

	for _, status := range []int{500, 502, 503, 599} {
		_, err := finishResponse(&http.Response{StatusCode: status, Body: http.NoBody})

		if !errors.Is(err, push.ErrUnavailable) {
			t.Fatal("server rejection misclassified")
		}
	}
}

// TestProviderAuthenticationHasNoPortableClass preserves the provider status
// without mislabeling valid recipients as unsupported or expired. Applications
// must resolve credential failures rather than switch recipient types.
func TestProviderAuthenticationHasNoPortableClass(t *testing.T) {
	t.Parallel()

	for _, status := range []int{401, 403} {
		_, err := finishResponse(&http.Response{StatusCode: status, Body: http.NoBody})
		var rejected *ProviderError

		if !errors.As(err, &rejected) || rejected.StatusCode != status {
			t.Fatal("authentication rejection lost provider status")
		}

		if errors.Unwrap(err) != nil {
			t.Fatal("authentication rejection acquired an unrelated portable class")
		}
	}
}

// TestProviderInvalidMessage maps size and malformed-request failures without
// treating the subscription as expired.
func TestProviderInvalidMessage(t *testing.T) {
	t.Parallel()

	for _, status := range []int{400, 413} {
		_, err := finishResponse(&http.Response{StatusCode: status, Body: http.NoBody})

		if !errors.Is(err, push.ErrInvalidMessage) {
			t.Fatal("invalid request misclassified")
		}
	}
}

// TestProviderUnexpectedSuccess refuses to invent acceptance semantics for
// status codes not used by the non-receipt RFC 8030 submission profile.
func TestProviderUnexpectedSuccess(t *testing.T) {
	t.Parallel()

	for _, status := range []int{200, 202, 204} {
		_, err := finishResponse(&http.Response{StatusCode: status, Body: http.NoBody})

		if !errors.Is(err, push.ErrOutcomeUnknown) {
			t.Fatal("unexpected response encouraged unsafe replay")
		}
	}
}

// TestProviderRedirectDoesNotRepeat checks redirect statuses are definitive
// provider errors, not a reason to send to a capability URL in Location.
func TestProviderRedirectDoesNotRepeat(t *testing.T) {
	t.Parallel()

	driver, subscription, _ := fixture(t)
	calls := 0
	driver.client = doFunc(func(*http.Request) (*http.Response, error) {
		calls++

		return &http.Response{StatusCode: 307, Header: http.Header{"Location": {"https://127.0.0.1/secret"}}, Body: http.NoBody}, nil
	})

	_, err := driver.Send(t.Context(), push.NewMessage(subscription, "Ready"), push.SendConfig{})

	if err == nil || calls != 1 || strings.Contains(err.Error(), "secret") {
		t.Fatal("redirect followed, accepted or exposed")
	}

	var rejected *ProviderError

	if !errors.As(err, &rejected) || rejected.StatusCode != 307 {
		t.Fatal("redirect lost provider status")
	}

	if errors.Unwrap(err) != nil {
		t.Fatal("redirect acquired an unrelated portable class")
	}
}

// TestRetryAfterDate handles fixed HTTP dates and hostile or overflowing
// delay-seconds without sleeps or dependence on wall-clock time.
func TestRetryAfterDate(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)

	if retryAfter(now.Add(time.Minute).Format(http.TimeFormat), now) != time.Minute {
		t.Fatal("HTTP date delay incorrect")
	}

	for _, value := range []string{"", "-1", "9999999999999999999999999", "9223372036854775807", "secret",
		now.Add(-time.Minute).Format(http.TimeFormat), "Fri, 31 Dec 9999 23:59:59 GMT"} {
		if retryAfter(value, now) != 0 {
			t.Fatal("accepted invalid retry metadata")
		}
	}
}

// faultyBody models cleanup failure after authoritative provider acceptance.
type faultyBody struct {
	// closed records mandatory release even when optional body reading fails.
	closed bool
	// read counts the bounded bytes requested by cleanup.
	read int
}

// Read emits an unlimited response, allowing the test to enforce its cap.
func (body *faultyBody) Read(buffer []byte) (int, error) {
	body.read += len(buffer)

	return len(buffer), nil
}

// Close records release but returns an error that cannot undo acceptance.
func (body *faultyBody) Close() error {
	body.closed = true

	return errors.New("secret cleanup failure")
}

// TestAcceptedResponseCleanupCannotFail checks finite response-body reading,
// mandatory close and acceptance despite missing optional receipt metadata.
func TestAcceptedResponseCleanupCannotFail(t *testing.T) {
	t.Parallel()

	driver, subscription, _ := fixture(t)
	body := &faultyBody{}
	driver.client = doFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 201, Body: body}, nil
	})

	receipt, err := driver.Send(t.Context(), push.NewMessage(subscription, "Ready"), push.SendConfig{})

	if err != nil || receipt.ID != "" || !body.closed || body.read > 4096 {
		t.Fatal("cleanup changed acceptance or exceeded its bound")
	}
}
