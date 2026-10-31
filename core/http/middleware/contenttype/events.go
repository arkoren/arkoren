package contenttype

import "arkoren.dev/core/event"

// RejectedEvent is the payload of [Rejected]. It carries no body, other
// headers, or media type parameters. Paths and media types remain client-chosen;
// review them before forwarding notifications to external systems.
//
// Example:
//
//	notification := contenttype.RejectedEvent{
//		Method:    http.MethodPost,
//		Path:      "/upload",
//		MediaType: "text/plain",
//	}
//	fmt.Println(notification.MediaType)
type RejectedEvent struct {
	// Method is the rejected request's method, without normalization.
	//
	// Example:
	//
	//	notification := contenttype.RejectedEvent{Method: http.MethodPost}
	//	fmt.Println(notification.Method)
	Method string

	// Path is the request path without its query. It can still contain personal
	// identifiers and is not a route template.
	//
	// Example:
	//
	//	notification := contenttype.RejectedEvent{Path: "/upload"}
	//	fmt.Println(notification.Path)
	Path string

	// MediaType is the lower-case media type the client sent, without
	// parameters such as charset. It is empty when the header is missing or its
	// base media type cannot be parsed; malformed parameters can still leave a
	// parsed base type. The client chooses it, so treat it as untrusted input.
	//
	// Example:
	//
	//	notification := contenttype.RejectedEvent{MediaType: "text/plain"}
	//	fmt.Println(notification.MediaType)
	MediaType string
}

// Rejected is published when [Middleware], built with [Builder.Events], rejects
// a request because its Content-Type is missing or not allowed. The default
// response is 415; changing [Builder.ErrorResponse] does not suppress the event.
//
// Every rejection publishes: there is no deduplication, so a client controls
// the rate. Listeners must be cheap, and a rate limiter such as the
// ratelimit middleware should sit in front of [Middleware].
//
// Example:
//
//	events := event.New()
//	unsubscribe, err := events.Listen(contenttype.Rejected,
//		func(_ context.Context, notification contenttype.RejectedEvent) error {
//			fmt.Println(notification.Method, notification.MediaType)
//
//			return nil
//		})
//	if err != nil {
//		return err
//	}
//	defer unsubscribe()
var Rejected = event.NewTopic[RejectedEvent]("http.contenttype.rejected")
