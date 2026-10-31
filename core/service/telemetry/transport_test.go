package telemetry_test

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"arkoren.dev/core/service/telemetry"
	"arkoren.dev/core/service/telemetry/telemetrytest"
)

// errRoundTrip is returned by failingTransport.
var errRoundTrip = errors.New("connection refused: secret-host")

// failingTransport fails every request.
type failingTransport struct{}

// RoundTrip implements [http.RoundTripper].
func (failingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errRoundTrip
}

// echoServer returns a server that records the request headers it received.
func echoServer(t *testing.T, status int, received *http.Header) *httptest.Server {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*received = r.Header.Clone()
		w.WriteHeader(status)

		_, _ = io.WriteString(w, "body")
	}))
	t.Cleanup(server.Close)

	return server
}

// get performs a GET with client and drains and closes the body.
func get(t *testing.T, client *http.Client, req *http.Request) *http.Response {
	t.Helper()

	response, err := client.Do(req)

	if err != nil {
		t.Fatalf("do: %v", err)
	}

	if _, err := io.Copy(io.Discard, response.Body); err != nil {
		t.Fatalf("read: %v", err)
	}

	if err := response.Body.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	return response
}

// TestTransportInjectsPropagationIntoClone verifies injection without mutating the caller's request.
func TestTransportInjectsPropagationIntoClone(t *testing.T) {
	t.Parallel()

	var received http.Header
	server := echoServer(t, http.StatusOK, &received)
	fake := telemetrytest.New()
	instance := fake.Telemetry(t)
	client := &http.Client{Transport: instance.Transport(nil)}

	ctx, parent := instance.Tracer("test").Start(t.Context(), "parent")

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/secret/path?token=abc", nil)

	if err != nil {
		t.Fatal(err)
	}

	get(t, client, req)
	parent.End()

	if len(req.Header) != 0 {
		t.Errorf("caller request mutated: %v", req.Header)
	}

	span, found := fake.Span("GET")

	if !found {
		t.Fatal("client span missing")
	}

	want := "00-" + parent.Context().TraceID.String() + "-" + span.Context.SpanID.String() + "-01"

	if got := received.Get("Traceparent"); got != want {
		t.Errorf("traceparent = %q, want %q", got, want)
	}

	if span.Parent.SpanID != parent.Context().SpanID || span.Kind != telemetry.Client {
		t.Errorf("parent = %v kind = %v", span.Parent, span.Kind)
	}

	for _, attr := range span.Attrs {
		if attr.Text() == "/secret/path" || attr.Key() == "url.full" || attr.Key() == "url.path" {
			t.Errorf("leaked attribute %v", attr)
		}
	}
}

// TestTransportRecordsSpanAndMetric verifies attributes and metric labels.
func TestTransportRecordsSpanAndMetric(t *testing.T) {
	t.Parallel()

	var received http.Header
	server := echoServer(t, http.StatusBadGateway, &received)
	fake := telemetrytest.New()
	client := &http.Client{Transport: fake.Telemetry(t).Transport(nil)}
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, server.URL, nil)

	get(t, client, req)

	host := req.URL.Hostname()
	port, _ := strconv.Atoi(req.URL.Port())

	fake.AssertSpan(t, "GET",
		telemetry.String("http.request.method", "GET"),
		telemetry.String("server.address", host),
		telemetry.Int("server.port", port),
		telemetry.String("url.scheme", "http"),
		telemetry.Int("http.response.status_code", http.StatusBadGateway),
		telemetry.String("error.type", "502"),
	)

	span, _ := fake.Span("GET")

	if span.Status != telemetry.Error || span.StatusDescription != "HTTP 502" {
		t.Errorf("status = %v %q", span.Status, span.StatusDescription)
	}

	fake.AssertHistogram(t, "http.client.request.duration", 1,
		telemetry.String("http.request.method", "GET"),
		telemetry.String("server.address", host),
		telemetry.String("http.response.status_code", "502"),
		telemetry.String("error.type", "502"),
	)
}

// TestTransportEndsSpanWhenBodyCloses verifies the span covers the body lifetime.
func TestTransportEndsSpanWhenBodyCloses(t *testing.T) {
	t.Parallel()

	var received http.Header
	server := echoServer(t, http.StatusOK, &received)
	fake := telemetrytest.New()
	client := &http.Client{Transport: fake.Telemetry(t).Transport(nil)}
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, server.URL, nil)

	response, err := client.Do(req)

	if err != nil {
		t.Fatal(err)
	}

	fake.AssertSpanCount(t, 0)

	if err := response.Body.Close(); err != nil {
		t.Fatal(err)
	}

	fake.AssertSpanCount(t, 1)

	if err := response.Body.Close(); err != nil {
		t.Fatal(err)
	}

	fake.AssertSpanCount(t, 1)
}

// TestTransportTransportErrorMarksSpan verifies the error path records the type, not the text.
func TestTransportTransportErrorMarksSpan(t *testing.T) {
	t.Parallel()

	fake := telemetrytest.New()
	client := &http.Client{Transport: fake.Telemetry(t).Transport(failingTransport{})}
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodPost, "https://example.com/x", nil)

	if _, err := client.Do(req); !errors.Is(err, errRoundTrip) {
		t.Fatalf("err = %v", err)
	}

	span, found := fake.Span("POST")

	if !found || span.Status != telemetry.Error {
		t.Fatalf("span = %+v found %v", span, found)
	}

	if span.StatusDescription != "*errors.errorString" {
		t.Errorf("description = %q", span.StatusDescription)
	}

	if port, _ := span.Attr("server.port"); port.AsInt64() != 443 {
		t.Errorf("server.port = %v", port)
	}

	for _, attr := range span.Attrs {
		if attr.Text() == errRoundTrip.Error() {
			t.Errorf("leaked error text in %v", attr)
		}
	}

	fake.AssertHistogram(t, "http.client.request.duration", 1, telemetry.String("error.type", "*errors.errorString"))
}

// TestTransportInactiveDelegates verifies inactive telemetry leaves requests untouched.
func TestTransportInactiveDelegates(t *testing.T) {
	t.Parallel()

	var received http.Header
	server := echoServer(t, http.StatusOK, &received)
	client := &http.Client{Transport: telemetry.New(nil).Transport(nil)}
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, server.URL, nil)

	get(t, client, req)

	if received.Get("Traceparent") != "" {
		t.Errorf("traceparent injected: %v", received)
	}
}

// TestPackageTransportResolvesDefaultLazily verifies the default is read per request.
// It changes the process default, so it must not run in parallel.
func TestPackageTransportResolvesDefaultLazily(t *testing.T) {
	var received http.Header
	server := echoServer(t, http.StatusOK, &received)
	client := &http.Client{Transport: telemetry.Transport(nil)}
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, server.URL, nil)

	get(t, client, req)

	fake := telemetrytest.New()
	telemetry.SetDefault(fake.Telemetry(t))
	t.Cleanup(func() {
		telemetry.SetDefault(nil)
	})

	get(t, client, req)

	fake.AssertSpanCount(t, 1)

	if received.Get("Traceparent") == "" {
		t.Error("traceparent not injected after SetDefault")
	}
}

// transportRecorder is a base transport that records the headers it was sent.
type transportRecorder struct {
	// header is the header of the last request.
	header http.Header
}

// RoundTrip implements [http.RoundTripper].
func (recorder *transportRecorder) RoundTrip(req *http.Request) (*http.Response, error) {
	recorder.header = req.Header.Clone()

	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       http.NoBody,
		Header:     http.Header{},
		Request:    req,
	}, nil
}

// propagates sends a GET to target through a transport built with opts and
// reports whether traceparent was injected, and whether a client span was
// recorded.
func propagates(t *testing.T, target string, opts ...func(*telemetry.TransportBuilder)) bool {
	t.Helper()

	fake := telemetrytest.New()
	instance := fake.Telemetry(t)
	recorder := &transportRecorder{}
	client := &http.Client{Transport: instance.Transport(recorder, opts...)}

	ctx, parent := instance.Tracer("test").Start(t.Context(), "parent")
	t.Cleanup(func() {
		parent.End()
	})

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)

	if err != nil {
		t.Fatal(err)
	}

	get(t, client, req)

	if _, found := fake.Span("GET"); !found {
		t.Fatalf("%s: client span missing", target)
	}

	return recorder.header.Get("Traceparent") != ""
}

// TestTransportDefaultPropagatesInternalDestinations verifies each internal host class receives headers.
func TestTransportDefaultPropagatesInternalDestinations(t *testing.T) {
	t.Parallel()

	for _, target := range []string{
		"http://127.0.0.1:8080/x", "http://10.1.2.3/x", "http://172.16.0.1/x", "http://192.168.0.9/x",
		"http://169.254.169.254/x", "http://[::1]:80/x", "http://[fd00::1]/x", "http://[fe80::1]/x",
		"http://users/x", "http://users:8080/x", "http://localhost/x",
		"http://db.internal/x", "http://a.b.local:9/x", "http://api.ns.svc/x", "http://api.ns.svc.cluster.local/x",
		"http://API.INTERNAL/x", "http://users./x",
	} {
		if !propagates(t, target) {
			t.Errorf("%s: traceparent not injected", target)
		}
	}
}

// TestTransportDefaultSkipsExternalDestinations verifies third parties receive no headers but are still traced.
func TestTransportDefaultSkipsExternalDestinations(t *testing.T) {
	t.Parallel()

	for _, target := range []string{
		"https://api.example.com/x", "http://8.8.8.8/x", "http://[2001:db8::1]/x", "https://evil.internal.example.com/x",
		"http://example.com/x.local", "http://example.localhost.com/x",
	} {
		if propagates(t, target) {
			t.Errorf("%s: traceparent injected", target)
		}
	}
}

// TestTransportPropagationHosts verifies exact and wildcard allow-listing next to the internal default.
func TestTransportPropagationHosts(t *testing.T) {
	t.Parallel()

	option := func(transport *telemetry.TransportBuilder) {
		transport.PropagationHosts("api.partner.com", "*.Example.com")
	}

	for target, want := range map[string]bool{
		"https://API.partner.com:8443/x": true, "https://a.example.com/x": true, "https://a.b.example.com/x": true,
		"https://example.com/x": false, "https://badexample.com/x": false, "https://partner.com/x": false,
		"https://other.com/x": false, "http://users/x": true,
	} {
		if got := propagates(t, target, option); got != want {
			t.Errorf("%s: propagated = %v, want %v", target, got, want)
		}
	}
}

// TestTransportPropagateAllAndNone verifies the fixed predicates and a custom one.
func TestTransportPropagateAllAndNone(t *testing.T) {
	t.Parallel()

	if !propagates(t, "https://api.example.com/x", func(transport *telemetry.TransportBuilder) {
		transport.Propagation(telemetry.PropagateAll)
	}) {
		t.Error("PropagateAll did not inject")
	}

	if propagates(t, "http://users/x", func(transport *telemetry.TransportBuilder) {
		transport.Propagation(telemetry.PropagateNone)
	}) {
		t.Error("PropagateNone injected")
	}

	if !propagates(t, "https://api.example.com/x", func(transport *telemetry.TransportBuilder) {
		transport.Propagation(func(request *http.Request) bool {
			return request.URL.Path == "/x"
		})
	}) {
		t.Error("custom predicate did not inject")
	}
}

// TestUnknownClientMethodUsesHTTPSpanName checks the outgoing public
// transport seam without network I/O. Unknown methods keep _OTHER as their
// attribute, but use the semantic-convention HTTP span name.
func TestUnknownClientMethodUsesHTTPSpanName(t *testing.T) {
	t.Parallel()

	fake := telemetrytest.New()
	transport := fake.Telemetry(t).Transport(failingTransport{})

	_, err := transport.RoundTrip(httptest.NewRequest("BREW", "https://example.test/private", nil))

	if !errors.Is(err, errRoundTrip) {
		t.Fatalf("RoundTrip error = %v, want injected transport failure", err)
	}

	span, ok := fake.Span("HTTP")

	if !ok {
		t.Fatalf("spans = %+v, want HTTP", fake.Spans())
	}

	if attr, ok := span.Attr("http.request.method"); !ok || attr.AsString() != "_OTHER" {
		t.Fatalf("request method = %v, found %v; want _OTHER", attr, ok)
	}
}
