package server

import (
	"time"

	"arkoren.dev/core/event"
)

// ListeningEvent reports the TCP address acquired by [Server.Start]. Binding
// does not mean Serve has begun accepting requests or a TLS handshake succeeded.
//
// Example:
//
//	notification := server.ListeningEvent{Addr: "127.0.0.1:8080"}
//	fmt.Println(notification.Addr)
type ListeningEvent struct {
	// Addr is the bound address, with the chosen port when the configured
	// port is zero. It is a listener address, not a public URL.
	//
	// Example:
	//
	//	notification := server.ListeningEvent{Addr: "127.0.0.1:8080"}
	//	fmt.Println(notification.Addr)
	Addr string
}

// Listening is published when [Server.Start] has bound the listener.
// Subscribe on the bus supplied through [Builder.Events]; callbacks run
// synchronously and should not perform slow work.
//
// Example:
//
//	events := event.New()
//	unsubscribe, err := events.Listen(server.Listening,
//		func(_ context.Context, notification server.ListeningEvent) error {
//			fmt.Println(notification.Addr)
//
//			return nil
//		})
//	if err != nil {
//		return err
//	}
//	defer unsubscribe()
var Listening = event.NewTopic[ListeningEvent]("http.server.listening")

// ShutdownTimedOutEvent describes the native HTTP drain deadline exceeded by
// [Server.Serve]. It does not assert that every handler goroutine has stopped;
// hijacked connections are outside native HTTP draining.
//
// Example:
//
//	notification := server.ShutdownTimedOutEvent{
//		Addr:    "127.0.0.1:8080",
//		Timeout: 15 * time.Second,
//	}
//	fmt.Println(notification.Timeout)
type ShutdownTimedOutEvent struct {
	// Addr is the bound listener address, not an inferred public URL.
	//
	// Example:
	//
	//	notification := server.ShutdownTimedOutEvent{Addr: "127.0.0.1:8080"}
	//	fmt.Println(notification.Addr)
	Addr string

	// Timeout is the configured drain budget, not the total elapsed Serve time.
	//
	// Example:
	//
	//	notification := server.ShutdownTimedOutEvent{Timeout: 15 * time.Second}
	//	fmt.Println(notification.Timeout)
	Timeout time.Duration
}

// ShutdownTimedOut is published when [Server.Serve] stops serving and
// tracked HTTP connections have not drained before the shutdown deadline, so
// those connections are force-closed. Hijacked connections remain caller-owned.
// A shutdown that drains in time, or one that
// fails for another reason, publishes nothing. [Server.Stop] publishes
// nothing, because its deadline belongs to the caller.
//
// Example:
//
//	events := event.New()
//	unsubscribe, err := events.Listen(server.ShutdownTimedOut,
//		func(_ context.Context, notification server.ShutdownTimedOutEvent) error {
//			fmt.Println(notification.Addr, notification.Timeout)
//
//			return nil
//		})
//	if err != nil {
//		return err
//	}
//	defer unsubscribe()
var ShutdownTimedOut = event.NewTopic[ShutdownTimedOutEvent]("http.server.shutdown.timed-out")
