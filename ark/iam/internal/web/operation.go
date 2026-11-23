package web

import (
	"net/http"
	"slices"
	"strings"

	"arkoren.dev/core/auth"
	"arkoren.dev/core/http/middleware/contenttype"
	"arkoren.dev/core/http/request"
	"arkoren.dev/core/http/router"
	"arkoren.dev/foundation/problem"
)

// Problem groups shared by the operations of the module. A handler's operation
// passes every group it needs to one [Problems] call, so statuses that several
// groups share (403, for example) are described together.
var (
	// BodyProblems lists the failures of every handler wrapped in [RequireJSON] that
	// reads a body with [Parse]: another media type (415), malformed JSON (400),
	// an oversized body (413) and a body that violates its schema (422).
	BodyProblems = []problem.Details{contenttype.ErrUnsupportedMediaType, request.ErrInvalidJSON, request.ErrBodyTooLarge, request.ErrValidationFailed}

	// ContentProblems lists the one failure of a handler that is wrapped in
	// [RequireJSON] but reads no body: a POST or PUT whose Content-Type is not
	// application/json is 415 even with an empty body.
	ContentProblems = []problem.Details{contenttype.ErrUnsupportedMediaType}

	// SessionProblems lists the failure of a handler that needs a browser session: an
	// anonymous request, or one authenticated by a bearer token, is 401.
	SessionProblems = []problem.Details{auth.ErrUnauthenticated}

	// FreshProblems lists the failures of a handler that needs a browser session and
	// a fresh login ([Fresh]): the 401 of [SessionProblems] and the two 403 answers of
	// the guard, a step-up that the client can confirm and a session too old
	// for a user with no method to confirm it.
	FreshProblems = []problem.Details{auth.ErrUnauthenticated, auth.ErrStepUpRequired, auth.ErrNoStepUpMethod}

	// VerifiedProblems lists the 403 of a handler that gates on a verified email.
	VerifiedProblems = []problem.Details{ErrEmailUnverified}
)

// sessionCookieSchema describes the Set-Cookie header the session middleware
// adds when a handler changes the session.
const sessionCookieSchema = `{"type":"string","description":"The session cookie, re-issued with a new session ID. Its name and attributes belong to the session middleware."}`

// Problems declares the problems of all groups as responses of operation. The
// groups are merged by status: the response description of a status lists each
// problem's title with its stable type, so one status never hides another's
// identity, and the media types come from [router.OperationBuilder.Problem].
// Duplicates across groups are described once.
func Problems(operation *router.OperationBuilder, groups ...[]problem.Details) {
	titles := map[int][]string{}
	order := []int{}

	for _, details := range slices.Concat(groups...) {
		operation.Problem(details)

		status := details.Status()

		if status == 0 {
			status = http.StatusInternalServerError
		}

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

// Cookie declares that a response at status carries the re-issued session
// cookie, which every handler that logs in, confirms or ends a session sets
// through the session middleware.
func Cookie(operation *router.OperationBuilder, status int) {
	operation.ResponseHeader(status, "Set-Cookie", router.RawSchema(sessionCookieSchema))
}

// JSONBody declares a required application/json request body described by
// schema, which should be the very schema the handler parses it with.
func JSONBody(operation *router.OperationBuilder, schema router.Schema) {
	operation.JSONBody(schema).BodyRequired(true)
}
