package restateworkflow_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"arkoren.dev/core/service/workflow"
	"arkoren.dev/driver/restate"
	"arkoren.dev/driver/restate/restateworkflow"
)

// TestStatusPreservesPendingLookupFailure distinguishes an unavailable admin
// API from proof that an accepted but not yet executing run does not exist.
func TestStatusPreservesPendingLookupFailure(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/query" {
			writer.WriteHeader(http.StatusServiceUnavailable)

			return
		}

		writer.WriteHeader(http.StatusNotFound)
		_, _ = writer.Write([]byte(`{"message":"not found"}`))
	}))
	t.Cleanup(server.Close)
	host := restate.New(func(host *restate.Builder) {
		host.IngressURL(server.URL)
		host.AdminURL(server.URL)
	})
	driver, err := restateworkflow.NewDriver(host)

	if err != nil {
		t.Fatal(err)
	}

	_, err = driver.Status(t.Context(), "onboard", "pending")

	if err == nil || errors.Is(err, workflow.ErrNotFound) {
		t.Fatalf("Status = %v, want admin failure", err)
	}

	if !strings.Contains(err.Error(), "503") {
		t.Fatalf("Status lost the admin response: %v", err)
	}
}

// TestSignalPreservesOutputProbeFailure covers a missing Signal handler result
// followed by an unavailable result API, which is not evidence of a finished run.
func TestSignalPreservesOutputProbeFailure(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if strings.HasSuffix(request.URL.Path, "/output") {
			writer.WriteHeader(http.StatusServiceUnavailable)
			_, _ = writer.Write([]byte(`{"message":"output unavailable"}`))

			return
		}

		writer.WriteHeader(http.StatusNotFound)
		_, _ = writer.Write([]byte(`{"message":"not running"}`))
	}))
	t.Cleanup(server.Close)
	driver, err := restateworkflow.NewDriver(restate.New(func(host *restate.Builder) {
		host.IngressURL(server.URL)
	}))

	if err != nil {
		t.Fatal(err)
	}

	err = driver.Signal(t.Context(), "onboard", "pending", "approved", []byte(`{"v":true}`))

	if err == nil || errors.Is(err, workflow.ErrNotFound) {
		t.Fatalf("Signal = %v, want output API failure", err)
	}

	if !strings.Contains(err.Error(), "output unavailable") {
		t.Fatalf("Signal lost the output response: %v", err)
	}
}

// TestStartPreservesIdentityAndSeparateTraceHeaders protects the real SDK
// transport boundary while retry/error changes are made elsewhere in the driver.
func TestStartPreservesIdentityAndSeparateTraceHeaders(t *testing.T) {
	t.Parallel()

	requests := make(chan *http.Request, 1)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests <- request.Clone(context.Background())
		writer.WriteHeader(http.StatusAccepted)
		_, _ = writer.Write([]byte(`{"invocationId":"inv_test","status":"Accepted"}`))
	}))
	t.Cleanup(server.Close)
	driver, err := restateworkflow.NewDriver(restate.New(func(host *restate.Builder) {
		host.IngressURL(server.URL)
	}), func(driver *restateworkflow.DriverBuilder) {
		driver.Prefix("orders")
	})

	if err != nil {
		t.Fatal(err)
	}

	err = driver.Start(t.Context(), "onboard", "user/42?#%", []byte(`{"v":"input"}`), workflow.StartOptions{
		Trace:    map[string]string{"traceparent": "start-parent"},
		Metadata: map[string]string{"traceparent": "application-metadata"},
	})

	if err != nil {
		t.Fatal(err)
	}

	request := <-requests

	if path := request.URL.EscapedPath(); path != "/OrdersWf_onboard/user%2F42%3F%23%25/Run/send" {
		t.Fatalf("native run identity = %q", path)
	}

	if request.Header.Get("arkoren-trace-traceparent") != "start-parent" {
		t.Fatal("start trace carrier was not forwarded")
	}

	if request.Header.Get("arkoren-meta-traceparent") != "application-metadata" {
		t.Fatal("metadata was merged with the trace carrier")
	}
}
