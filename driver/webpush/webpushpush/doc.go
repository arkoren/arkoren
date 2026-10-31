// Package webpushpush sends encrypted browser notifications using Web Push,
// RFC 8291 encryption and RFC 8292 VAPID authentication. It implements
// arkoren.dev/core/service/push.Driver without lifecycle I/O or retries.
//
// Construct a driver with the application's persistent key.Signing identity and
// contact URI, then pass it to push.New:
//
//	identity, err := key.LoadSigning("vapid-private.pem")
//	if err != nil {
//		return err
//	}
//	driver, err := webpushpush.New(identity, "mailto:push@example.com")
//	if err != nil {
//		return err
//	}
//	_ = push.New(driver)
//
// The plaintext is flat JSON: {"title":"...","body":"...","data":{...}}.
// Empty body and data are omitted. Encoded plaintext may contain at most
// 3993 bytes, including JSON syntax and escaping, for a 4096-byte encrypted
// record. TTL is rounded down to whole seconds; zero explicitly forbids
// storage. Only HTTP 201 confirms acceptance, not display or delivery.
// Receipt IDs are empty: capability URLs in provider headers are neither
// returned nor followed. Unexpected responses never trigger a retry.
//
// Defaults permit Google's fcm.googleapis.com, Mozilla's
// updates.push.services.mozilla.com, and subdomains of push.apple.com.
// AllowedOrigins replaces these defaults with exact HTTPS origins. All
// connections still require public DNS addresses, verified TLS and port 443.
// Environment proxies, redirects, connection reuse and transparent retries
// are disabled. An operation has a finite ten-second default timeout.
//
// VAPID authorizations are reused per origin until five minutes before their
// twelve-hour expiration, including concurrent sends. At most 256 origins
// with unexpired authorizations are retained; additional origins fail closed.
// Reuse a long-lived driver rather than constructing one for each send.
// Authentication state is not shared across processes or driver instances.
// Keep constructor pointers instead of copying synchronized drivers. Formatting
// the pointer emits a fixed redaction marker for owned credentials without
// traversing caller objects. Error text is bounded, but explicitly unwrapped
// native causes retain identity and may contain confidential transport data.
// Applications own subscription authorization, persistence, service workers,
// visible notification display and reconciliation of uncertain outcomes.
package webpushpush
