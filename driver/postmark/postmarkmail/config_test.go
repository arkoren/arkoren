package postmarkmail

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"arkoren.dev/core/service/mail"
)

// ExampleBuilder_HTTPClient constructs a mailer with a borrowed HTTP client.
// Construction makes no request and does not transfer ownership of the client
// or its transport to the driver.
func ExampleBuilder_HTTPClient() {
	client := &http.Client{Timeout: 10 * time.Second}
	driver, err := New("server-token", func(transport *Builder) {
		transport.HTTPClient(client)
	})

	if err != nil {
		fmt.Println(err)

		return
	}

	mailer := mail.New(driver)
	fmt.Println(mailer.Driver() == driver)
	// Output: true
}

// TestPostmarkHTTPClientSendsThroughClient verifies Send uses the borrowed client
// supplied to the construction draft, including its live transport.
func TestPostmarkHTTPClientSendsThroughClient(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"MessageID":"postmark-id"}`))
	}))
	t.Cleanup(server.Close)

	var calls atomic.Int64
	client := &http.Client{
		Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
			calls.Add(1)

			return http.DefaultTransport.RoundTrip(request)
		}),
	}

	driver, err := New("postmark-token", func(transport *Builder) {
		transport.Endpoint(server.URL + "/email")
		transport.HTTPClient(client)
	})

	if err != nil {
		t.Fatal(err)
	}

	message := mail.NewMessage(mail.Recipients{
		To: []mail.Address{{
			Email: "person@example.com",
		}},
	}, mail.Body{Text: "Hi"}, func(letter *mail.MessageBuilder) {
		letter.From(mail.Address{Email: "sender@example.com"})
		letter.Subject("Hello")
	})

	if _, err := driver.Send(t.Context(), message); err != nil {
		t.Fatal(err)
	}

	if calls.Load() != 1 {
		t.Fatalf("requests through the custom client = %d, want 1", calls.Load())
	}
}

// roundTripFunc adapts a function to [http.RoundTripper].
type roundTripFunc func(*http.Request) (*http.Response, error)

// RoundTrip passes request to the fixture transport function. Its response and
// error propagate unchanged so tests can inspect HTTP client injection.
func (roundTrip roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return roundTrip(request)
}

// TestConstructionCallbacksShareDraftOnce verifies order, receiver identity,
// last-write replacement and isolation from retained root settings.
func TestConstructionCallbacksShareDraftOnce(t *testing.T) {
	t.Parallel()

	var retained *Builder
	calls := 0
	client := &http.Client{
		Timeout: time.Second,
	}

	driver, err := New("token", func(transport *Builder) {
		calls++
		retained = transport

		if transport.Endpoint("https://old.example.com/email").HTTPClient(client) != transport {
			t.Fatal("settings returned a different draft")
		}
	}, func(transport *Builder) {
		calls++

		if transport != retained || transport.config.endpoint != "https://old.example.com/email" {
			t.Fatal("callbacks did not share ordered settings")
		}

		transport.Endpoint("https://api.example.com/email")
	})

	if err != nil {
		t.Fatal(err)
	}

	retained.Endpoint("https://changed.example.com").HTTPClient(nil)

	if calls != 2 || driver.endpoint != "https://api.example.com/email" {
		t.Fatalf("callbacks = %d, endpoint = %s", calls, driver.endpoint)
	}

	if driver.http != client {
		t.Fatal("borrowed HTTP client was replaced or cloned")
	}

	client.Timeout = 2 * time.Second

	if driver.http.Timeout != 2*time.Second {
		t.Fatal("live HTTP client was recursively cloned")
	}
}

// TestConstructionEmptyCallbacksKeepDefaults verifies nil and empty expansion
// select the production endpoint and separate default HTTP clients.
func TestConstructionEmptyCallbacksKeepDefaults(t *testing.T) {
	t.Parallel()

	var policies []func(*Builder)
	driver, err := New("token", policies...)

	if err != nil {
		t.Fatal(err)
	}

	other, err := New("token", []func(*Builder){}...)

	if err != nil {
		t.Fatal(err)
	}

	if driver.endpoint != "https://api.postmarkapp.com/email" || other.endpoint != driver.endpoint {
		t.Fatal("empty callbacks changed endpoint defaults")
	}

	if driver.http.Timeout != 30*time.Second || other.http == driver.http {
		t.Fatal("default HTTP client timeout or ownership changed")
	}
}

// TestConstructionResetToDefaults verifies later empty and nil settings undo
// earlier overrides instead of retaining stale values.
func TestConstructionResetToDefaults(t *testing.T) {
	t.Parallel()

	driver, err := New("token", func(transport *Builder) {
		transport.Endpoint("https://old.example.com/email")
		transport.HTTPClient(&http.Client{
			Timeout: time.Second,
		})
	}, func(transport *Builder) {
		transport.Endpoint("")
		transport.HTTPClient(nil)
	})

	if err != nil {
		t.Fatal(err)
	}

	if driver.endpoint != "https://api.postmarkapp.com/email" || driver.http.Timeout != 30*time.Second {
		t.Fatal("cleared settings did not restore defaults")
	}
}

// TestConstructionDraftsAreFresh verifies drafts and their optional settings
// are independent between constructor invocations.
func TestConstructionDraftsAreFresh(t *testing.T) {
	t.Parallel()

	var first *Builder
	_, err := New("token", func(transport *Builder) {
		first = transport
		transport.Endpoint("https://api.example.com/email")
	})

	if err != nil {
		t.Fatal(err)
	}

	_, err = New("token", func(transport *Builder) {
		if transport == first || transport.config != (settings{}) {
			t.Fatal("draft reused")
		}
	})

	if err != nil {
		t.Fatal(err)
	}
}

// TestConstructionNilCallbackPanics verifies nil entries are not skipped even
// when required token validation would otherwise fail.
func TestConstructionNilCallbackPanics(t *testing.T) {
	t.Parallel()

	defer func() {
		if recover() == nil {
			t.Fatal("nil callback did not panic")
		}
	}()

	_, _ = New("", nil)
}

// TestConstructionCallbackPanicPropagates verifies policy panics are not
// recovered or transformed into domain validation errors.
func TestConstructionCallbackPanicPropagates(t *testing.T) {
	t.Parallel()

	defer func() {
		if got := recover(); got != "policy panic" {
			t.Fatalf("panic = %v", got)
		}
	}()

	_, _ = New("", func(transport *Builder) {
		panic("policy panic")
	})
}

// TestConstructionCallbacksPrecedeValidation verifies required token errors
// are reported only after the supplied construction callback runs once.
func TestConstructionCallbacksPrecedeValidation(t *testing.T) {
	t.Parallel()

	calls := 0
	_, err := New("", func(transport *Builder) {
		calls++
		transport.Endpoint("https://api.example.com/email")
	})

	if err == nil || calls != 1 {
		t.Fatalf("err = %v, callbacks = %d", err, calls)
	}
}
