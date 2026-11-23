package iam

import (
	"net/http"

	"arkoren.dev/core/auth"
	"arkoren.dev/core/http/router"
	"arkoren.dev/foundation/problem"
	"arkoren.dev/foundation/validate"

	"arkoren.dev/ark/iam/internal/web"
)

// credentialBodySchema describes the body of the challenge and confirm routes:
// "method" selects a registered credential and the other members are that
// credential's own input.
const credentialBodySchema = `{
  "type":"object","required":["method"],"additionalProperties":true,
  "properties":{"method":{"type":"string","description":"The credential method, for example \"otp\" or \"recovery\"."}}
}`

// LogoutOperation describes [IAM.LogoutHandler]. It ends the logged-in session,
// its login record and any pending login, regenerates the session ID and answers
// 204 with the re-issued session cookie. Logging out an anonymous session also
// succeeds, so no 401 is declared. Like every state-changing route of the module
// it needs Content-Type application/json (415 otherwise) and belongs behind CSRF
// protection.
//
// Example:
//
//	web.Post("/logout", identity.LogoutHandler(), iam.LogoutOperation)
func LogoutOperation(operation *router.OperationBuilder) {
	operation.Summary("Log out")
	operation.Description("Ends the logged-in session and its login record and regenerates the session ID. " +
		"Logging out a session without a logged-in user also succeeds. The request needs Content-Type application/json " +
		"although it has no body.")
	operation.Tags("authentication")
	operation.Response(http.StatusNoContent, "Logged out")
	web.Cookie(operation, http.StatusNoContent)
	web.Problems(operation, web.ContentProblems)
}

// PendingOperation describes [IAM.PendingHandler]. The 200 body lists the
// second-factor methods of the parked login without naming its subject; a
// request without a pending login, or one locked out of the second factor, is
// 401. It spends no attempt.
//
// Example:
//
//	web.Get("/login/pending", identity.PendingHandler(), iam.PendingOperation)
func PendingOperation(operation *router.OperationBuilder) {
	operation.Summary("Pending second-factor login")
	operation.Description("Lists the second-factor methods a client with a parked login can use. It needs the session " +
		"cookie of the parked login, not an identity, and spends no attempt.")
	operation.Tags("authentication")
	operation.JSONResponse(http.StatusOK, router.RawSchema(`{"type":"object","required":["methods"],
		"properties":{"methods":{"type":"array","items":{"type":"string"}}}}`))
	operation.Response(http.StatusOK, "The methods the parked login can use")
	web.Problems(operation, web.SessionProblems)
}

// ChallengeOperation describes [IAM.ChallengeHandler], which finishes a parked
// login with one second-factor credential: the body names the method and carries
// its input, and success is 204 with the re-issued session cookie. A wrong code
// or a method the user is not enrolled in is 403, an unknown method 422, and
// used-up attempts or no pending login 401.
//
// Example:
//
//	web.Post("/login/challenge", identity.ChallengeHandler(), iam.ChallengeOperation)
func ChallengeOperation(operation *router.OperationBuilder) {
	operation.Summary("Finish a second-factor login")
	operation.Description("Verifies one second-factor credential for the parked login. The body's \"method\" selects a " +
		"registered method and the other members are that method's input. Attempts are limited per user across methods " +
		"and sessions; a wrong code rotates the session ID and the last attempt discards the pending login. " +
		"Success logs the user in.")
	operation.Tags("authentication")
	operation.JSONBody(router.RawSchema(credentialBodySchema)).BodyRequired(true)
	operation.Response(http.StatusNoContent, "Logged in")
	web.Cookie(operation, http.StatusNoContent)
	web.Problems(operation, web.ContentProblems, []problem.Details{problem.BadRequest(), auth.ErrUnauthenticated, auth.ErrInvalidCredential, auth.ErrUnknownMethod})
}

// ConfirmOperation describes [IAM.ConfirmHandler], which re-proves a logged-in
// user with a step-up method. It needs a browser session identity (a bearer
// token never confirms); success is 204 with the re-issued session cookie, and a
// wrong code, a method the user is not enrolled in or a lockout is 403.
//
// Example:
//
//	web.Post("/confirm", identity.ConfirmHandler(), iam.ConfirmOperation)
func ConfirmOperation(operation *router.OperationBuilder) {
	operation.Summary("Confirm a login")
	operation.Description("Re-proves the logged-in user with a step-up method (\"pwd\" or \"otp\" by default), which makes " +
		"the session fresh for the credential routes. The body's \"method\" selects the credential and the other members " +
		"are its input. A locked-out user is answered like a wrong code.")
	operation.Tags("authentication")
	operation.JSONBody(router.RawSchema(credentialBodySchema)).BodyRequired(true)
	operation.Response(http.StatusNoContent, "Confirmed")
	web.Cookie(operation, http.StatusNoContent)
	web.Problems(operation, web.ContentProblems, []problem.Details{problem.BadRequest(), auth.ErrUnauthenticated, auth.ErrInvalidCredential, auth.ErrUnknownMethod})
}

// ListSessionsOperation describes [IAM.ListSessionsHandler]. It needs a browser
// session and answers 200 with the user's logins, the request's own marked
// current.
//
// Example:
//
//	web.Get("/sessions", identity.ListSessionsHandler(), iam.ListSessionsOperation)
func ListSessionsOperation(operation *router.OperationBuilder) {
	operation.Summary("List sessions")
	operation.Description("Lists the logged-in user's logins with device, client address and times; the request's own " +
		"login is marked current. Needs a browser session.")
	operation.Tags("sessions")
	operation.JSONResponse(http.StatusOK, validate.Infer[[]auth.LoginRecord]())
	operation.Response(http.StatusOK, "The user's logins")
	web.Problems(operation, web.SessionProblems)
}

// DeleteSessionOperation describes [IAM.DeleteSessionHandler]. It needs a
// browser session and answers 204 after ending the login with that ID; an
// unknown ID changes nothing.
//
// Example:
//
//	web.Delete("/sessions/{id}", identity.DeleteSessionHandler(), iam.DeleteSessionOperation)
func DeleteSessionOperation(operation *router.OperationBuilder) {
	operation.Summary("End a session")
	operation.Description("Ends the login with the given ID, which may be another device or this one. An unknown ID " +
		"changes nothing and also answers 204. Needs a browser session.")
	operation.Tags("sessions")
	operation.Parameter("id", "path", router.RawSchema(`{"type":"string","description":"The login's ID, as listed by the sessions route."}`))
	operation.Response(http.StatusNoContent, "The login ended or did not exist")
	web.Problems(operation, web.SessionProblems)
}

// DeleteOtherSessionsOperation describes [IAM.DeleteOtherSessionsHandler]. It
// needs a browser session and answers 204 after ending every login of the user
// except the request's own.
//
// Example:
//
//	web.Delete("/sessions", identity.DeleteOtherSessionsHandler(), iam.DeleteOtherSessionsOperation)
func DeleteOtherSessionsOperation(operation *router.OperationBuilder) {
	operation.Summary("End the other sessions")
	operation.Description("Ends every login of the user except the one that made the request. Needs a browser session.")
	operation.Tags("sessions")
	operation.Response(http.StatusNoContent, "The other logins ended")
	web.Problems(operation, web.SessionProblems)
}

// DeleteAccountOperation describes [IAM.DeleteAccountHandler]. It needs a
// browser session and a fresh login and answers 204 after removing the user and
// every package's rows in one transaction; the session ends, so the cookie is
// cleared.
//
// Example:
//
//	web.Delete("/account", identity.DeleteAccountHandler(), iam.DeleteAccountOperation)
func DeleteAccountOperation(operation *router.OperationBuilder) {
	operation.Summary("Delete the account")
	operation.Description("Deletes the logged-in user and everything stored for them (passwords, two-factor, tokens, " +
		"connections, login records) in one transaction; a failure of any step leaves the user in place. " +
		"Needs a browser session and a fresh login.")
	operation.Tags("account")
	operation.Response(http.StatusNoContent, "Account deleted")
	web.Cookie(operation, http.StatusNoContent)
	web.Problems(operation, web.FreshProblems)
}

// AccountOperation describes [IAM.AccountHandler]. It answers 200 with the
// [AccountView] of the request, for a session cookie or a token alike; a request
// without an identity, including a login still waiting for its second factor, is
// 401.
//
// Example:
//
//	web.Get("/account", identity.AccountHandler(), iam.AccountOperation)
func AccountOperation(operation *router.OperationBuilder) {
	operation.Summary("Current account")
	operation.Description("Describes the authenticated user: subject, email state, and, as enabled with Builder.Account, " +
		"two-factor state, how the request authenticated, and the identity's roles and permissions. " +
		"Never includes application data.")
	operation.Tags("account")
	operation.JSONResponse(http.StatusOK, validate.Infer[AccountView]())
	operation.Response(http.StatusOK, "The authenticated user")
	web.Problems(operation, web.SessionProblems)
}

// providerFailures describes what a provider's handlers answer when a login
// cannot proceed, as the [Provider] contract leaves the details to the provider.
func providerFailures(operation *router.OperationBuilder) {
	web.Problems(operation, []problem.Details{problem.BadRequest(), auth.ErrUnauthenticated})
	operation.Response(http.StatusBadGateway, "The provider could not be reached or rejected the request")
	operation.Response(http.StatusServiceUnavailable, "The provider is not started")
}

// ProviderLoginOperation describes [Provider.LoginHandler] at "/auth/<name>":
// it needs no identity, redirects (302) to the provider and remembers the
// optional local return_to target in a sealed cookie. The query parameter
// fresh=1 asks the provider to authenticate the user again.
//
// Example:
//
//	web.Get("/auth/google", google.LoginHandler(), iam.ProviderLoginOperation)
func ProviderLoginOperation(operation *router.OperationBuilder) {
	operation.Summary("Start a provider login")
	operation.Description("Redirects the browser to the identity provider. The optional return_to query parameter names a " +
		"local path to return to after the login, and fresh=1 asks an OpenID Connect provider to authenticate the user " +
		"again; providers may ignore it. The login state travels in a sealed cookie. Connecting a provider to a logged-in " +
		"user starts with POST /connections/{provider}, which then redirects here.")
	operation.Tags("providers")
	operation.Parameter("return_to", "query", router.RawSchema(`{"type":"string","default":"/","description":"A local path to return to after the login."}`))
	operation.Parameter("fresh", "query", router.RawSchema(`{"type":"string","enum":["1"],"description":"Asks the provider to authenticate the user again."}`))
	operation.Response(http.StatusFound, "Redirected to the provider")
	operation.ResponseHeader(http.StatusFound, "Location", router.RawSchema(`{"type":"string"}`))
	providerFailures(operation)
}

// ProviderCallbackOperation describes [Provider.CallbackHandler] at
// "/auth/<name>/callback", registered for GET and POST. It verifies the
// provider's answer, begins the login on the guard and redirects (302) to the
// return target, or to the challenge page when a second factor is required. It
// carries no operation ID because one registration covers both methods.
//
// Example:
//
//	web.Methods([]string{http.MethodGet, http.MethodPost}, "/auth/google/callback",
//		google.CallbackHandler(guard, resolve), iam.ProviderCallbackOperation)
func ProviderCallbackOperation(operation *router.OperationBuilder) {
	operation.Summary("Finish a provider login")
	operation.Description("Receives the provider's redirect with an authorization code and state (GET, or POST for providers " +
		"that answer with a form post), verifies it, maps the verified identity to a user and logs the user in. " +
		"Redirects to the return target, or to the configured challenge page when the user has to answer a second factor. " +
		"A provider error, a missing code, an unknown or expired state, and an identity the application does not accept " +
		"are rejected.")
	operation.Tags("providers")
	operation.Parameter("code", "query", router.RawSchema(`{"type":"string","description":"The authorization code."}`))
	operation.Parameter("state", "query", router.RawSchema(`{"type":"string","description":"The state of the login that started."}`))
	operation.Parameter("error", "query", router.RawSchema(`{"type":"string","description":"The provider's error code when the user denied access."}`))
	operation.Response(http.StatusFound, "Logged in or parked for a second factor; redirected")
	operation.ResponseHeader(http.StatusFound, "Location", router.RawSchema(`{"type":"string"}`))
	web.Cookie(operation, http.StatusFound)
	providerFailures(operation)
	web.Problems(operation, []problem.Details{problem.Forbidden()})
}

// ProviderNonceOperation describes [NativeProvider.NonceHandler] at
// "/auth/<name>/nonce". It needs the session cookie and answers 200 with the
// single-use nonce the mobile app puts in its ID token request; the answer is
// not cacheable.
//
// Example:
//
//	web.Get("/auth/google/nonce", google.NonceHandler(), iam.ProviderNonceOperation)
func ProviderNonceOperation(operation *router.OperationBuilder) {
	operation.Summary("Native login nonce")
	operation.Description("Issues the nonce a mobile app includes in its provider ID token request, stored in the session " +
		"for the next native login. It is single use and short lived.")
	operation.Tags("providers")
	operation.JSONResponse(http.StatusOK, router.RawSchema(`{"type":"object","required":["nonce"],"properties":{"nonce":{"type":"string"}}}`))
	operation.Response(http.StatusOK, "The nonce")
	operation.ResponseHeader(http.StatusOK, "Cache-Control", router.RawSchema(`{"type":"string","const":"no-store"}`))
	web.Problems(operation, web.SessionProblems)
	operation.Response(http.StatusNotImplemented, "The provider has no native login")
}

// ProviderNativeOperation describes [NativeProvider.NativeHandler] at
// "/auth/<name>/native". The body carries the app's ID token, which is verified
// against the nonce of the session; a complete login is 204 and a parked one 202
// with the second-factor methods, both with the session cookie.
//
// Example:
//
//	web.Post("/auth/google/native", google.NativeHandler(guard, resolve), iam.ProviderNativeOperation)
func ProviderNativeOperation(operation *router.OperationBuilder) {
	operation.Summary("Native provider login")
	operation.Description("Logs a user in with an ID token issued to a mobile app by the provider's own SDK. The token must " +
		"carry the nonce issued for this session and an accepted audience. Answers 204 when the login is complete, or 202 " +
		"with the second-factor methods when the user has to answer a challenge.")
	operation.Tags("providers")
	operation.JSONBody(router.RawSchema(`{"type":"object","required":["id_token"],"properties":{"id_token":{"type":"string"}}}`)).BodyRequired(true)
	operation.Response(http.StatusNoContent, "Logged in")
	web.Cookie(operation, http.StatusNoContent)
	operation.JSONResponse(http.StatusAccepted, validate.Infer[auth.SecondFactorChallenge]())
	operation.Response(http.StatusAccepted, "A second factor is required")
	web.Cookie(operation, http.StatusAccepted)
	providerFailures(operation)
	web.Problems(operation, []problem.Details{problem.Forbidden()})
	operation.Response(http.StatusNotImplemented, "The provider has no native login")
}
