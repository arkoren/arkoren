// Package cloudflarecaptcha verifies Cloudflare Turnstile tokens and optionally
// protects router handlers. It is vendor-local: no core CAPTCHA contract exists.
//
// Construct one concurrent-safe Verifier with a server secret and trusted
// hostname allowlist. Verify returns evidence only after provider success and
// hostname/action checks. It does not authenticate users or replace CSRF checks.
//
// Example:
//
//	verifier, err := cloudflarecaptcha.New("server-secret", []string{"example.com"})
//	if err != nil {
//		return err
//	}
//	_, err = verifier.Verify(ctx, cloudflarecaptcha.Input{Token: token, ExpectedAction: "signup"})
//	return err
//
// New does no I/O. Clients and transports are borrowed, never closed. Each
// verification has a ten-second default deadline, never follows redirects, and
// makes one HTTP attempt. Custom transports must honor request cancellation and
// must not implement retries. Turnstile tokens expire after five minutes and
// are single-use. Although Siteverify supports idempotency keys, this package
// does not send one or retry; obtain a fresh challenge after an uncertain result.
// Formatting a Verifier value or pointer emits a fixed redaction marker for its
// owned secret without traversing the borrowed transport or arbitrary caller
// objects. Native error causes remain inspectable, not safe to log indiscriminately.
//
// For JSON requests, decode once in the application handler and call Verify
// before effects. Middleware requires explicit extraction, does not read the
// body itself, and never trusts proxy headers or forwards a remote IP.
package cloudflarecaptcha
