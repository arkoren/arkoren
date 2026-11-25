---
title: Verification & Recovery
nav: Verification & Recovery
description: Send signed, single-use links through your queue and mailer so users can reset forgotten passwords and verify email addresses safely.
order: 30
summary:
  - Create a persistent link signer and wire IAM's queue, mailer, and front-end page URLs.
  - Register IAM's background email jobs in your worker and customize the default message wording.
  - Handle password-reset and email-verification links safely in your front end and guard routes with RequireVerified.
---

# Verification & Recovery

Two flows depend on proving that a user controls an email inbox: recovering a forgotten password and verifying an email address before unlocking sensitive credentials. IAM handles both without storing token rows in your database. Instead, it signs short-lived, single-use links bound to the user's current state, enqueues a lightweight job during the HTTP request, and sends the email from your queue worker through your mailer.

This page builds on the facade from [IAM Overview](overview.md#building-the-facade) and assumes you have an existing [queue dispatcher and consumer](../queues/overview.md) and a [mailer](../mail/overview.md).

## Creating a Link Signer

Signed links need a persistent 32-byte shared key. Parse a [provisioned key](../keys/overview.md) into a `key.Ring` with `key` from `arkoren.dev/foundation/key` and pass it to `link.New` from `arkoren.dev/core/http/link`:

```go
text, err := os.ReadFile("link-key.key")
ring, err := key.ParseRing[key.Shared](string(text))
signer, err := link.New(ring)
```

To rotate the key, put the new key first in the ring and keep the old ones after it, comma-separated: `NEW_KEY,OLD_KEY`. The signer signs with the first key and verifies links with every key in the ring, so links issued before the rotation keep working until you remove the old key.

> [!WARNING]
> Do not generate a fresh key at startup. Every application instance and every previously issued link must share the same signing key, or valid links will fail verification.

By default, `link.New` produces a signed relative path and query; an [origin-configured signer](../http-capabilities/secure-links.md) produces an absolute URL. IAM accepts either, and binds each signature to its purpose path so a verification link can never be replayed as a password-reset link.

## Wiring the Queue and Mailer

Give IAM your queue dispatcher with `identity.Queue(dispatcher)`, then enable password reset, email verification, or both. Each flow takes the signer, your `*mail.Mailer`, and the absolute URL of the front-end page that will handle the link (with no `#fragment`):

```go
identity.Queue(dispatcher)
identity.Password(hasher, func(passwords *password.Builder) {
	passwords.Reset(signer, mailer, "https://app.example.com/reset-password")
})
identity.Directory(func(accounts *account.Builder) {
	accounts.Verification(signer, mailer, "https://app.example.com/verify-email")
})
```

Without `Reset`, IAM mounts neither `/password/forgot` nor `/password/reset`. Without `Verification`, it mounts neither `/email/verify` nor `/email/verify/resend`. Omitting the signer, mailer, page URL, or `Queue` causes `iam.New` to fail with `iam.ErrInvalidConfig`.

IAM sends short plain-text emails from your mailer's default sender. Delivery always goes to the address stored on the user record, never to raw text copied from a forgot-password request.

## Running the Email Worker

HTTP requests only enqueue jobs; your worker sends the emails. Register IAM's job handlers on your queue consumer:

```go
err = identity.RegisterJobs(consumer)
```

`RegisterJobs` registers handlers for every enabled feature using the facade's `Instance`, so multiple instances can share one consumer. All jobs run on the queue named `iam.QueueName` (`"iam"`). If you restrict your consumer with `ConsumerBuilder.Queues`, include `iam.QueueName` or emails will sit unconsumed.

> [!NOTE]
> With a networked queue driver, deploy the worker running `RegisterJobs` before enabling a new email flow on your web servers. With the in-memory queue, run the consumer in the same process.

When a worker picks up a job, it looks up the user and signs the link just before sending:

- Unknown or disabled users (for reset) and deleted or already verified users (for verification) are silently skipped.
- Transient mailer errors are returned to the queue so your retry policy can try again; malformed addresses fail permanently because retrying cannot fix them.
- Because retries can run more than once, each attempt signs a fresh link; earlier links stay valid until they expire or the bound state changes.

If you prefer orchestrating email delivery inside a [workflow](../workflows/steps.md) rather than a queue, call `identity.Verifier().SendReset` or `identity.Directory().SendVerification` directly from a workflow step.

### Customizing Email Wording

To replace the default plain-text wording, pass an `Email` callback inside `Reset` or `Verification`. It receives the message metadata and the full `href` to place in the email body:

```go
passwords.Reset(signer, mailer, resetPage, func(reset *password.ResetBuilder) {
	reset.Email(func(message password.ResetMessage, href string) mail.Message {
		return mail.NewMessage(
			mail.Recipients{To: []mail.Address{{Email: message.Address}}},
			mail.Body{Text: "Reset your password: " + href},
		)
	})
})
```

Check `message.Notice` on `password.ResetMessage`: when `true`, the email was triggered by a [registration attempt on an existing address](passwords.md#registration-emails), so explain that setting a new password recovers the account if the recipient did not initiate it. Verification uses `accounts.Verification(..., func(verification *account.VerificationBuilder) { verification.Email(...) })` with `account.VerifyMessage` (which has no `Notice` field).

> [!WARNING]
> Treat `href` as a bearer credential. Never log it or send it to analytics or error trackers.

## Resetting a Password

To start recovery, the client sends `POST /password/forgot`:

```json
{
  "email": "ada@example.com"
}
```

IAM enqueues the typed identifier without looking up the user and returns an empty `202` whether or not the account exists. A dispatch failure returns `500` so the client can retry. The route allows 5 requests per typed identifier and 30 per client address in 15 minutes before returning `429`.

IAM places the signed token in the URL **fragment** (`#...`) because browsers never send fragments in HTTP requests or `Referer` headers. When Ada opens the link on your reset page, extract the token and immediately clear the fragment from the address bar:

```js
const token = decodeURIComponent(location.hash.slice(1));
history.replaceState(null, "", location.pathname + location.search);
```

Submit the token verbatim with the new password:

```js
const response = await fetch("/password/reset", {
  method: "POST",
  headers: { "Content-Type": "application/json" },
  body: JSON.stringify({ token, password: newPassword }),
});
```

A `204` response means the password has been reset. A password that fails your policy returns `422` and leaves the link usable; a forged, expired, or already-used link returns `422` `password:invalid-token`. Links expire after one hour by default; change this with `passwords.ResetTTL(d)`.

What a reset guarantees:

- **Single-use without a token table**: The link binds to the user's current password hash and updates it with a conditional write. If two submissions race, only one succeeds, and any password change immediately invalidates all earlier links.
- **Full revocation, no automatic login**: A reset rotates the user's credential version, ending every active session, pending login, and API token. Ada must sign in again (including her second factor if enrolled).
- **Unverified-account cleanup**: If Ada's email was unverified, completing the reset proves she owns the mailbox. IAM marks the email verified and purges any external connections, two-factor enrollment, recovery codes, and tokens that an earlier registrant may have attached. If the email was already verified, existing connections and two-factor stay intact; review them under your [recovery policy](accounts.md#security-events).

## Verifying an Email

An unverified user can sign in, but IAM blocks them from connecting external providers, enrolling two-factor authentication, or creating API tokens until their address is verified.

To send a verification email from your own invitation or onboarding code:

```go
err := identity.Directory().RequestVerification(ctx, subject)
```

Signed-in users can also request a fresh link via `POST /email/verify/resend` (`202`, limited to 5 per user and 20 per client address in 15 minutes).

When Ada opens the verification link, extract and clear the URL fragment the same way as password reset. **Ada must be signed in as the account the link belongs to** before your front end submits it:

```js
const response = await fetch("/email/verify", {
  method: "POST",
  headers: { "Content-Type": "application/json" },
  body: JSON.stringify({ token }),
});
```

Why require a matching session? So an unsolicited click on an email link cannot verify an account whose password is held by someone else. If Ada did not create the account and does not know its password, she should use password recovery instead.

`204` marks the email verified immediately; no re-login is needed. Missing a session returns `401`; being signed in as a different user returns `403` `account:verify-wrong-account`; an expired, forged, or already-used link returns `422` `account:invalid-token`. Verification links expire after 24 hours by default; adjust this with `accounts.VerifyTTL(d)`.

### Requiring Verified Emails on Your Routes

To restrict your own routes to verified users, add `identity.RequireVerified()` after `auth.Require()`:

```go
verified := web.With(auth.Require(), identity.RequireVerified())
verified.Post("/posts", createPost)
```

Unverified users receive `403`, and verification takes effect on their very next request. If you use [your own users](own-users.md#preserving-email-verification-protections), configure `UsersBuilder.Verification` to keep these checks active.

## Standalone Packages

If you construct `password.NewVerifier` or `account.NewDirectory` without the `*iam.IAM` facade, call `Queue(dispatcher)` directly on the package builder next to `Reset` or `Verification`, and register the jobs with `verifier.RegisterJobs(consumer)` or `directory.RegisterJobs(consumer)`.

Next, read [Step-Up & Freshness](freshness.md) to understand how IAM protects credential changes and sensitive application routes.

For the underlying contracts, see the [`password` API](https://pkg.go.dev/arkoren.dev/ark/iam/password) and [`account` API](https://pkg.go.dev/arkoren.dev/ark/iam/account).
