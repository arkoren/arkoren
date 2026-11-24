package apitoken

import (
	"net/http"

	"arkoren.dev/core/auth"
	"arkoren.dev/core/http/router"
	"arkoren.dev/foundation/problem"
	"arkoren.dev/foundation/validate"

	"arkoren.dev/ark/iam/internal/web"
)

// deviceBody is the JSON body of [Issuer.ExchangeHandler].
type deviceBody struct {
	Device string `json:"device"`
}

// requestSchema parses the body of [Issuer.CreateHandler]. It fixes the member
// types and documents them; the name, the scope catalog and the expiry are
// checked by [Issuer.Issue], which answers the stable [ErrInvalidRequest].
var requestSchema = validate.Struct(func(object *validate.StructBuilder[Request]) {
	object.Field("Name", func(name *validate.StringBuilder[string]) {
		name.Describe("Labels the token in the user's list. Required, at most 255 bytes.")
	})
	object.Field("Scopes", func(scopes *validate.SliceBuilder[string]) {
		scopes.Describe("The scopes to grant: a subset of the scope catalog that the user may hold. Empty grants none.")
	})
	object.Field("ExpiresAt", func(expires *validate.TimeBuilder) {
		expires.Describe("Optionally ends the token. It must be in the future and within the maximum lifetime when one is set; omitted means no expiry, or the maximum lifetime when set.")
	})
})

// deviceSchema parses the body of [Issuer.ExchangeHandler]; the device name is
// checked by [Issuer.IssueDevice].
var deviceSchema = validate.Struct(func(object *validate.StructBuilder[deviceBody]) {
	object.Field("Device", func(device *validate.StringBuilder[string]) {
		device.Describe("The device's name, which labels the token. Required, at most 255 bytes.")
	})
})

// idParameter describes the {id} path parameter of a token.
const idParameter = `{"type":"string","description":"The token's ID, as listed by the tokens route."}`

// ListOperation describes [Issuer.ListHandler]. It needs a browser session (a
// token cannot list tokens) and answers 200 with the user's tokens, newest
// first.
//
// Example:
//
//	web.Get("/tokens", issuer.ListHandler(), apitoken.ListOperation)
func ListOperation(operation *router.OperationBuilder) {
	operation.Summary("List API tokens")
	operation.Description("Lists the logged-in user's tokens, newest first, without their secrets. " +
		"Needs a browser session: a token cannot list tokens.")
	operation.Tags("tokens")
	operation.JSONResponse(http.StatusOK, validate.Infer[[]Token]())
	operation.Response(http.StatusOK, "The user's tokens")
	web.Problems(operation, web.SessionProblems)
}

// CreateOperation describes [Issuer.CreateHandler]. It needs a browser session
// and a fresh login and answers 201 with the token and its secret, which is
// shown once. An invalid request is 422 [ErrInvalidRequest] and a scope the
// user may not hold 403 [ErrScopeNotGranted]; an unverified email is 403.
//
// Example:
//
//	web.Post("/tokens", issuer.CreateHandler(guard), apitoken.CreateOperation)
func CreateOperation(operation *router.OperationBuilder) {
	operation.Summary("Create a personal API token")
	operation.Description("Creates a named, scoped token for the logged-in user. The secret is shown once, in the " +
		"response. Needs a browser session and a fresh login, so a hijacked old session cannot mint a long-lived credential.")
	operation.Tags("tokens")
	web.JSONBody(operation, requestSchema)
	operation.JSONResponse(http.StatusCreated, validate.Infer[secretBody]())
	operation.Response(http.StatusCreated, "Token created; the secret is shown once")
	web.Problems(operation, web.BodyProblems, web.FreshProblems, web.VerifiedProblems,
		[]problem.Details{ErrInvalidRequest, ErrScopeNotGranted})
}

// DeleteOperation describes [Issuer.DeleteHandler]. It needs a browser session
// and answers 204 when the user's token was revoked, or 404 [ErrNotFound] when
// it does not exist or belongs to someone else.
//
// Example:
//
//	web.Delete("/tokens/{id}", issuer.DeleteHandler(), apitoken.DeleteOperation)
func DeleteOperation(operation *router.OperationBuilder) {
	operation.Summary("Revoke an API token")
	operation.Description("Revokes one of the logged-in user's tokens. A token that does not exist, or belongs to " +
		"someone else, is not found. Needs a browser session.")
	operation.Tags("tokens")
	operation.Parameter("id", "path", router.RawSchema(idParameter))
	operation.Response(http.StatusNoContent, "Token revoked")
	web.Problems(operation, web.SessionProblems, []problem.Details{ErrNotFound})
}

// ExchangeOperation describes [Issuer.ExchangeHandler]. It trades a browser
// login at most [ExchangeWindow] old for a device token (201 with the token and
// its secret) and ends that login, so the response clears the session cookie. A
// stale session is 403 and an unverified email 403.
//
// Example:
//
//	web.Post("/device-tokens", issuer.ExchangeHandler(guard), apitoken.ExchangeOperation)
func ExchangeOperation(operation *router.OperationBuilder) {
	operation.Summary("Exchange a login for a device token")
	operation.Description("Trades the browser-session login for a device token and ends that login: the device keeps " +
		"the token and no cookie. Needs a browser session whose login is at most five minutes old; a device logs in " +
		"through the ordinary routes and exchanges right away. The token carries the scopes the user may hold now. " +
		"The secret is shown once.")
	operation.Tags("tokens")
	web.JSONBody(operation, deviceSchema)
	operation.JSONResponse(http.StatusCreated, validate.Infer[secretBody]())
	operation.Response(http.StatusCreated, "Device token issued; the secret is shown once")
	web.Cookie(operation, http.StatusCreated)
	web.Problems(operation, web.BodyProblems, web.SessionProblems, web.VerifiedProblems,
		[]problem.Details{auth.ErrNoStepUpMethod, ErrInvalidRequest})
}

// DeleteCurrentOperation describes [Issuer.DeleteCurrentHandler], a device's
// logout. It needs a bearer token of the issuer, not a browser session, and
// answers 204 after revoking the presented token; any other credential is 401.
//
// Example:
//
//	api.Delete("/device-tokens/current", issuer.DeleteCurrentHandler(), apitoken.DeleteCurrentOperation)
func DeleteCurrentOperation(operation *router.OperationBuilder) {
	operation.Summary("Revoke the presented device token")
	operation.Description("Revokes the token the request presented, which is how a device logs out. " +
		"Accepts any token of this issuer as a bearer credential; a browser session or another credential is unauthenticated.")
	operation.Tags("tokens")
	operation.Response(http.StatusNoContent, "Token revoked")
	web.Problems(operation, web.SessionProblems)
}
