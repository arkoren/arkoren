package federation

import (
	"net/http"

	"arkoren.dev/core/http/router"
	"arkoren.dev/foundation/problem"
	"arkoren.dev/foundation/validate"

	"arkoren.dev/ark/iam/internal/web"
)

// ListOperation describes [Links.ListHandler]. It needs a browser session and
// answers 200 with the user's connections, including the claims their provider
// resolvers kept.
//
// Example:
//
//	web.Get("/connections", links.ListHandler(), federation.ListOperation)
func ListOperation(operation *router.OperationBuilder) {
	operation.Summary("List connected accounts")
	operation.Description("Lists the logged-in user's connections to external identity providers. " +
		"A connection shows the claims its provider's resolver kept, which are unverified suggestions for prefilling a form. " +
		"Needs a browser session.")
	operation.Tags("connections")
	operation.JSONResponse(http.StatusOK, validate.Infer[[]Link]())
	operation.Response(http.StatusOK, "The user's connections")
	web.Problems(operation, web.SessionProblems)
}

// ConnectOperation describes [Links.ConnectHandler]. It needs a browser session
// and a fresh login, records a one-callback connect intent in the session and
// answers 303 to the provider's login route. A provider name the facade does not
// know is 404 [ErrUnknownProvider] and an unverified email is 403.
//
// Example:
//
//	web.Post("/connections/{provider}", links.ConnectHandler(guard, targets), federation.ConnectOperation)
func ConnectOperation(operation *router.OperationBuilder) {
	operation.Summary("Connect an account")
	operation.Description("Starts connecting an external identity provider to the logged-in user: it records a connect " +
		"intent in the session, which lasts a few minutes and serves one callback, and redirects to the provider's login " +
		"route. The callback then links the identity to this account. It is a POST so that a cross-site GET to the login " +
		"route alone can never link an identity to a victim. The path segment is the provider's name, as in its login path. " +
		"Needs a browser session and a fresh login.")
	operation.Tags("connections")
	operation.Response(http.StatusSeeOther, "Redirected to the provider's login route")
	operation.ResponseHeader(http.StatusSeeOther, "Location", router.RawSchema(`{"type":"string"}`))
	web.Cookie(operation, http.StatusSeeOther)
	web.Problems(operation, web.ContentProblems, web.FreshProblems, web.VerifiedProblems,
		[]problem.Details{ErrUnknownProvider})
}

// DeleteOperation describes [Links.DeleteHandler]. It needs a browser session
// and a fresh login and answers 204 once the connection is removed. Another
// user's connection is 404 [ErrNotFound] and the user's last way to log in is
// 409 [ErrLastLink].
//
// Example:
//
//	web.Delete("/connections/{id}", links.DeleteHandler(guard), federation.DeleteOperation)
func DeleteOperation(operation *router.OperationBuilder) {
	operation.Summary("Remove a connected account")
	operation.Description("Removes one of the logged-in user's connections. The user's last way to log in cannot be removed. " +
		"Needs a browser session and a fresh login.")
	operation.Tags("connections")
	operation.Parameter("id", "path", router.RawSchema(`{"type":"string","description":"The connection's ID, as listed by the connections route."}`))
	operation.Response(http.StatusNoContent, "Connection removed")
	web.Problems(operation, web.FreshProblems, []problem.Details{ErrNotFound, ErrLastLink})
}
