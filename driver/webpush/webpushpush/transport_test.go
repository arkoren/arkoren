package webpushpush

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// roundTripFunc substitutes provider responses while retaining http.Client's
// real redirect handling in the production client configuration.
type roundTripFunc func(*http.Request) (*http.Response, error)

// RoundTrip executes one deterministic provider exchange.
func (exchange roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return exchange(request)
}

// TestClientRejectsRedirect exercises real http.Client redirect processing,
// not merely the configured callback in isolation.
func TestClientRejectsRedirect(t *testing.T) {
	t.Parallel()

	client := newClient(time.Second)
	calls := 0
	client.Transport = roundTripFunc(func(request *http.Request) (*http.Response, error) {
		calls++

		return &http.Response{
			StatusCode: http.StatusTemporaryRedirect, Request: request,
			Header: http.Header{"Location": {"https://127.0.0.1/secret"}},
			Body:   io.NopCloser(strings.NewReader("untrusted")),
		}, nil
	})

	request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, "https://fcm.googleapis.com/path", strings.NewReader("encrypted"))

	if err != nil {
		t.Fatal(err)
	}

	response, err := client.Do(request)

	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		response.Body.Close()
	})

	if response.StatusCode != 307 || calls != 1 {
		t.Fatal("client followed redirect")
	}
}

// TestClientSecurityPolicy checks the production transport, not the private
// test seam: proxies, redirects and connection replay remain disabled.
func TestClientSecurityPolicy(t *testing.T) {
	t.Parallel()

	client := newClient(time.Second)
	transport := client.Transport.(*http.Transport)

	if transport.Proxy != nil || !transport.DisableKeepAlives || transport.ForceAttemptHTTP2 {
		t.Fatal("proxy or retry-enabling transport policy")
	}

	if transport.TLSClientConfig != nil || transport.DialContext == nil {
		t.Fatal("TLS verification changed or secure dialing omitted")
	}

	if transport.MaxResponseHeaderBytes != 16<<10 || client.Timeout != time.Second {
		t.Fatal("transport bounds missing")
	}

	if err := client.CheckRedirect(nil, nil); !errors.Is(err, http.ErrUseLastResponse) {
		t.Fatal("redirect policy permits following capability URLs")
	}
}
