package auth

import (
	"net/http"
	"strings"

	"arkoren.dev/core/http/router"
	"arkoren.dev/core/http/session"
	"arkoren.dev/foundation/problem"
)

// sessionCookieSchema describes the Set-Cookie header the session middleware
// adds when a handler changes the session.
const sessionCookieSchema = `{"type":"string","description":"The session cookie, re-issued with a new session ID. Its name and attributes belong to the session middleware."}`

// credentialBodySchema describes the body shared by [Guard.ChallengeHandler]
// and [Guard.ConfirmHandler]: "method" selects a registered credential and the
// remaining members are that credential's own input.
const credentialBodySchema = `{
  "type":"object","required":["method"],"additionalProperties":true,
  "properties":{"method":{"type":"string","description":"The credential method, for example \"otp\"."}}
}`

// problems declares every problem as a response of operation. Problems that
// share a status are described together, so one status never hides another's
// identity: the description lists each title with its stable type.
func problems(operation *router.OperationBuilder, all ...problem.Details) {
	titles := map[int][]string{}
	order := []int{}

	for _, details := range all {
		operation.Problem(details)

		status := details.Status()

		if status == 0 {
			status = http.StatusInternalServerError
		}

		if _, seen := titles[status]; !seen {
			order = append(order, status)
		}

		titles[status] = append(titles[status], details.Title()+" ("+details.Type()+")")
	}

	for _, status := range order {
		operation.Response(status, strings.Join(titles[status], "; "))
	}
}

// LogoutOperation describes [Guard.LogoutHandler] on the route chosen by the
// caller. It documents the 204 answer, the 303 answer that
// [LogoutBuilder.Redirect] selects, and the re-issued session cookie. Logging
// out an anonymous session also succeeds, so no 401 is declared. The annotation
// changes no handler behavior and installs no middleware.
//
// Example:
//
//	routes.Post("/logout", guard.LogoutHandler(), auth.LogoutOperation)
func LogoutOperation(operation *router.OperationBuilder) {
	operation.Summary("Log out")
	operation.Description("Ends the logged-in session, its login record and any pending login, and regenerates " +
		"the session ID. Logging out a session without a logged-in user also succeeds. " +
		"Answers 204, or 303 to the configured redirect target when the handler was built with one. " +
		"Register it for POST only, behind CSRF protection.")
	operation.Response(http.StatusNoContent, "Logged out")
	operation.ResponseHeader(http.StatusNoContent, "Set-Cookie", router.RawSchema(sessionCookieSchema))
	operation.Response(http.StatusSeeOther, "Logged out; redirected to the configured target")
	operation.ResponseHeader(http.StatusSeeOther, "Location", router.RawSchema(`{"type":"string"}`))
	operation.ResponseHeader(http.StatusSeeOther, "Set-Cookie", router.RawSchema(sessionCookieSchema))
	problems(operation, session.ErrNoSession)
}

// PendingOperation describes [Guard.PendingHandler]. The 200 body lists the
// second-factor methods of the parked login without naming its subject, and a
// request without a pending login, or one locked out, answers 401. It reads
// state only and spends no attempt.
//
// Example:
//
//	routes.Get("/login/pending", guard.PendingHandler(), auth.PendingOperation)
func PendingOperation(operation *router.OperationBuilder) {
	operation.Summary("Pending second-factor login")
	operation.Description("Lists the second-factor methods a client with a parked login can use. " +
		"It needs the session cookie of the parked login, not an identity, and spends no attempt. " +
		"A request without a pending login, a stale login, or a user locked out of the second factor is 401.")
	operation.JSONResponse(http.StatusOK, router.RawSchema(`{
  "type":"object","required":["methods"],
  "properties":{"methods":{"type":"array","items":{"type":"string"}}}
}`))
	problems(operation, ErrUnauthenticated)
}

// ChallengeOperation describes [Guard.ChallengeHandler], which finishes a
// parked login with one second-factor credential. The body names the method
// and carries that method's input; success is 204 with a re-issued session
// cookie. Problems follow the handler's order: no pending login or used-up
// attempts (401), a body that is not a JSON object (400), an unknown method
// (422), and a wrong code or a method the user is not enrolled in (403).
//
// Example:
//
//	routes.Post("/login/challenge", guard.ChallengeHandler(), auth.ChallengeOperation)
func ChallengeOperation(operation *router.OperationBuilder) {
	operation.Summary("Finish a second-factor login")
	operation.Description("Verifies one second-factor credential for the parked login. The body's \"method\" " +
		"selects a method registered with GuardBuilder.SecondFactor and the other members are that method's input. " +
		"Every attempt counts against a limit shared across methods and sessions; a wrong code rotates the session ID " +
		"and the last attempt discards the pending login. Success logs the user in and answers 204.")
	operation.JSONBody(router.RawSchema(credentialBodySchema)).BodyRequired(true)
	operation.Response(http.StatusNoContent, "Logged in")
	operation.ResponseHeader(http.StatusNoContent, "Set-Cookie", router.RawSchema(sessionCookieSchema))
	problems(operation, problem.BadRequest(), ErrUnauthenticated, ErrInvalidCredential, ErrUnknownMethod)
}

// ConfirmOperation describes [Guard.ConfirmHandler], which lets a logged-in
// session prove itself again with a step-up credential. It needs a browser
// session identity; a bearer token is always 401. Success is 204 with a
// re-issued session cookie, and a wrong code, a method the user is not enrolled
// in, or a lockout is 403.
//
// Example:
//
//	routes.Post("/confirm", guard.ConfirmHandler(), auth.ConfirmOperation)
func ConfirmOperation(operation *router.OperationBuilder) {
	operation.Summary("Confirm a login")
	operation.Description("Re-proves the logged-in user with a method registered with GuardBuilder.StepUp, " +
		"which makes the session fresh. It needs a browser session identity; a bearer token never confirms. " +
		"The body's \"method\" selects the credential and the other members are its input. " +
		"A locked-out user is answered like a wrong code.")
	operation.JSONBody(router.RawSchema(credentialBodySchema)).BodyRequired(true)
	operation.Response(http.StatusNoContent, "Confirmed")
	operation.ResponseHeader(http.StatusNoContent, "Set-Cookie", router.RawSchema(sessionCookieSchema))
	problems(operation, problem.BadRequest(), ErrUnauthenticated, ErrInvalidCredential, ErrUnknownMethod)
}
