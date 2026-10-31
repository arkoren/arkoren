// Package server serves any [http.Handler] over HTTP or HTTPS with secure default
// timeouts, startup-time port binding, and graceful shutdown.
//
// [New] creates a [Server] without binding. [Server.Start] binds the
// listener so an unavailable address fails application startup,
// [Server.Serve] serves until its context ends and then drains in-flight
// requests, and [Server.Stop] releases the server. The package uses net/http
// interfaces, so it serves any handler.
//
// Configure without opening a listener, then register its three lifecycle
// methods on an existing application composer:
//
//	srv := server.New(http.NotFoundHandler(), func(listener *server.Builder) {
//		listener.Addr("127.0.0.1:8080")
//	})
//	compose.Cleanup(srv.Stop)
//	compose.Startup(srv.Start)
//	compose.Run(srv.Serve)
//
// Plaintext HTTP is the default. [Builder.TLS] selects HTTPS with a shared native
// TLS configuration. Load certificates before constructing the server; Start
// checks missing or malformed static credentials before binding. Certificate
// callbacks run during handshakes, not construction or startup. HTTPS retains
// native HTTP/2 negotiation. Configure native values before Start and leave them
// unchanged afterward, except through the callbacks supported by crypto/tls.
//
// Beginning shutdown does not cancel active request contexts. Long-lived
// handlers watch [ShuttingDown] and cooperate with shutdown. A drain timeout
// closes tracked HTTP connections; it cannot forcibly stop application work.
// Hijacked connections such as WebSockets are neither closed nor awaited:
// their owners must close them and join associated work before releasing
// dependencies. [Server.Stop] always uses its caller's context as its budget.
//
// With [Builder.Events], [Server.Start] publishes [Listening]
// ("http.server.listening") with the bound address. [Server.Serve] publishes
// [ShutdownTimedOut] ("http.server.shutdown.timed-out") with the address and
// the shutdown timeout when tracked HTTP connections did not drain in time and
// were force-closed. A clean shutdown publishes nothing;
// serving and shutdown are otherwise reported by the application's lifecycle
// events when the server runs under an application.
package server
