package oauthauth

import (
	"net/http"
	"slices"
	"strings"

	"arkoren.dev/core/auth"
	"arkoren.dev/core/http/request"
	"arkoren.dev/core/http/router"
	"arkoren.dev/foundation/problem"
	"arkoren.dev/foundation/validate"
)

// nativeLoginSchema parses the body of [Client.NativeHandler]. It fixes the
// member type and documents it; a missing token is the handler's own
// [ErrInvalidLoginRequest].
var nativeLoginSchema = validate.Struct(func(object *validate.StructBuilder[nativeLoginBody]) {
	object.Field("IDToken", func(token *validate.StringBuilder[string]) {
		token.Describe("The ID token the provider's mobile SDK issued to the app. Required.")
	})
})

// sessionCookieSchema describes the Set-Cookie header the session middleware
// adds when a login changes the session.
const sessionCookieSchema = `{"type":"string","description":"The session cookie. Its name and attributes belong to the session middleware."}`

// locationSchema describes the Location header of a redirect.
const locationSchema = `{"type":"string"}`

// problems declares every problem as a response of operation. Problems that
// share a status are described together, so one status never hides another's
// identity: the description lists each title with its stable type.
func problems(operation *router.OperationBuilder, all ...problem.Details) {
	titles := map[int][]string{}
	order := []int{}

	for _, details := range all {
		operation.Problem(details)

		status := details.Status()

		if _, seen := titles[status]; !seen {
			order = append(order, status)
		}

		title := details.Title() + " (" + details.Type() + ")"

		if !slices.Contains(titles[status], title) {
			titles[status] = append(titles[status], title)
		}
	}

	for _, status := range order {
		operation.Response(status, strings.Join(titles[status], "; "))
	}
}

// LoginOperation describes [Client.LoginHandler]. It needs no identity,
// remembers the optional return_to target in a sealed cookie and redirects (302)
// to the provider; fresh=1 asks an OpenID Connect provider to authenticate the
// user again. An unsafe return_to is 400 [ErrInvalidLoginRequest], a client that
// has not started 503 [ErrNotStarted], and an unreachable provider 502
// [ErrProviderUnavailable]. The annotation changes no handler behavior.
//
// Example:
//
//	web.Get("/auth/google", client.LoginHandler(), oauthauth.LoginOperation)
func LoginOperation(operation *router.OperationBuilder) {
	operation.Summary("Start a login")
	operation.Description("Redirects the browser to the identity provider to start an Authorization Code login. " +
		"The login state (state, nonce and PKCE verifier) travels in a sealed cookie that lives a few minutes. " +
		"It needs no session.")
	operation.Tags("oauth")
	operation.Parameter("return_to", "query", router.RawSchema(`{"type":"string","default":"/","description":"A local path to return to after the login. Unsafe or non-local targets are rejected."}`))
	operation.Parameter("fresh", "query", router.RawSchema(`{"type":"string","enum":["1"],"description":"Asks an OpenID Connect provider to authenticate the user again (prompt=login, max_age=0). A hint that providers may ignore; OAuth2 providers ignore it."}`))
	operation.Response(http.StatusFound, "Redirected to the provider")
	operation.ResponseHeader(http.StatusFound, "Location", router.RawSchema(locationSchema))
	operation.ResponseHeader(http.StatusFound, "Set-Cookie", router.RawSchema(`{"type":"string","description":"The sealed login state cookie."}`))
	problems(operation, ErrInvalidLoginRequest, ErrProviderUnavailable, ErrNotStarted)
}

// CallbackOperation describes [Client.CallbackHandler], registered for GET and,
// for providers that answer with a form post (Apple), POST, on one path: it
// carries no operation ID. The provider's code, state and error arrive as query
// parameters (GET) or form fields (POST). A verified login that is complete
// redirects (302) to the saved return target, one that needs a second factor to
// [ClientBuilder.ChallengeURL]. A callback that cannot be verified is 400
// [ErrInvalidCallback], a session-less request 401, and an identity the
// application's resolver refuses is the resolver's problem, such as a 403.
//
// Example:
//
//	web.Methods([]string{http.MethodGet, http.MethodPost}, "/auth/google/callback",
//		client.CallbackHandler(guard, resolve), oauthauth.CallbackOperation)
func CallbackOperation(operation *router.OperationBuilder) {
	operation.Summary("Finish a login")
	operation.Description("Receives the provider's redirect, verifies the sealed login state, exchanges the code, verifies " +
		"the identity, maps it to a local user with the application's resolver and begins the login on the guard. " +
		"The parameters arrive as query parameters on GET and as form fields on POST (providers that answer with a form post). " +
		"A complete login redirects to the return target saved at the start; a login that needs a second factor redirects to " +
		"the configured challenge URL. The login cookie is consumed and a replayed callback fails. " +
		"It needs the session cookie of the browser that started the login.")
	operation.Tags("oauth")
	operation.Parameter("code", "query", router.RawSchema(`{"type":"string","description":"The authorization code. Form field on POST."}`))
	operation.Parameter("state", "query", router.RawSchema(`{"type":"string","description":"The state of the login that started. Form field on POST."}`))
	operation.Parameter("error", "query", router.RawSchema(`{"type":"string","description":"The provider's error code when access was denied. Form field on POST."}`))
	operation.Response(http.StatusFound, "Logged in or parked for a second factor; redirected")
	operation.ResponseHeader(http.StatusFound, "Location", router.RawSchema(locationSchema))
	operation.ResponseHeader(http.StatusFound, "Set-Cookie", router.RawSchema(sessionCookieSchema))
	problems(operation, ErrInvalidCallback, auth.ErrUnauthenticated, ErrProviderUnavailable, ErrNotStarted)
}

// NonceOperation describes [Client.NonceHandler]. It needs the session cookie and
// answers 200 with the nonce the mobile app includes in its ID token request,
// stored in the session for one native login; the answer is not cacheable. A
// provider without native login is 501 [ErrUnsupported].
//
// Example:
//
//	web.Get("/auth/google/nonce", client.NonceHandler(), oauthauth.NonceOperation)
func NonceOperation(operation *router.OperationBuilder) {
	operation.Summary("Native login nonce")
	operation.Description("Issues a random nonce and stores it in the session. The mobile app puts it in its ID token " +
		"request to the provider's SDK, and the native login route checks the token carries it. The nonce is single use and " +
		"lives a few minutes. Needs a session; the answer is not cacheable.")
	operation.Tags("oauth")
	operation.JSONResponse(http.StatusOK, router.RawSchema(`{"type":"object","required":["nonce"],"properties":{"nonce":{"type":"string"}}}`))
	operation.Response(http.StatusOK, "The nonce")
	operation.ResponseHeader(http.StatusOK, "Cache-Control", router.RawSchema(`{"type":"string","const":"no-store"}`))
	problems(operation, auth.ErrUnauthenticated, ErrUnsupported)
}

// NativeOperation describes [Client.NativeHandler]. The body carries the app's ID
// token, which is verified with its nonce and audience; a complete login is 204
// and a parked one 202 with the second-factor methods, both with the session
// cookie. A missing token is 400 [ErrInvalidLoginRequest], a token that fails
// verification or carries the wrong nonce 400 [ErrInvalidCallback], malformed
// JSON 400, a body of the wrong shape 422, no session 401 and an unsupported
// provider 501 [ErrUnsupported].
//
// Example:
//
//	web.Post("/auth/google/native", client.NativeHandler(guard, resolve), oauthauth.NativeOperation)
func NativeOperation(operation *router.OperationBuilder) {
	operation.Summary("Native login")
	operation.Description("Logs a user in with an ID token the provider's mobile SDK issued to the app. The token must " +
		"carry the nonce issued by the nonce route for this session and an accepted native audience. " +
		"Answers 204 when the login is complete, or 202 with the second-factor methods when the user has to answer a challenge. " +
		"Needs the session cookie of the nonce request.")
	operation.Tags("oauth")
	operation.JSONBody(nativeLoginSchema).BodyRequired(true)
	operation.Response(http.StatusNoContent, "Logged in")
	operation.ResponseHeader(http.StatusNoContent, "Set-Cookie", router.RawSchema(sessionCookieSchema))
	operation.JSONResponse(http.StatusAccepted, validate.Infer[auth.SecondFactorChallenge]())
	operation.Response(http.StatusAccepted, "A second factor is required")
	operation.ResponseHeader(http.StatusAccepted, "Set-Cookie", router.RawSchema(sessionCookieSchema))
	problems(operation, ErrInvalidLoginRequest, ErrInvalidCallback, request.ErrInvalidJSON, request.ErrValidationFailed,
		auth.ErrUnauthenticated, ErrUnsupported, ErrProviderUnavailable, ErrNotStarted)
	operation.Problem(request.ErrBodyTooLarge)
}
