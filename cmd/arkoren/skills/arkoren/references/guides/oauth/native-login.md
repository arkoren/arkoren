---
title: Native Login
description: Log in mobile apps that sign in with the Google or Apple SDK by verifying their ID token against a one-time nonce.
order: 30
summary:
  - Mount the nonce and native routes and follow the two-step exchange between the app and your server.
  - List the audiences your apps request tokens for, for Google and for Apple.
  - Know how email, the nonce and the outcome behave and which presets support native login.
---

# Native Login

A mobile app that signs in with the provider's own SDK (Credential Manager or
Google Sign-In, Sign in with Apple) receives an ID token and no authorization
code, so it cannot use the [browser callback](browser-login.md). Only the Google
and Apple presets support native login; every other preset returns
`ErrUnsupported`. The examples assume the started `client`, `guard` and mapper
from [Browser Login](browser-login.md#connect-routes).

## Mount Routes

Native login uses two routes on the same session, which the app keeps in its
cookie jar. Both use the default session of the session middleware. Mount them
behind the same rate limit as your other login routes:

```go
web.Get("/auth/google/nonce", client.NonceHandler(), oauthauth.NonceOperation)
web.Post("/auth/google/native", client.NativeHandler(guard, mapIdentity), oauthauth.NativeOperation)
```

`NonceHandler` answers `{"nonce": "..."}` and stores the nonce in the session.
The nonce is valid for ten minutes and one attempt spends it. The app gives the
nonce to the SDK, then posts `{"id_token": "..."}` to `NativeHandler`. That
handler verifies the token's signature, expiry, issuer, audience and nonce,
calls your mapper and `guard.Begin`, and answers like every other login method:
`204` when the person is logged in, or `202` with the second-factor methods to
choose from ([Hold a Login](../authentication/two-factor.md#hold-a-login)). The
session ID rotates, and a completed login publishes `LoginSucceeded`. Native
login needs no challenge URL, because the `202` response carries the methods.

What the app passes to the SDK depends on the provider. Google returns the nonce
unchanged in the token's `nonce` claim, so the app passes it as received. Apple
puts the request nonce into the token unchanged, and this client requires the
lowercase hexadecimal SHA-256 of the issued nonce, so the token never carries the
secret held in the session; any other form is rejected.

A failed attempt, including a wrong audience, costs the app a new nonce request.
A missing, expired or used nonce, a bad token or a nonce mismatch returns
`ErrInvalidCallback` and publishes `LoginFailed`. A malformed body or an empty
`id_token` returns `ErrInvalidLoginRequest` without spending the nonce.

## Accept Audiences

An **audience** names a token's intended recipient. List the audiences your apps
request tokens for with `ClientBuilder.NativeAudiences`. Without them the
handlers return `ErrNoNativeAudiences`, and a token for any other audience,
including the browser client ID, is rejected:

```go
client, err := oauth.New(oauth.Google(), cipher, webClientID,
	func(login *oauth.ClientBuilder) {
		login.NativeAudiences(webClientID, iosClientID)
	},
)
```

- **Google.** The ID token's audience is the server client ID the app configures
  in its SDK: Credential Manager's `setServerClientId` on Android and
  `GIDConfiguration.serverClientID` on iOS. That is normally the web client ID,
  the one you give `oauth.New`. List it, and any iOS or Android client ID your
  apps request tokens for.
- **Apple.** The `aud` is the app's `client_id`, which for a native app is its
  bundle ID, not the Services ID of browser login. Apple's token carries no
  name: the SDK hands it to the app on the first authorization only, so the app
  sends it to your API itself.

Email and `EmailVerified` follow the browser rules: Google's boolean
`email_verified` and Apple's `email_verified` (a boolean or `"true"`). A native
login sets no `Claims["scope"]`, because no authorization code is exchanged.
These facts come from the provider documentation:

- Google: [Authenticate with a backend server](https://developers.google.com/identity/sign-in/ios/backend-auth)
  and [OpenID Connect](https://developers.google.com/identity/openid-connect/openid-connect)
  for `iss`, `aud`, `exp`, `email_verified`, `hd` and the `nonce` claim;
  [`GetGoogleIdOption.Builder`](https://developers.google.com/identity/android-credential-manager/android/reference/com/google/android/libraries/identity/googleid/GetGoogleIdOption.Builder)
  and [`GIDConfiguration`](https://developers.google.com/identity/sign-in/ios/reference/Classes/GIDConfiguration)
  for the server client ID as token audience and the nonce.
- Apple: [Verifying a user](https://developer.apple.com/documentation/signinwithapple/verifying-a-user)
  (verify the nonce, `iss`, `aud` as the developer's `client_id` and `exp`) and the
  [identity token claims](https://developer.apple.com/documentation/signinwithapplejs/authorizationi/id_token)
  (`nonce`, `nonce_supported`, `email_verified` as a string or Boolean).

Native ID tokens are not accepted by [`Verify`](bearer-tokens.md), which
requires API audiences and rejects tokens that carry a nonce. To trade a native
login for a device token, check
[`guard.RecentLogin`](browser-login.md#federated-logins-and-step-up) first.

For all functions and options, see the
[`oauthauth` API reference](https://pkg.go.dev/arkoren.dev/ark/oauth/oauthauth).
