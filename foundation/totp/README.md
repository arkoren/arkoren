# One-Time Passwords

`arkoren.dev/foundation/totp` generates and verifies time-based one-time
passwords (RFC 6238) using only the standard library. It stores nothing and has
no clock: every call takes the time as an argument, so tests control time by
passing a fixed value.

Install with `go get arkoren.dev/foundation/totp`. Build a scheme once, then
enroll and verify:

```go
import "arkoren.dev/foundation/totp"

scheme := totp.New() // SHA1, 6 digits, 30 seconds, one step of drift each side

secret, err := scheme.NewSecret()
if err != nil {
	return err
}
uri, err := scheme.URI("Example", "alice@example.com", secret)
if err != nil {
	return err
}
// Show uri as a QR code and store secret encrypted.

step, err := scheme.Verify(secret, submitted, time.Now())
if errors.Is(err, totp.ErrInvalidCode) {
	// Wrong or expired code.
}
```

Defaults match what authenticator apps support. SHA256 and SHA512, 7 or 8
digits and other periods are available through `New` callbacks, but most apps
honor only the SHA1, 6-digit, 30-second profile; check your target apps before
changing it.

`Verify` accepts the current step and at most one step on each side
(`Window(0)` accepts only the current step; larger windows are rejected). It
returns the matched step. Because the package keeps no state, replay protection
is yours: persist the highest accepted step per user and reject any code whose
step is not greater. Also rate-limit attempts; a six-digit code is guessable
without limits.

The secret is the credential. Store it encrypted, show the provisioning URI only
to the enrolling user, and never log it.

See the [API reference](https://pkg.go.dev/arkoren.dev/foundation/totp).
