---
title: Email Links
description: Wire durable email delivery, then let users reset passwords and verify addresses through signed, single-use links.
order: 30
summary:
  - Separate request-time dispatch from worker-time lookup, signing, and email delivery.
  - Configure password recovery and email verification with small queue-backed examples.
  - Submit signed links safely and distinguish password recovery from email verification.
---

# Email Links

A forgotten password needs a recovery path, and an email address needs proof
before it can be trusted. IAM supplies signed links for both, but your
application owns the email delivery. Neither feature runs until you provide a
signer and two functions: **dispatch** stores work during the HTTP request;
**deliver** sends the resulting message later.

This page extends the [password-enabled facade](overview.md#start-small).
It uses an existing queue dispatcher and consumer; their connection and worker
lifecycle belong to [Queues](../queues/overview.md). A workflow or outbox can
make the same handoff.

## Prepare a Signer

Use `key` from `arkoren.dev/foundation/key` to load a
[provisioned 32-byte shared key](../keys/overview.md) reserved for signed links.
Do not generate a replacement at each startup:
other instances and previously issued links need the same key.

```go
secret, err := key.LoadShared("link-key.key")
```

After handling that error, construct the signer with `link` from
`arkoren.dev/core/http/link`:

```go
signer, err := link.New(secret)
```

The default signed value is a relative path and query. An
[origin-configured signer](../http-capabilities/secure-links.md) can produce
an absolute value instead; IAM accepts either. The signature binds each
purpose's path, so a verification link cannot be used as a password-reset link.

## Deliver Messages

A delivery callback receives the stored recipient address, signed value, and
expiry time. The signed value is a credential: keep it out of logs, analytics,
and exception reports.

Suppose `sendEmail(ctx, address, subject, text)` is your application's mail
adapter and returns an error. Import `password` from
`arkoren.dev/ark/iam/password` and define the reset delivery:

```go
deliverReset := func(
	ctx context.Context,
	message password.ResetMessage,
) error {
	subject := "Reset your password"
	if message.Notice {
		subject = "Someone tried to register your address"
	}
	href := message.Link("https://app.example.com/reset-password")

	return sendEmail(ctx, message.Address, subject, href)
}
```

`Notice` distinguishes an ordinary reset request from a
[registration attempt using a taken address](registration.md#registration-emails).
In the latter email, explain that setting a new password recovers the account
if the recipient did not create it. `message.Link(base)` puts the signed value
in the URL fragment; `base` must not already have a fragment.

Verification uses `account.VerifyMessage` from
`arkoren.dev/ark/iam/account`, with no `Notice` field:

```go
deliverVerification := func(
	ctx context.Context,
	message account.VerifyMessage,
) error {
	href := message.Link("https://app.example.com/verify-email")

	return sendEmail(ctx, message.Address, "Confirm your email", href)
}
```

These page URLs belong to your front end, not to IAM. Delivery always uses
the address stored with the user, never an address copied from an incoming
reset request. For a concrete mail adapter, see
[composing mail](../mail/composing-messages.md).

## Store the Work

Queue the request value, not a pre-signed link. The worker should look up the
current user and sign just before delivery.
Import `queue` from `arkoren.dev/core/service/queue`:

```go
resetJob := queue.NewJob[password.ResetRequest]("send-reset")
```

With your existing dispatcher, store each reset request:

```go
dispatchReset := func(
	ctx context.Context,
	request password.ResetRequest,
) error {
	return dispatcher.Dispatch(ctx, resetJob, request)
}
```

`ResetRequest.Identifier` is exactly what the user typed. Do not look it up
inside `dispatchReset`: the forgot-password response must not depend on
whether that user exists. The optional `Notice` travels with the work.

Verification jobs carry a subject instead:

```go
verifyJob := queue.NewJob[account.VerifyRequest]("send-verification")
```

Give verification its own dispatch callback:

```go
dispatchVerification := func(
	ctx context.Context,
	request account.VerifyRequest,
) error {
	return dispatcher.Dispatch(ctx, verifyJob, request)
}
```

Both dispatch callbacks must store work durably before returning success.
The queue's durability depends on your
[driver and delivery setup](../queues/drivers.md); launching a goroutine is not
a durable handoff. Stored request values remain decodable across IAM
upgrades: new fields are additive, with zero values for older payloads.

## Enable the Flows

With the signer and four callbacks defined, enable recovery on passwords and
verification on the built-in directory:

```go
identity, err := iam.New(db, store, func(identity *iam.Builder) {
	identity.Password(hasher, func(passwords *password.Builder) {
		passwords.Reset(signer, dispatchReset, deliverReset)
	})
	identity.Directory(func(accounts *account.Builder) {
		accounts.Verification(signer, dispatchVerification, deliverVerification)
	})
})
```

This leaves registration closed. Without `Reset`, IAM mounts neither
`/password/forgot` nor `/password/reset`. Without `Verification`, it mounts
neither email route. A partially configured flow makes construction fail;
requesting one of its unavailable handlers directly panics during composition.

Once `iam` exists, register the worker's reset handler:

```go
err = consumer.Handle(resetJob, identity.SendReset)
```

Register verification separately:

```go
err = consumer.Handle(verifyJob, identity.SendVerification)
```

Construction does not invoke dispatch. Requests enqueue work; the running
consumer later calls these methods. For workflows, call the same methods from
a [workflow step](../workflows/steps.md) and start the run from dispatch.
For synchronous tests only, dispatch can call the matching method directly.

`SendReset` silently skips unknown or disabled users. `SendVerification`
silently skips deleted or already verified users. They return delivery errors
to the worker, whose retry policy decides what happens next.
Delivery can therefore run more than once. Each attempt signs a link; earlier
attempts remain usable until their expiry or the state they bind to changes.
Neither flow needs a database table of email-link tokens.

## Reset a Password

To request recovery, send `POST /password/forgot`:

```json
{
  "email": "ada@example.com"
}
```

The route queues the typed identifier and returns an empty `202` whether or not
Ada exists. There is no user lookup during the request. A dispatch failure
returns `500`; the client can retry. The route accepts 5 requests per typed
identifier and 30 per client address in 15 minutes, then returns `429`.

When Ada opens the email, the reset page reads the signed value from its
fragment. A browser does not send fragments in the HTTP request or `Referer`:

```js
const token = decodeURIComponent(location.hash.slice(1));
history.replaceState(null, "", location.pathname + location.search);
```

Removing the fragment keeps the credential out of the address bar.
Do not send it to third-party scripts. With `newPassword` collected from Ada's
form, submit the value verbatim:

```js
const response = await fetch("/password/reset", {
  method: "POST",
  headers: { "Content-Type": "application/json" },
  body: JSON.stringify({ token, password: newPassword }),
});
```

`204` means the password changed. A policy failure is `422` and leaves the link
usable; a malformed, forged, expired, or used link is `422`
`password:invalid-token`. The default expiry is one hour; configure
`passwords.ResetTTL(d)` alongside `Reset` to change it.

The link binds to the current password hash. A conditional write makes it
single-use even if two submissions race; a successful reset or password change
invalidates every link bound to the old hash.

A reset also rotates the user's credential version, ending every session,
pending login, and API token. It never logs Ada in: she signs in again,
including her second factor if it remains enrolled.

If Ada's email was unverified, the reset proves the mailbox, marks it verified,
and removes connections, two-factor enrollment, recovery codes, and tokens
an earlier registrant might have attached. If it was already verified,
connections and two-factor enrollment remain. Review retained connections
according to your [account-recovery policy](accounts.md#security-notifications).

## Verify an Email

An unverified user can log in, but cannot connect a provider, enroll two-factor,
or create personal or device tokens. Verification unlocks those actions;
it does not establish a new login.

To send Ada a verification email from your invitation flow, use:

```go
err := identity.Directory().RequestVerification(ctx, subject)
```

The mounted `POST /email/verify/resend` requests the same work for the logged-in
user and returns `202`. It allows 5 requests per user and 20 per client address
in 15 minutes, then `429`. Dispatch failure returns `500`.
[Registration](registration.md) can also request verification automatically.

The verification page extracts the fragment exactly as the reset page does.
Before submitting, Ada must be logged in **as the account the link belongs
to**. Keep the signed value out of URL queries and logs while completing login.
Then send:

```js
const response = await fetch("/email/verify", {
  method: "POST",
  headers: { "Content-Type": "application/json" },
  body: JSON.stringify({ token }),
});
```

`204` marks the address verified. Without a session the response is `401`;
another user's session receives `403` `account:verify-wrong-account`.
Neither failure changes the address. A second click, invalid signature, or
expired link returns `422` `account:invalid-token`.
The default lifetime is 24 hours; set `accounts.VerifyTTL(d)` in the
`Directory` callback to change it.

Requiring the matching session prevents an unsolicited email click from
verifying an account whose password belongs to someone else. If Ada did not
register the account and does not know its password, she uses password recovery,
not verification, to claim it safely.

For your own application routes, require a verified email after authentication:

```go
verified := web.With(auth.Require(), identity.RequireVerified())
verified.Post("/posts", createPost)
```

`web` is the authenticated router from [Overview](overview.md#mount-routes).
An unverified user receives `403`; a successful verification takes effect on the
next request without another login. With [your own users](own-users.md#verified-emails),
you provide the corresponding verification functions and UI yourself.

Next, decide whether to open [Registration](registration.md).

For the link contracts, see the
[`password` API](https://pkg.go.dev/arkoren.dev/ark/iam/password) and
[`account` API](https://pkg.go.dev/arkoren.dev/ark/iam/account).
