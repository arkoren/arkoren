---
title: HTTP Tests
description: Test routes, handlers, and middleware by sending requests through the router without a server, and check statuses, headers, and JSON.
order: 20
---

# HTTP Tests

## Introduction

Most HTTP tests do not need a running server. An Arkoren router can handle a
request directly and return the response, with every middleware applied, in
the same process without binding a port. This makes it practical to test each
route, including its error cases, as an ordinary unit test.

The fragments run inside your own `func Test...(t *testing.T)` functions.
Import `net/http`, `net/http/httptest`, and `arkoren.dev/core/http/router` for
request recording. Later examples add `strings`, `encoding/json/v2`,
`arkoren.dev/foundation/problem`, and
`arkoren.dev/core/http/middleware/secureheaders` where used. The post examples
assume your router has `POST /posts`, returns the created post as JSON, and
validates a required `title` field.

## Sending a request

Build the router the same way your service does, and seed an invoice with ID 1.
With that router named `r`, call `Record` with a request created by
`httptest.NewRequest`:

```go
res := r.Record(httptest.NewRequest(http.MethodGet, "/invoices/1", nil))
if res.StatusCode != http.StatusOK {
	t.Fatalf("status = %d, want 200", res.StatusCode)
}
```

`Record` returns an `*http.Response`, so you check it with the same fields as a
response from a real server: `StatusCode`, `Header`, and `Body`.
Close the returned body when you finish reading it, including responses from
`Record`. A test cleanup callback can close it after your assertions.

Building routes through your feature's route-registration function, as described in
[Project Layout](../first-steps/project-layout.md), pays off here:
the test mounts exactly the feature it tests.

## Sending a JSON body

Pass the body as a reader and set the content type, as a client would:

```go
body := strings.NewReader(`{"title": "First post"}`)
req := httptest.NewRequest(http.MethodPost, "/posts", body)
req.Header.Set("Content-Type", "application/json")

res := r.Record(req)
if res.StatusCode != http.StatusCreated {
	t.Fatalf("status = %d, want 201", res.StatusCode)
}
```

This assumes your create handler returns HTTP 201. `Record` does not choose
that status for you; use the status your endpoint promises.

## Reading a JSON response

Decode the body into your application's response type, `Post` below, which has
a string field named `Title`. Use
`encoding/json/v2`, as Arkoren does:

```go
var post Post
if err := json.UnmarshalRead(res.Body, &post); err != nil {
	t.Fatal(err)
}
if post.Title != "First post" {
	t.Fatalf("title = %q, want First post", post.Title)
}
```

## Testing errors

Error responses are Problem Details documents, so a test can decode them into
`problem.Details` and check the fields that matter, such as the status and, for
validation errors, which fields failed:

```go
req := httptest.NewRequest(http.MethodPost, "/posts", strings.NewReader(`{"title": ""}`))
req.Header.Set("Content-Type", "application/json")
res := r.Record(req)

var got problem.Details
if err := json.UnmarshalRead(res.Body, &got); err != nil {
	t.Fatal(err)
}

if got.Status() != http.StatusUnprocessableEntity {
	t.Fatalf("status = %d, want 422", got.Status())
}
fields, found := got.Extension("errors")
if !found {
	t.Fatal("missing validation errors extension")
}
violations, ok := fields.(map[string]any)
if !ok {
	t.Fatalf("errors has type %T, want an object", fields)
}
if _, ok := violations["/title"]; !ok {
	t.Fatalf("no error for /title: %v", fields)
}
```

Extension members, such as the `errors` of a validation problem, are in the
`Extension(name)` lookup; `Extensions()` returns an independent outer map.

## Testing middleware

Because `Record` runs the whole router, middleware is tested the same way:
register it, add a trivial route, and check its effect on the response:

```go
r := router.New()
r.Use(secureheaders.Middleware())
r.Get("/", func(w http.ResponseWriter, r *http.Request) error {
	return nil
})

res := r.Record(httptest.NewRequest(http.MethodGet, "/", nil))
if got := res.Header.Get("X-Frame-Options"); got != "DENY" {
	t.Fatalf("X-Frame-Options = %q", got)
}
```

## Testing a single handler

A handler is a function, so it can also be tested on its own, without a router.
Wrap it in `router.Handler` so returned errors are rendered as in production:

```go
res := router.Handler(h.show).Record(httptest.NewRequest(http.MethodGet, "/", nil))
```

Path parameters are only set by the router's patterns, so testing through the
router is simpler for handlers that read them.

## Requests across several calls

Some flows span requests, such as logging in and then visiting a page, where
the second request needs the session cookie from the first. For these, start
a real test server and use a client with a cookie jar. Use a router `r` with
your real login and `/me` routes and their middleware. Here `credentials` is
your form data (`url.Values`) with a valid login. Import `net/http/cookiejar`
for the client. This example assumes login ends in HTTP 200, including after
any redirect that the client follows.

`httptest.NewServer` serves plain HTTP. Before building `r`, configure its
session middleware with a callback that calls `cookies.AllowInsecure(true)`,
where `cookies` is a `*session.Builder`. Enable this only in the test setup.

Submit the login, then use the same client for the protected request. The jar
keeps the session cookie from the login response:

```go
srv := httptest.NewServer(r)
t.Cleanup(srv.Close)

jar, err := cookiejar.New(nil)
if err != nil {
	t.Fatal(err)
}
client := &http.Client{Jar: jar}

login, err := client.PostForm(srv.URL+"/login", credentials)
if err != nil {
	t.Fatal(err)
}
login.Body.Close()
if login.StatusCode != http.StatusOK {
	t.Fatalf("login status = %d, want 200", login.StatusCode)
}
res, err := client.Get(srv.URL + "/me")
if err != nil {
	t.Fatal(err)
}
t.Cleanup(func() {
	res.Body.Close()
})
if res.StatusCode != http.StatusOK {
	t.Fatalf("status = %d, want 200", res.StatusCode)
}
```

[Auth tests](auth-tests.md) shows simpler ways to test routes that need a user.

If your own login flow requires an earlier request to establish state, make that
request with the same client first. This example checks session continuity, not
browser-origin enforcement; test [CSRF protection](../browser-security/csrf-protection.md)
with the relevant browser headers separately.

For more on the testing helpers, see the
[testing API reference](overview.md).
