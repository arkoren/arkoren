---
title: Registration
description: Open password sign-up deliberately, handle its accepted response, and give both new users and existing address owners a safe path forward.
order: 40
summary:
  - Enable self-service registration only after configuring password recovery and email delivery.
  - Understand what registration creates, which email each branch sends, and why acceptance is not a login.
  - Account for address-enumeration limits and keep the sign-up UI honest.
---

# Registration

Leave registration closed if invitations, administrators, or external
providers determine who joins your application. Password login does not need
an open sign-up route. If anyone may join, enable registration explicitly,
after the [email-link flow](email-links.md) can recover an address registered
by someone who does not own it.

Provider sign-up is a separate, per-provider choice on
[Federated Sign-In](federated-sign-in.md#first-time-sign-in); enabling one does not enable
the other.

## Open Sign-Up

With the built-in users, `Registration(true)` requires password `Reset`;
otherwise `iam.New` returns `iam.ErrInvalidConfig`. Configure email
verification too so new users can prove their addresses and existing owners
receive a recovery notice. Reuse the signer and callbacks from Email Links:

```go
identity, err := iam.New(db, store, func(identity *iam.Builder) {
	identity.Password(hasher, func(passwords *password.Builder) {
		passwords.Reset(signer, dispatchReset, deliverReset)
		passwords.Registration(true)
	})
	identity.Directory(func(accounts *account.Builder) {
		accounts.Verification(signer, dispatchVerification, deliverVerification)
	})
})
```

After mounting, `POST /register` exists. Turning registration off removes that
route; it does not remove existing users or their ability to log in.

## Register an Address

To request an account, send `POST /register` as JSON:

```json
{
  "email": "ada@example.com",
  "password": "correct horse battery"
}
```

For a new address, IAM creates an unverified user and stores the password in
one database transaction. Success is an empty `202`, **not** a session:
the client still calls [password login](passwords.md#log-in).

For an address already in use, the route also returns `202`. It neither
overwrites the owner's password nor connects accounts by email.
A malformed email or rejected password receives `422`; the default
[password policy](passwords.md#password-policy) accepts 8–72 bytes.
The directory trims and lowercases plain email addresses and limits them to
254 bytes.

Until the address is verified, the new user can log in but cannot connect
providers, enroll two-factor, or create tokens. Their verification link must
be submitted while logged in as that account.

## Registration Emails

The email work depends on the enabled flows:

| Configuration | New address | Taken address |
| --- | --- | --- |
| Reset and verification | Queue a verification email | Queue a reset notice for the owner |
| Reset only | Queue nothing | Queue nothing |

With verification configured, both branches dispatch work rather than doing
one synchronous lookup-and-send path. A reset notice has
`password.ResetMessage.Notice` set; the email should explain the attempted
registration and offer a new password if the owner did not create the account.

A dispatch or post-registration hook failure is logged, but the response
stays `202`. Unlike an explicit forgot-password request, this response must not
distinguish the new-address branch from the taken-address branch.
Provide a resend-verification or forgot-password action if mail does not arrive.

With [your own users](own-users.md), `Users.Create` is required and IAM does
not dispatch these registration emails automatically. Your application owns
verification and equivalent owner notices.

## Response Limits

Do not describe `202` as proof that an account was created, and do not promise
that registration hides account existence from an active attacker. Someone
can register an address with a chosen password, then try logging in:
the password works for a new address and normally fails for a taken one.

The built-in throttles bound that probe rather than eliminate it. Registration
accepts 5 attempts per identifier **as typed** and 30 per client address in
15 minutes, then returns `429` with `Retry-After`; login limits apply as well.
An accepted probe of a new address still creates an unverified account.
A taken-address probe queues an owner notice only when verification is enabled,
as in the table above.

For the UI, say that the request was accepted, offer login, and explain that
an email may arrive. If login fails, offer recovery rather than claiming the
registration succeeded. An owner who later proves the mailbox through a reset
can [remove the earlier registrant's credentials](email-links.md#reset-a-password).

If account-existence privacy outweighs open sign-up, leave registration off.
For public registration, consider application-level
[rate limits](../abuse-prevention/rate-limiting.md) and
[CAPTCHA](../abuse-prevention/captcha.md) in addition to the built-in counters.

Before adding more credentials, read [Fresh Logins](freshness.md) to understand
why an authenticated session alone cannot change them.

For registration settings and hooks, see the
[`password` API reference](https://pkg.go.dev/arkoren.dev/ark/iam/password).
