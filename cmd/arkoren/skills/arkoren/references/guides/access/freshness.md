---
title: Fresh Logins
description: Know which routes need a fresh login and which need a brand-new one, how a provider's auth_time can only make a login older, and why provider-only users sign in again through the provider.
order: 68
summary:
  - Tell a fresh login, which re-proving a step-up method renews, from a recent login, which only a new login satisfies.
  - See which routes of the facade ask for which, and how to protect your own routes with each.
  - Understand what a provider's auth_time changes, and why users of Google and GitHub sign in again through the provider.
---

# Fresh Logins

A session cookie proves that someone logged in at some point, not that the person
holding it is still the user. So the routes that change a credential or hand out
a long-lived secret ask for more: a login that is recent enough. The facade uses
two checks for this, and they differ in what can renew them. This page says which
route uses which, so you can protect your own routes the same way and tell your
front end what to do when a route answers `403`.

## Fresh and Recent

A login is **fresh** when the user proved a factor of one of their step-up methods
within the window, 15 minutes by default. The step-up methods are the password
(`pwd`) and the authenticator (`otp`), and only those the user has set up count.
A fresh login is renewable: the client posts `POST /confirm` with
`{"method": "pwd", "password": "..."}` or an authenticator code, and the login is
fresh again. A user with no step-up method at all, such as someone who only signs in
with a provider, is fresh right after any login and stale when the window passes;
they renew it by logging in again.

A login is **recent** when the session was created by a login at most a given time
ago. Confirming does not renew it, and neither does a silent re-login of the same
user in an open session. Only a new login counts. This is the check for handing out
a long-lived secret, where "the user is here now" is not enough and "the user just
logged in" is the point.

Set the freshness window for every route with the guard's `FreshFor`:

```go
iam.Guard(func(policy *auth.GuardBuilder) {
	policy.FreshFor(5 * time.Minute)
})
```

When a route needs a fresh login and the login is stale, the answer is `403` with
the problem `step-up-required` and the methods the user can confirm with. A user
who has no step-up method to confirm with and a stale login gets
`no-step-up-method`: they log in again.

## Which Routes

| Route | Check |
| --- | --- |
| `PUT /password`, `DELETE /account` | Fresh |
| `POST /two-factor/setup`, `POST /two-factor` | Fresh |
| `DELETE /two-factor`, `POST /two-factor/recovery-codes` | Fresh, or a recovery code used within the window, plus a recent second factor |
| `POST /tokens` | Fresh |
| `POST /connections/{provider}`, `DELETE /connections/{id}` | Fresh |
| `POST /device-tokens` | Recent: a login at most five minutes old (`apitoken.ExchangeWindow`) |

Disabling two-factor needs a recent authenticator or recovery code in addition,
within the two-factor window (15 minutes by default). A recovery code used to log
in also counts as the fresh login for these two routes, because a user who lost
their authenticator has nothing else to prove.

Your own routes use the same two checks on the guard that the facade built:

```go
guard := iam.Guard()

// Fresh: renewable by POST /confirm.
posts.Delete("/posts/{id}", func(w http.ResponseWriter, r *http.Request) error {
	if err := guard.Fresh(r); err != nil {
		return err
	}

	return deletePost(w, r)
})

// Recent: only a new login counts.
posts.Post("/exports", func(w http.ResponseWriter, r *http.Request) error {
	if err := guard.RecentLogin(r, 5*time.Minute); err != nil {
		return err
	}

	return startExport(w, r)
})
```

`guard.RequireFresh(5*time.Minute)` wraps the first check as middleware with a
window of its own. The facade's credential routes also require a browser session,
so a stolen bearer token can never change a credential, whatever it carries.

## Provider Logins

A login through a provider is one factor of the method `federated`. It is not a
step-up method, so signing in again through a provider never makes a user who has a
password or an authenticator fresh: they confirm with the password or the code, not
with the provider. A user who signs in only through providers has no step-up method,
so their freshness is simply how long ago they logged in.

The factor's time is the callback's time, unless the provider's ID token says the
user authenticated earlier. The `auth_time` claim of an OIDC provider can make the
login older than the callback, never fresher: a time in the future or a missing
claim leaves the callback time. This matters for silent single sign-on. When the
browser already has a session at the provider, the provider approves without asking,
the callback happens now, and without `auth_time` the facade would take that for a
deliberate sign-in. With the claim, a user whose provider session is three hours old
is three hours old for the facade too, and a credential change needs a new sign-in.

How much of this you get depends on the provider:

- **Microsoft Entra** sends `auth_time` when you add it as an optional claim in the
  app registration; see [Organization Sign-In](organization-sign-in.md#register-the-app)
  for the manifest.
- **Google** and **GitHub**: the facade applies an `auth_time` whenever the ID token
  carries one, but do not count on it from Google, and GitHub has no ID token at
  all. Without it a silent approval counts as a login now. A user who signs in
  only with Google or GitHub has no password or authenticator on your side to
  confirm with, so the only way to be fresh again once the window passes is to
  sign in again through the provider: send them to `GET /auth/google?fresh=1`. The `fresh=1` parameter is only a hint:
  it adds `prompt=login&max_age=0` to the request, which asks an OIDC provider to
  authenticate again instead of approving silently, and it is recorded in the
  login state. The provider is free to ignore it, and GitHub's OAuth2 flow has
  nothing to ask.
- **Generic OIDC** providers are as good as their ID tokens. Check whether yours
  sends `auth_time`.

Where a deliberate sign-in matters, such as handing out a device token, ask for a
recent login rather than a fresh one. `RecentLogin` checks when your application's
own login was created, not when the user last authenticated at the provider: a
silent re-login inside an already open session keeps the original login and does
not extend the window, but a new login of your application that the provider
approves silently creates a new one and passes. `RecentLogin` therefore proves
that the user started a login on your side recently, not that they typed
credentials.

For every function and option, see
[the `access` API reference](https://pkg.go.dev/arkoren.dev/ark/access).
