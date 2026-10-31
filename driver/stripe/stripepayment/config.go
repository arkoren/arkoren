package stripepayment

import (
	"net/http"
	"time"
)

// Builder configures the HTTP client used by New. The draft is copied before
// publication; the supplied client and transport remain caller-owned.
//
// Example:
//
//	backend, err := stripepayment.New("sk_test_example", func(connection *stripepayment.Builder) {
//		connection.HTTPClient(&http.Client{Timeout: 10 * time.Second})
//	})
//	if err != nil { return err }
//	_ = backend.Client()
type Builder struct {
	// client is borrowed and shallow-copied when constructing the SDK backend.
	client *http.Client
}

// BuilderFunc edits one fresh construction draft synchronously in argument
// order, after New validates the required API key. Invalid keys skip every
// callback. With a valid key, nil callbacks and application panics propagate;
// an empty or expanded nil callback slice keeps the finite default timeout.
//
// Example:
//
//	var configure stripepayment.BuilderFunc = func(connection *stripepayment.Builder) {
//		connection.HTTPClient(&http.Client{Timeout: time.Second})
//	}
//	_, err := stripepayment.New("sk_test_example", configure)
//	if err != nil { return err }
type BuilderFunc = func(*Builder)

// defaultTimeout bounds requests even when an injected client has no timeout.
const defaultTimeout = 30 * time.Second

// HTTPClient borrows client without modifying it. Nil restores defaults; a zero
// timeout becomes 30 seconds on the driver's copy. Negative timeouts are invalid.
// The copy refuses redirects regardless of the supplied CheckRedirect policy.
// It suppresses standard HTTP mutation replays without modifying client or its
// transport. Custom transports must not implement their own mutation retry loop.
//
// Example:
//
//	configure := func(connection *stripepayment.Builder) {
//		connection.HTTPClient(&http.Client{Timeout: 5 * time.Second})
//	}
//	_, err := stripepayment.New("sk_test_example", configure)
//	if err != nil { return err }
func (connection *Builder) HTTPClient(client *http.Client) *Builder {
	connection.client = client

	return connection
}
