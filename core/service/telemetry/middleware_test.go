package telemetry_test

import (
	"encoding/json/v2"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"

	"arkoren.dev/core/http/router"
	"arkoren.dev/core/service/telemetry"
	"arkoren.dev/core/service/telemetry/memorytelemetry"
	"arkoren.dev/core/service/telemetry/telemetrytest"
	"arkoren.dev/foundation/problem"
)

const (
	// remoteTraceID and remoteSpanID identify the remote parent in traceparent.
	remoteTraceID = "4bf92f3577b34da6a3ce929d0e0e4736"
	// remoteSpanID identifies the remote parent span in propagation tests.
	remoteSpanID = "00f067aa0ba902b7"

	// remoteTraceparent is a sampled W3C traceparent header value.
	remoteTraceparent = "00-" + remoteTraceID + "-" + remoteSpanID + "-01"
)

// newRouter returns a router with mw registered first and routes for the tests.
func newRouter(mw ...router.Middleware) *router.Router {
	app := router.New()
	app.Use(mw...)
	app.Get("/users/{id}", func(w http.ResponseWriter, _ *http.Request) error {
		w.WriteHeader(http.StatusNoContent)

		return nil
	})
	app.Get("/bad", func(http.ResponseWriter, *http.Request) error {
		return (problem.Details{}).Derive(func(details *problem.Builder) {
			details.Status(http.StatusBadRequest)
			details.Title("invalid")
		})
	})
	app.Get("/fail", func(http.ResponseWriter, *http.Request) error {
		return errors.New("sensitive internal failure")
	})
	app.Get("/panic", func(http.ResponseWriter, *http.Request) error {
		panic("unexpected")
	})

	return app
}

// recordGet serves GET target through app from a private peer address.
func recordGet(app *router.Router, target string, headers ...string) *http.Response {
	return recordFrom(app, "10.1.2.3:4567", target, headers...)
}

// recordFrom serves GET target through app as if sent from remote.
func recordFrom(app *router.Router, remote, target string, headers ...string) *http.Response {
	req := httptest.NewRequest(http.MethodGet, target, nil)
	req.RemoteAddr = remote

	for index := 0; index+1 < len(headers); index += 2 {
		req.Header.Set(headers[index], headers[index+1])
	}

	return app.Record(req)
}

// TestMiddlewareContinuesRemoteParent verifies explicit internal trust joins
// the remote trace and preserves its parent span.
func TestMiddlewareContinuesRemoteParent(t *testing.T) {
	t.Parallel()

	fake := telemetrytest.New()
	app := newRouter(fake.Telemetry(t).Middleware(func(middleware *telemetry.MiddlewareBuilder) {
		middleware.TrustRemote(telemetry.TrustInternal)
	}))

	recordGet(app, "/users/7", "Traceparent", remoteTraceparent)

	span, found := fake.Span("GET /users/{id}")

	if !found {
		t.Fatal("server span missing")
	}

	if got := span.Context.TraceID.String(); got != remoteTraceID {
		t.Errorf("trace ID = %q, want %q", got, remoteTraceID)
	}

	if got := span.Parent.SpanID.String(); got != remoteSpanID {
		t.Errorf("parent span ID = %q, want %q", got, remoteSpanID)
	}

	if span.Kind != telemetry.Server {
		t.Errorf("kind = %v, want server", span.Kind)
	}
}

// TestMiddlewareRecordsRouteAndStatus verifies attributes and the final status.
func TestMiddlewareRecordsRouteAndStatus(t *testing.T) {
	t.Parallel()

	fake := telemetrytest.New()
	app := newRouter(fake.Telemetry(t).Middleware())

	response := recordGet(app, "/bad?token=secret")

	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d", response.StatusCode)
	}

	fake.AssertSpan(t, "GET /bad",
		telemetry.String("http.request.method", "GET"),
		telemetry.String("http.route", "/bad"),
		telemetry.String("url.scheme", "http"),
		telemetry.String("network.protocol.name", "http"),
		telemetry.Int("http.response.status_code", http.StatusBadRequest),
	)

	span, _ := fake.Span("GET /bad")

	if span.Status != telemetry.Unset {
		t.Errorf("4xx status = %v, want unset", span.Status)
	}

	for _, attr := range span.Attrs {
		if strings.Contains(attr.Text(), "secret") || strings.HasPrefix(attr.Key(), "url.path") || attr.Key() == "url.query" {
			t.Errorf("leaked attribute %v", attr)
		}
	}
}

// TestMiddlewareMarksRenderedServerErrors verifies 5xx spans fail without leaking error text.
func TestMiddlewareMarksRenderedServerErrors(t *testing.T) {
	t.Parallel()

	fake := telemetrytest.New()
	app := newRouter(fake.Telemetry(t).Middleware())

	response := recordGet(app, "/fail")

	if response.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status = %d", response.StatusCode)
	}

	span, found := fake.Span("GET /fail")

	if !found {
		t.Fatal("server span missing")
	}

	if span.Status != telemetry.Error || span.StatusDescription != "HTTP 500" {
		t.Errorf("status = %v %q", span.Status, span.StatusDescription)
	}

	if len(span.Events) != 0 {
		t.Errorf("events = %d, want 0", len(span.Events))
	}

	if errorType, _ := span.Attr("error.type"); errorType.AsString() != "500" {
		t.Errorf("error.type = %q", errorType.AsString())
	}
}

// TestMiddlewareObservesRecoveredPanic verifies a recovered panic ends the span as an error.
func TestMiddlewareObservesRecoveredPanic(t *testing.T) {
	t.Parallel()

	fake := telemetrytest.New()
	app := newRouter(fake.Telemetry(t).Middleware(), router.Recover())

	response := recordGet(app, "/panic")

	if response.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status = %d", response.StatusCode)
	}

	span, found := fake.Span("GET /panic")

	if !found || span.Status != telemetry.Error {
		t.Fatalf("span = %+v, found %v", span, found)
	}
}

// TestMiddlewareRecordsMetrics verifies the duration histogram and active counter.
func TestMiddlewareRecordsMetrics(t *testing.T) {
	t.Parallel()

	fake := telemetrytest.New()
	app := newRouter(fake.Telemetry(t).Middleware())

	recordGet(app, "/users/1")
	recordGet(app, "/fail")

	fake.AssertHistogram(t, "http.server.request.duration", 1,
		telemetry.String("http.request.method", "GET"),
		telemetry.String("http.route", "/users/{id}"),
		telemetry.String("http.response.status_code", "204"),
		telemetry.String("url.scheme", "http"),
		telemetry.String("error.type", ""),
	)
	fake.AssertHistogram(t, "http.server.request.duration", 1,
		telemetry.String("http.route", "/fail"),
		telemetry.String("http.response.status_code", "500"),
		telemetry.String("error.type", "500"),
	)
	fake.AssertCounter(t, "http.server.active_requests", 0)
}

// TestMiddlewareExposesTraceID verifies the response headers and problem member.
func TestMiddlewareExposesTraceID(t *testing.T) {
	t.Parallel()

	fake := telemetrytest.New()
	app := newRouter(fake.Telemetry(t).Middleware(func(middleware *telemetry.MiddlewareBuilder) {
		middleware.TraceHeader("X-Trace-ID")
	}))

	response := recordGet(app, "/bad", "Traceparent", remoteTraceparent)

	span, _ := fake.Span("GET /bad")
	traceID := span.Context.TraceID.String()

	if traceID == remoteTraceID {
		t.Fatal("response used the untrusted caller's trace ID")
	}

	want := "00-" + traceID + "-" + span.Context.SpanID.String() + "-01"

	if got := response.Header.Get("Traceparent"); got != "" {
		t.Errorf("traceparent leaked on response: %q", got)
	}

	if got := response.Header.Get("traceresponse"); got != want {
		t.Errorf("traceresponse = %q, want %q", got, want)
	}

	if got := response.Header.Get("X-Trace-ID"); got != traceID {
		t.Errorf("X-Trace-ID = %q", got)
	}

	var body map[string]any

	if err := json.UnmarshalRead(response.Body, &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}

	if body["trace_id"] != traceID {
		t.Errorf("trace_id = %v", body["trace_id"])
	}
}

// TestMiddlewareOptionsDisableTraceID verifies each exposure can be turned off or renamed.
func TestMiddlewareOptionsDisableTraceID(t *testing.T) {
	t.Parallel()

	fake := telemetrytest.New()
	app := newRouter(fake.Telemetry(t).Middleware(func(middleware *telemetry.MiddlewareBuilder) {
		middleware.TraceResponse(false)
	}, func(middleware *telemetry.MiddlewareBuilder) {
		middleware.ProblemKey("request_id")
	}))

	response := recordGet(app, "/bad")

	if got := response.Header.Get("traceresponse"); got != "" {
		t.Errorf("traceresponse = %q, want none", got)
	}

	var body map[string]any

	if err := json.UnmarshalRead(response.Body, &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}

	if _, found := body["trace_id"]; found {
		t.Error("default problem key present")
	}

	if body["request_id"] == nil {
		t.Error("custom problem key missing")
	}

	disabled := newRouter(fake.Telemetry(t).Middleware(func(middleware *telemetry.MiddlewareBuilder) {
		middleware.ProblemKey("")
	}))

	if err := json.UnmarshalRead(recordGet(disabled, "/bad").Body, &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}

	if _, found := body["trace_id"]; found {
		t.Error("problem key present although disabled")
	}
}

// checkMiddlewareNoopSetsNothing fails the test unless a request through
// instance's middleware gets no trace headers or body trace_id.
func checkMiddlewareNoopSetsNothing(t *testing.T, instance *telemetry.Telemetry) {
	t.Helper()

	app := newRouter(instance.Middleware(func(middleware *telemetry.MiddlewareBuilder) {
		middleware.TraceHeader("X-Trace-ID")
	}))

	response := recordGet(app, "/bad", "Traceparent", remoteTraceparent)

	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d", response.StatusCode)
	}

	if response.Header.Get("traceresponse") != "" || response.Header.Get("X-Trace-ID") != "" {
		t.Errorf("headers = %v", response.Header)
	}

	var body map[string]any

	if err := json.UnmarshalRead(response.Body, &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}

	if _, found := body["trace_id"]; found {
		t.Error("trace_id present")
	}
}

// TestMiddlewareNoopSetsNothing verifies a noop telemetry passes requests
// through.
func TestMiddlewareNoopSetsNothing(t *testing.T) {
	t.Parallel()

	checkMiddlewareNoopSetsNothing(t, telemetry.New(nil))
}

// TestMiddlewareNotStartedSetsNothing verifies telemetry that was never started
// passes requests through.
func TestMiddlewareNotStartedSetsNothing(t *testing.T) {
	t.Parallel()

	checkMiddlewareNoopSetsNothing(t, telemetry.New(memorytelemetry.New()))
}

// TestMiddlewareUnmatchedRouteUsesMethodOnly verifies the span name without a route.
func TestMiddlewareUnmatchedRouteUsesMethodOnly(t *testing.T) {
	t.Parallel()

	fake := telemetrytest.New()
	app := router.New()
	app.Use(fake.Telemetry(t).Middleware())

	req := httptest.NewRequest("BREW", "/nothing", nil)
	app.Record(req)

	fake.AssertSpan(t, "HTTP")
}

// TestPackageMiddlewareResolvesDefaultLazily verifies the default is read per request.
// It changes the process default, so it must not run in parallel.
func TestPackageMiddlewareResolvesDefaultLazily(t *testing.T) {
	middleware := telemetry.Middleware()
	app := newRouter(middleware)

	recordGet(app, "/users/1")

	fake := telemetrytest.New()
	telemetry.SetDefault(fake.Telemetry(t))
	t.Cleanup(func() {
		telemetry.SetDefault(nil)
	})

	recordGet(app, "/users/2")

	fake.AssertSpanCount(t, 1)
	fake.AssertSpan(t, "GET /users/{id}")
}

// requireTrace fails unless the server span continues the remote trace
// exactly when joined is true.
func requireTrace(t *testing.T, fake *telemetrytest.Fake, joined bool, remote string) {
	t.Helper()

	span, found := fake.Span("GET /users/{id}")

	if !found {
		t.Fatalf("%s: server span missing", remote)
	}

	if !span.Context.IsValid() {
		t.Fatalf("%s: server span has no valid context", remote)
	}

	if got := span.Context.TraceID.String() == remoteTraceID; got != joined {
		t.Errorf("%s: joined remote trace = %v, want %v", remote, got, joined)
	}

	if span.Parent.IsValid() != joined {
		t.Errorf("%s: parent = %+v, want valid %v", remote, span.Parent, joined)
	}
}

// TestMiddlewareDefaultRejectsInternalPeers verifies even loopback, private,
// ULA, and link-local peers cannot supply a parent without explicit trust.
func TestMiddlewareDefaultRejectsInternalPeers(t *testing.T) {
	t.Parallel()

	for _, remote := range []string{"127.0.0.1:1", "10.0.0.1:1", "172.16.5.4:1", "192.168.1.1:1", "169.254.1.1:1", "[::1]:1", "[fd12:3456::1]:1", "[fe80::1%eth0]:1", "[::ffff:10.0.0.1]:1"} {
		fake := telemetrytest.New()
		app := newRouter(fake.Telemetry(t).Middleware())

		recordFrom(app, remote, "/users/7", "Traceparent", remoteTraceparent)

		requireTrace(t, fake, false, remote)
	}
}

// TestMiddlewareNilTrustRejectsAllPeers verifies explicit nil resets an earlier
// opt-in to the safe default for internal, public, and malformed peers.
func TestMiddlewareNilTrustRejectsAllPeers(t *testing.T) {
	t.Parallel()

	for _, remote := range []string{"127.0.0.1:1", "10.0.0.1:1", "[::1]:1", "[fd12:3456::1]:1", "169.254.1.1:1", "203.0.113.9:1", "[2001:db8::1]:1", "garbage"} {
		fake := telemetrytest.New()
		app := newRouter(fake.Telemetry(t).Middleware(func(middleware *telemetry.MiddlewareBuilder) {
			middleware.TrustRemote(telemetry.TrustAll)
			middleware.TrustRemote(nil)
		}))

		recordFrom(app, remote, "/users/7", "Traceparent", remoteTraceparent)

		requireTrace(t, fake, false, remote)
	}
}

// TestMiddlewareTrustInternalAcceptsInternalPeers preserves explicit opt-in
// for loopback, private, ULA, link-local, and IPv4-mapped private addresses.
func TestMiddlewareTrustInternalAcceptsInternalPeers(t *testing.T) {
	t.Parallel()

	for _, remote := range []string{"127.0.0.1:1", "10.0.0.1:1", "172.16.5.4:1", "192.168.1.1:1", "169.254.1.1:1", "[::1]:1", "[fd12:3456::1]:1", "[fe80::1%eth0]:1", "[::ffff:10.0.0.1]:1"} {
		fake := telemetrytest.New()
		app := newRouter(fake.Telemetry(t).Middleware(func(middleware *telemetry.MiddlewareBuilder) {
			middleware.TrustRemote(telemetry.TrustInternal)
		}))

		recordFrom(app, remote, "/users/7", "Traceparent", remoteTraceparent)

		requireTrace(t, fake, true, remote)
	}
}

// TestMiddlewareTrustInternalRejectsPublicPeers preserves the opt-in policy's
// refusal to trust public or malformed peers, regardless of forwarded headers.
func TestMiddlewareTrustInternalRejectsPublicPeers(t *testing.T) {
	t.Parallel()

	for _, remote := range []string{"203.0.113.9:1", "[2001:db8::1]:1", "[::ffff:8.8.8.8]:1", "garbage"} {
		fake := telemetrytest.New()
		app := newRouter(fake.Telemetry(t).Middleware(func(middleware *telemetry.MiddlewareBuilder) {
			middleware.TrustRemote(telemetry.TrustInternal)
		}))

		recordFrom(app, remote, "/users/7", "Traceparent", remoteTraceparent, "X-Forwarded-For", "10.0.0.1")

		requireTrace(t, fake, false, remote)
	}
}

// TestMiddlewareDefaultDistrustsPublicPeers verifies public and unparseable peers start a new root.
func TestMiddlewareDefaultDistrustsPublicPeers(t *testing.T) {
	t.Parallel()

	for _, remote := range []string{"203.0.113.9:1", "8.8.8.8:443", "[2001:db8::1]:1", "[::ffff:8.8.8.8]:1", "", "@", "garbage"} {
		fake := telemetrytest.New()
		app := newRouter(fake.Telemetry(t).Middleware())

		recordFrom(app, remote, "/users/7", "Traceparent", remoteTraceparent, "X-Forwarded-For", "10.0.0.1")

		requireTrace(t, fake, false, remote)
	}
}

// TestMiddlewareTrustAllAndNone verifies the fixed predicates.
func TestMiddlewareTrustAllAndNone(t *testing.T) {
	t.Parallel()

	all := telemetrytest.New()
	recordFrom(newRouter(all.Telemetry(t).Middleware(func(middleware *telemetry.MiddlewareBuilder) {
		middleware.TrustRemote(telemetry.TrustAll)
	})), "203.0.113.9:1", "/users/7", "Traceparent", remoteTraceparent)

	requireTrace(t, all, true, "TrustAll public")

	none := telemetrytest.New()
	recordFrom(newRouter(none.Telemetry(t).Middleware(func(middleware *telemetry.MiddlewareBuilder) {
		middleware.TrustRemote(telemetry.TrustNone)
	})), "127.0.0.1:1", "/users/7", "Traceparent", remoteTraceparent)

	requireTrace(t, none, false, "TrustNone loopback")
}

// TestMiddlewareTrustedNetworks verifies only the listed prefixes are trusted.
func TestMiddlewareTrustedNetworks(t *testing.T) {
	t.Parallel()

	option := func(middleware *telemetry.MiddlewareBuilder) {
		middleware.TrustedNetworks(netip.MustParsePrefix("203.0.113.0/24"), netip.MustParsePrefix("2001:db8::/32"))
	}

	for remote, want := range map[string]bool{
		"203.0.113.9:1": true, "[2001:db8::1]:1": true, "[::ffff:203.0.113.9]:1": true,
		"127.0.0.1:1": false, "10.0.0.1:1": false, "198.51.100.1:1": false,
	} {
		fake := telemetrytest.New()
		recordFrom(newRouter(fake.Telemetry(t).Middleware(option)), remote, "/users/7", "Traceparent", remoteTraceparent)

		requireTrace(t, fake, want, remote)
	}
}

// TestMiddlewareCustomTrustSeesRequest verifies the predicate receives the request.
func TestMiddlewareCustomTrustSeesRequest(t *testing.T) {
	t.Parallel()

	fake := telemetrytest.New()
	trust := func(middleware *telemetry.MiddlewareBuilder) {
		middleware.TrustRemote(func(request *http.Request) bool {
			return request.Header.Get("X-Mesh") == "1"
		})
	}

	app := newRouter(fake.Telemetry(t).Middleware(trust))

	recordFrom(app, "203.0.113.9:1", "/users/7", "Traceparent", remoteTraceparent, "X-Mesh", "1")

	requireTrace(t, fake, true, "custom")
}

// TestUnknownServerMethodUsesHTTPSpanName checks public captured spans against
// HTTP semantic conventions: unknown methods use HTTP in the span name, while
// the bounded http.request.method attribute remains _OTHER.
func TestUnknownServerMethodUsesHTTPSpanName(t *testing.T) {
	t.Parallel()

	fake := telemetrytest.New()
	routes := router.New()
	routes.Use(fake.Telemetry(t).Middleware())
	routes.Record(httptest.NewRequest("BREW", "/nothing", nil))

	span, ok := fake.Span("HTTP")

	if !ok {
		t.Fatalf("spans = %+v, want HTTP", fake.Spans())
	}

	if attr, ok := span.Attr("http.request.method"); !ok || attr.AsString() != "_OTHER" {
		t.Fatalf("request method = %v, found %v; want _OTHER", attr, ok)
	}
}

// TestMiddlewareRejectsMissingHooksBeforeRecording invokes the public
// middleware without the router's lifecycle wrapper. The documented misuse
// panic must occur before a span or active-request measurement is started.
func TestMiddlewareRejectsMissingHooksBeforeRecording(t *testing.T) {
	t.Parallel()

	fake := telemetrytest.New()
	handler := fake.Telemetry(t).Middleware()(func(http.ResponseWriter, *http.Request) error {
		t.Error("handler called without required router hooks")

		return nil
	})

	var failure any
	func() {
		defer func() {
			failure = recover()
		}()
		_ = handler(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/operation", nil))
	}()

	if failure == nil {
		t.Fatal("middleware without router hooks did not panic")
	}

	if metrics := fake.Metrics(); len(metrics) != 0 {
		t.Fatalf("metrics started before missing-hooks panic = %+v", metrics)
	}
}
