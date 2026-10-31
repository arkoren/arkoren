package client

import "arkoren.dev/core/event"

// RequestFailedEvent describes the final outcome reported by RequestFailed, not
// every retry attempt or a later response-body read error. Exactly one of Err
// and a nonzero StatusCode describes the outcome. Request cancellation suppresses
// publication; per-attempt timeout errors can still be reported while the parent
// request remains active. Applications own redaction of any native error text.
//
// Example:
//
//	failed := client.RequestFailedEvent{
//		Method: http.MethodGet, Host: "api.example.com", Attempts: 3,
//		StatusCode: http.StatusServiceUnavailable,
//	}
//	fmt.Println(failed.Method, failed.Host, failed.Attempts, failed.StatusCode)
type RequestFailedEvent struct {
	// Method is the caller's request method, copied without normalization. Use it
	// to classify an observed operation, not as proof that replay was safe.
	//
	// Example:
	//
	//	failed := client.RequestFailedEvent{Method: http.MethodGet}
	//	fmt.Println(failed.Method)
	Method string

	// Host is the destination hostname from the request URL, without a port,
	// path, or query. It is request metadata, not a resolved network address.
	//
	// Example:
	//
	//	failed := client.RequestFailedEvent{Host: "api.example.com"}
	//	fmt.Println(failed.Host)
	Host string

	// Attempts is the positive number of attempts made for this round trip,
	// including the initial attempt. Redirects have their own round trips.
	//
	// Example:
	//
	//	failed := client.RequestFailedEvent{Attempts: 3}
	//	fmt.Println(failed.Attempts)
	Attempts int

	// StatusCode is the returned response status, or zero when Err is nonnil.
	// A response reports the RetryIf predicate's final classification; arbitrary
	// 4xx or 5xx statuses are not automatically failed events.
	//
	// Example:
	//
	//	failed := client.RequestFailedEvent{StatusCode: http.StatusServiceUnavailable}
	//	fmt.Println(failed.StatusCode)
	StatusCode int

	// Err is the returned native transport error; it is nil with a response.
	// Inspect its cause with errors.Is/errors.As before choosing recovery. Native
	// error strings can contain sensitive details and are not sanitized here.
	//
	// Example:
	//
	//	failed := client.RequestFailedEvent{Err: io.ErrUnexpectedEOF}
	//	if errors.Is(failed.Err, io.ErrUnexpectedEOF) {
	//		fmt.Println("incomplete transport response")
	//	}
	Err error
}

// RequestFailed is published when [Transport] gives up on a request: it
// returns a transport error, or a response that [Builder.RetryIf] would have
// retried once attempts ran out or Retry-After asked for too long.
// Publication requires Builder.Events and is suppressed when the request context
// is done. This reports a final transport observation, not durable delivery or a
// later body-read failure.
//
// Example:
//
//	bus := event.New()
//	unsubscribe, err := bus.Listen(client.RequestFailed,
//		func(ctx context.Context, failed client.RequestFailedEvent) error {
//			fmt.Println(failed.Attempts, failed.StatusCode)
//			return nil
//		})
//	if err != nil {
//		return err
//	}
//	defer unsubscribe()
//	outgoing := &http.Client{Transport: client.Transport(nil, func(transport *client.Builder) {
//		transport.Events(bus)
//	})}
//	_ = outgoing
var RequestFailed = event.NewTopic[RequestFailedEvent]("http.client.request.failed")
