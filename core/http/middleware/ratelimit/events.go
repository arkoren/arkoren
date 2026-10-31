package ratelimit

import (
	"time"

	"arkoren.dev/core/event"
)

// ExceededEvent describes a denied request and the normalized policy that denied
// it. It carries request metadata rather than the full internal counter key.
// Key and Path may identify a client or resource; subscribers own their privacy
// and retention policy. Exceeded's best-effort deduplication can deliver the same
// key/window observation more than once.
//
// Example:
//
//	exceeded := ratelimit.ExceededEvent{
//		Key: "account:123", Method: http.MethodGet, Path: "/search",
//		Limit: 60, Window: time.Minute,
//	}
//	fmt.Println(exceeded.Method, exceeded.Path, exceeded.Limit, exceeded.Window)
type ExceededEvent struct {
	// Key is the client part of the counter key, the client IP unless
	// Builder.KeyFunc says otherwise. It excludes the namespace and fixed-window
	// suffix. Avoid treating this potentially identifying value as public data.
	//
	// Example:
	//
	//	exceeded := ratelimit.ExceededEvent{Key: "account:123"}
	//	fmt.Println(exceeded.Key == "account:123")
	Key string

	// Method is the denied request's method, copied without normalization.
	// It describes the observation rather than the client's identity.
	//
	// Example:
	//
	//	exceeded := ratelimit.ExceededEvent{Method: http.MethodGet}
	//	fmt.Println(exceeded.Method)
	Method string

	// Path is the decoded URL path of the denied request, without its query.
	// Paths can still contain sensitive identifiers; subscribers own redaction.
	//
	// Example:
	//
	//	exceeded := ratelimit.ExceededEvent{Path: "/search"}
	//	fmt.Println(exceeded.Path)
	Path string

	// Limit is the positive configured request count after zero/default
	// normalization, not the number of denied requests or a raw cache counter.
	//
	// Example:
	//
	//	exceeded := ratelimit.ExceededEvent{Limit: 60}
	//	fmt.Println(exceeded.Limit)
	Limit int

	// Window is the positive sliding-window duration after default normalization.
	// Counter retention spans twice this interval; the event reports one window.
	//
	// Example:
	//
	//	exceeded := ratelimit.ExceededEvent{Window: time.Minute}
	//	fmt.Println(exceeded.Window.Seconds())
	Window time.Duration
}

// Exceeded reports denied requests when Builder.Events supplies a bus. A cache
// marker normally limits publication to the first denial per key and fixed
// window. Deduplication is best-effort: if adding that marker fails, the limiter
// still publishes, so later denials can publish duplicates. This topic is not a
// durable or exactly-once delivery guarantee; listeners must tolerate duplicates.
// A marker failure does not admit a denied request or replace its rate-limit error.
//
// Example:
//
//	bus := event.New()
//	unsubscribe, err := bus.Listen(ratelimit.Exceeded,
//		func(ctx context.Context, exceeded ratelimit.ExceededEvent) error {
//			fmt.Println(exceeded.Limit)
//			return nil
//		})
//	if err != nil {
//		return err
//	}
//	defer unsubscribe()
//	routes.Use(ratelimit.Middleware(store, func(limiter *ratelimit.Builder) {
//		limiter.Events(bus)
//	}))
var Exceeded = event.NewTopic[ExceededEvent]("http.ratelimit.exceeded")
