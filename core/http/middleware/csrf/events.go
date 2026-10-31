package csrf

import "arkoren.dev/core/event"

// BlockedEvent reports a request rejected by the configured native cross-origin
// policy. Paths, Origin, and the native error can contain untrusted or sensitive
// data; review them before forwarding a notification outside the process.
//
// Example:
//
//	notification := csrf.BlockedEvent{
//		Method: http.MethodPost,
//		Path:   "/settings",
//		Origin: "https://other.example",
//	}
//	fmt.Println(notification.Method, notification.Path)
type BlockedEvent struct {
	// Method is the blocked request's method, without normalization.
	//
	// Example:
	//
	//	notification := csrf.BlockedEvent{Method: http.MethodPost}
	//	fmt.Println(notification.Method)
	Method string

	// Path is the request path without its query, not a route template. It can
	// still contain personal identifiers.
	//
	// Example:
	//
	//	notification := csrf.BlockedEvent{Path: "/settings"}
	//	fmt.Println(notification.Path)
	Path string

	// Origin is the untrusted Origin header; empty when the request had none.
	// It is reported for diagnosis, not as an authenticated identity.
	//
	// Example:
	//
	//	notification := csrf.BlockedEvent{Origin: "https://other.example"}
	//	fmt.Println(notification.Origin)
	Origin string

	// Err is the reason [net/http.CrossOriginProtection] gave. Its text is native
	// diagnostic information, not an application-facing response body.
	//
	// Example:
	//
	//	notification := csrf.BlockedEvent{Err: errors.New("cross-origin request rejected")}
	//	fmt.Println(notification.Err != nil)
	Err error
}

// Blocked is published when [Middleware] rejects a cross-origin request.
// Supply its bus through [Builder.Events]; omitted or nil buses publish nothing.
// Listeners run synchronously and cannot change the rejection outcome.
//
// Example:
//
//	events := event.New()
//	unsubscribe, err := events.Listen(csrf.Blocked,
//		func(_ context.Context, notification csrf.BlockedEvent) error {
//			fmt.Println(notification.Method, notification.Path)
//
//			return nil
//		})
//	if err != nil {
//		return err
//	}
//	defer unsubscribe()
var Blocked = event.NewTopic[BlockedEvent]("http.csrf.blocked")
