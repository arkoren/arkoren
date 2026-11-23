---
title: Email Links
description: Give IAM a queue, a mailer and page addresses, then let users reset passwords and verify addresses through signed, single-use links.
order: 30
summary:
  - Wire the queue, mailer and page addresses IAM needs to send its own emails.
  - Run IAM's jobs in the process that sends mail and replace the default wording.
  - Submit signed links safely and distinguish password recovery from email verification.
---

# Email Links

A forgotten password needs a recovery path, and an email address needs proof
before it can be trusted. IAM supplies signed links for both and sends the
emails itself: it queues its own jobs on your dispatcher from inside the request
and sends through your mailer in a worker. You supply a signer, a mailer, the
address of the page each link opens, and the queue. You write no job, handler
or send function.

This page extends the [password-enabled facade](overview.md#start-small).
It uses an existing queue dispatcher and consumer; their connection and worker
lifecycle belong to [Queues](../queues/overview.md), and the mailer to
[Mail](../mail/overview.md).

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

## Enable the Flows

Give IAM the dispatcher with `Queue`, then enable each flow with the signer,
your `*mail.Mailer` and the absolute address of the page that the link opens.
That page belongs to your front end, not to IAM, and its address must not carry
a fragment:

```go
identity, err := iam.New(db, store, func(identity *iam.Builder) {
	identity.Queue(dispatcher)
	identity.Password(hasher, func(passwords *password.Builder) {
		passwords.Reset(signer, mailer, "https://app.example.com/reset-password")
	})
	identity.Directory(func(accounts *account.Builder) {
		accounts.Verification(signer, mailer, "https://app.example.com/verify-email")
	})
})
```

This leaves registration closed. Without `Reset`, IAM registers neither
`/password/forgot` nor `/password/reset`. Without `Verification`, it registers
neither email route. A flow missing its signer, mailer, page address or `Queue`
makes `iam.New` fail with `iam.ErrInvalidConfig`; requesting one of its
unavailable handlers directly panics during composition.

The emails are short plain-text messages with a default subject and wording,
sent from the mailer's default sender. Delivery always uses the address stored
with the user, never an address copied from an incoming reset request.

## Run the Jobs

Requests store work; a worker sends it. In the process that sends the emails,
register IAM's handlers on the consumer once `iam` exists:

```go
err = identity.RegisterJobs(consumer)
```

`RegisterJobs` registers the handler of every job of the features you enabled,
so a feature you enable later needs no further wiring. The jobs run on the queue
named by `iam.QueueName` (`"iam"`) and carry the facade's `Instance`, so several
instances can share one consumer. IAM cannot see a restriction set with
`ConsumerBuilder.Queues`: if you restrict the consumer, list `iam.QueueName` or
the stored jobs wait unconsumed.

With the memory queue, the process that stores the jobs must run the consumer.
With a networked queue driver, run `RegisterJobs` and the consumer in a worker,
and deploy that worker before you enable a new email feature on the servers.
Otherwise jobs queue up with nobody to send them.

The worker looks the user up and signs the link just before sending, so a stored
job holds only the request: the typed identifier for a reset, the subject for a
verification. It silently skips unknown or disabled users (reset) and deleted or
already verified users (verification). A mailer failure is returned so the
queue's retry policy decides what happens next; an email the mailer rejects as
malformed fails the job permanently, because retrying cannot fix it. Sending can
therefore run more than once. Each attempt signs a link; earlier attempts remain
usable until their expiry or the state they bind to changes. Neither flow needs a
database table of email-link tokens. A [workflow](../workflows/steps.md) can call
`Verifier.SendReset` and `Directory.SendVerification` from a step instead of using
the queue.

## Change the Wording

To write your own email, pass a function to `Email` in the flow's callback. It
receives the message and the page link to put in the body, and returns a
`mail.Message`. The signed value is a credential: keep it out of logs, analytics
and exception reports.

```go
passwords.Reset(signer, mailer, resetPage, func(reset *password.ResetBuilder) {
	reset.Email(func(message password.ResetMessage, href string) mail.Message {
		return mail.NewMessage(
			mail.Recipients{To: []mail.Address{{Email: message.Address}}},
			mail.Body{Text: href},
		)
	})
})
```

`message.Notice` distinguishes an ordinary reset request from a
[registration attempt using a taken address](registration.md#registration-emails).
In the latter email, explain that setting a new password recovers the account
if the recipient did not create it. Verification uses
`accounts.Verification(..., func(verification *account.VerificationBuilder) { verification.Email(...) })`
with an `account.VerifyMessage`, which has no `Notice` field. Stored request
values remain decodable across IAM upgrades: new fields are additive, with zero
values for older payloads.

For a concrete mail adapter, see [composing mail](../mail/composing-messages.md).

## Without the Facade

Code that builds `password.NewVerifier` or `account.NewDirectory` by itself calls
`Queue(dispatcher)` on the package builder, next to `Reset` or `Verification`,
and registers the handler with `verifier.RegisterJobs(consumer)` or
`directory.RegisterJobs(consumer)`. The same names, queue and rules apply.

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

The registered `POST /email/verify/resend` requests the same work for the logged-in
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

`web` is the authenticated router from [Overview](overview.md#register-routes).
An unverified user receives `403`; a successful verification takes effect on the
next request without another login. With [your own users](own-users.md#verified-emails),
you provide the corresponding verification functions and UI yourself.

Next, decide whether to open [Registration](registration.md).

For the link contracts, see the
[`password` API](https://pkg.go.dev/arkoren.dev/ark/iam/password) and
[`account` API](https://pkg.go.dev/arkoren.dev/ark/iam/account).
