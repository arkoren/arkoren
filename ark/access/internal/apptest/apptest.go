// Package apptest is the HTTP harness of the tests of arkoren.dev/ark/access:
// a router with session and auth middleware behind an in-process server and a
// client that keeps cookies, like a browser. It imports core only (no driver),
// and only test files import it.
package apptest

import (
	"context"
	"encoding/json/v2"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"strings"
	"testing"

	"arkoren.dev/core/auth"
	"arkoren.dev/core/http/router"
	"arkoren.dev/core/http/session"
	"arkoren.dev/core/service/cache"
)

// Client is a cookie-keeping HTTP client of one test server.
type Client struct {
	t      *testing.T
	server *httptest.Server
	http   *http.Client
}

// Reply is one completed response with its body read.
type Reply struct {
	// Status is the HTTP status code.
	Status int

	// Body is the response body.
	Body string

	// Header holds the response headers.
	Header http.Header
}

// JSON decodes the body into a T and fails the test when it is not JSON.
func JSON[T any](t *testing.T, reply Reply) T {
	t.Helper()

	var value T
	if err := json.Unmarshal([]byte(reply.Body), &value); err != nil {
		t.Fatalf("body %q is not the expected JSON: %v", reply.Body, err)
	}

	return value
}

// Serve starts a server whose router runs the session middleware over store and
// the auth middleware over authenticators, then calls mount to register the
// routes. The server stops with the test.
func Serve(t *testing.T, store *cache.Cache, authenticators []auth.Authenticator, mount func(r *router.Router)) *Client {
	t.Helper()

	r := router.New()
	r.Use(session.Middleware(store, func(policy *session.Builder) {
		policy.AllowInsecure(true)
	}), auth.Middleware(authenticators...))
	mount(r)

	return ServeRouter(t, r)
}

// ServeRouter starts a server for r, which must already run its own session
// middleware, and returns a cookie-keeping client. The server stops with the
// test.
func ServeRouter(t *testing.T, r *router.Router) *Client {
	t.Helper()

	server := httptest.NewServer(r)
	t.Cleanup(server.Close)
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("cookie jar: %v", err)
	}

	// The server's own transport, not http.DefaultTransport: closing another
	// parallel test's server closes the default one's idle connections and breaks
	// requests in flight here.
	return &Client{t: t, server: server, http: &http.Client{
		Jar:           jar,
		Transport:     server.Client().Transport,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
}

// NewDevice returns a client of the same server with its own, empty cookie jar:
// another browser or phone.
func (client *Client) NewDevice() *Client {
	client.t.Helper()

	jar, err := cookiejar.New(nil)
	if err != nil {
		client.t.Fatalf("cookie jar: %v", err)
	}

	return &Client{t: client.t, server: client.server, http: &http.Client{
		Jar:           jar,
		Transport:     client.http.Transport,
		CheckRedirect: client.http.CheckRedirect,
	}}
}

// Do sends method path with body (empty for none) and optional "Name: value"
// headers, and returns the reply. POST, PUT and PATCH carry Content-Type
// application/json unless a header sets another value; a header with an empty
// value ("Content-Type: ") removes it. Redirects are not followed.
func (client *Client) Do(method, path, body string, headers ...string) Reply {
	client.t.Helper()

	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	request, err := http.NewRequestWithContext(context.Background(), method, client.server.URL+path, reader)
	if err != nil {
		client.t.Fatalf("request: %v", err)
	}
	if method == http.MethodPost || method == http.MethodPut || method == http.MethodPatch {
		request.Header.Set("Content-Type", "application/json")
	}
	for _, header := range headers {
		name, value, _ := strings.Cut(header, ": ")
		if value == "" {
			request.Header.Del(name)

			continue
		}
		request.Header.Set(name, value)
	}
	response, err := client.http.Do(request)
	if err != nil {
		client.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(response.Body)
	if err != nil {
		client.t.Fatalf("read body: %v", err)
	}

	return Reply{Status: response.StatusCode, Body: string(data), Header: response.Header}
}

// Post sends a POST with a JSON body.
func (client *Client) Post(path, body string) Reply {
	client.t.Helper()

	return client.Do(http.MethodPost, path, body)
}
