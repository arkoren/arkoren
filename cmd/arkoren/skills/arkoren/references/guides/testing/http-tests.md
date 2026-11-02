---
title: HTTP Tests
description: Exercise route matching, middleware, and response contracts with in-process HTTP requests.
order: 20
summary:
  - Send requests through the router and assert decoded responses without opening a port.
  - Check input failures and middleware behavior while owning every response body.
  - Carry cookies between requests and choose networked tests for transport behavior.
---

# HTTP Tests

Send an HTTP request through your application's router to test routing, middleware, and handler behavior together. `Record` runs the full router pipeline using an in-memory response recorder: it does not bind a port or make a network call. Your handler still receives a genuine `*http.Request`.

## Record Requests

Reuse your application's [route registration](../http-fundamentals/routing.md#register-handlers) when testing a feature. Inject test dependencies before registering its handlers; otherwise a test can accidentally retain a production backend.

To illustrate the boundary, this small route returns the report ID selected by routing. Import `net/http`, `arkoren.dev/core/http/response`, and `arkoren.dev/core/http/router`:

```go
type reportView struct {
	ID string `json:"id"`
}

func reportRoutes() *router.Router {
	routes := router.New()
	routes.Get("/reports/{id}", func(w http.ResponseWriter, r *http.Request) error {
		return response.JSON(w, http.StatusOK, reportView{ID: r.PathValue("id")})
	})
	return routes
}
```

The route intentionally has no persistence or authorization behavior. In an application test, replace `reportRoutes()` with your production route factory and its test dependencies.

Add `testing`, `net/http/httptest`, and `encoding/json/v2` for the test. Check the HTTP status before interpreting the body, and register body cleanup before assertions can stop the test:

```go
func TestReportRoute(t *testing.T) {
	routes := reportRoutes()
	res := routes.Record(httptest.NewRequest(http.MethodGet, "/reports/monthly", nil))
	t.Cleanup(func() {
		if err := res.Body.Close(); err != nil {
			t.Error(err)
		}
	})
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.StatusCode)
	}

	var report reportView
	if err := json.UnmarshalRead(res.Body, &report); err != nil {
		t.Fatal(err)
	}
	if report.ID != "monthly" {
		t.Fatalf("report ID = %q, want monthly", report.ID)
	}
}
```

This checks that the path pattern selected the ID and the response encoded it. `Record` returns an `*http.Response`; you own its body even though no socket was opened. Apply the same cleanup to every recorded response below.

## Submit JSON

When testing a create endpoint, send the content type and body a client would send. With `strings` imported and your configured router named `routes`, create a request inside your test:

```go
incoming := httptest.NewRequest(http.MethodPost, "/reports", strings.NewReader(`{"name":"Monthly"}`))
incoming.Header.Set("Content-Type", "application/json")
res := routes.Record(incoming)
```

Assert the endpoint's promised status, response headers, and decoded fields, not merely that it did not panic. The router does not choose a successful creation status for you; your handler must return it. Keep [input validation](../http-fundamentals/requests.md#validate-input) in the production request path so malformed and rejected input exercise the same behavior as real clients.

## Assert Errors

For an endpoint that returns Problem Details, check both the HTTP status and the document. After recording a rejected request and registering body cleanup, import `arkoren.dev/foundation/problem` and decode:

```go
var details problem.Details
if err := json.UnmarshalRead(res.Body, &details); err != nil {
	t.Fatal(err)
}
if res.StatusCode != http.StatusUnprocessableEntity || details.Status() != res.StatusCode {
	t.Fatalf("HTTP status = %d, problem status = %d; want 422", res.StatusCode, details.Status())
}
```

This fragment assumes your endpoint promises HTTP 422 for that input. For validation problems, inspect `details.Extension("errors")` and assert the particular field path that should fail. Do not accept any non-200 response as proof that validation worked: an unrelated internal error could satisfy that assertion. [Error Handling](../http-fundamentals/error-handling.md) explains which failures the router renders and which your application must map.

## Cover Middleware

Keep middleware in the router used by a route test. Assert its visible result, such as a required response header, rejected origin, or denied anonymous request. Registering a trivial handler is useful for testing one middleware in isolation; it does not prove that middleware is installed on the production route.

To test a handler without route matching, use `router.Handler(handler).Record(incoming)`. Returned errors still go through the handler lifecycle. Path values are supplied by route matching, so prefer the router when the handler reads them. [Authentication Tests](auth-tests.md) distinguishes handler identity fixtures from access-enforcement tests.

## Continue Flows

For a cookie-based flow, keep the response cookies and attach them to the next request. With a successful login response named `login`, and after registering cleanup for its body:

```go
incoming := httptest.NewRequest(http.MethodGet, "https://app.example/me", nil)
for _, cookie := range login.Cookies() {
	incoming.AddCookie(cookie)
}
res := routes.Record(incoming)
```

This explicitly carries cookies but does not emulate a browser's cookie policy. Test login, protected access, logout, and subsequent rejection with the real [session middleware](../http-capabilities/sessions.md#enable-sessions). Check session mutation errors and [save failures](../http-capabilities/sessions.md#save-changes); a successful handler alone does not establish session persistence.

If the test needs automatic cookie scope, redirects, or a real HTTP transport, use `httptest.NewServer` with an `http.Client` and cookie jar instead. Prefer `httptest.NewTLSServer` when retaining secure-cookie defaults. Plain HTTP requires test-only `session.Builder.AllowInsecure(true)` configuration; never carry that setting into production. Register server cleanup and close every client response body.

An in-memory recorder collects output rather than observing its arrival over time. Use a networked test for transport cancellation or incremental delivery, following [Streaming Responses](../http-fundamentals/streaming-responses.md).

For recording and handler behavior, see the [`router` API reference](https://pkg.go.dev/arkoren.dev/core/http/router).
