package problem

import "net/http"

// badRequestProblem stores the canonical 400 identity, including about:blank
// and the default status title, before any occurrence is derived or rendered.
var badRequestProblem = New(func(details *Builder) {
	details.Status(http.StatusBadRequest)
})

// unauthorizedProblem stores the canonical 401 identity without a challenge.
// Unauthorized derives the caller's challenge without changing this template.
var unauthorizedProblem = New(func(details *Builder) {
	details.Status(http.StatusUnauthorized)
})

// forbiddenProblem stores the canonical 403 identity with about:blank and the
// default status title so matching does not depend on rendering defaults.
var forbiddenProblem = New(func(details *Builder) {
	details.Status(http.StatusForbidden)
})

// notFoundProblem stores the canonical 404 identity with about:blank and the
// default status title, shared by independently constructed equivalent problems.
var notFoundProblem = New(func(details *Builder) {
	details.Status(http.StatusNotFound)
})

// conflictProblem stores the canonical 409 identity with about:blank and the
// default status title; occurrences inherit these stored identity members.
var conflictProblem = New(func(details *Builder) {
	details.Status(http.StatusConflict)
})

// unprocessableContentProblem stores the canonical 422 identity and about:blank.
// Its explicit title replaces the legacy HTTP phrase Unprocessable Entity.
var unprocessableContentProblem = New(func(details *Builder) {
	details.Title("Unprocessable Content")
	details.Status(http.StatusUnprocessableEntity)
})

// internalServerErrorProblem stores New's canonical defaults: about:blank,
// status 500, and Internal Server Error, without occurrence-specific metadata.
var internalServerErrorProblem = New()

// BadRequest returns a copy of the 400 Bad Request problem template.
// Its stored type is about:blank, matching an equivalent [New] value.
//
// Example:
//
//	failure := problem.BadRequest()
//	_ = failure.Status() // 400
func BadRequest() Details {
	return badRequestProblem
}

// Unauthorized returns a 401 Unauthorized problem with the supplied HTTP
// authentication challenge. Callers must provide a non-empty, syntactically
// valid challenge; it is preserved without parsing or rewriting.
// The returned value owns its header slices; the shared template is unchanged.
//
// Example:
//
//	failure := problem.Unauthorized(`Basic realm="api"`)
//	_ = failure.Status() // 401
func Unauthorized(challenge string) Details {
	return unauthorizedProblem.Derive(func(details *Builder) {
		details.Header("WWW-Authenticate", challenge)
	})
}

// Forbidden returns a copy of the 403 Forbidden problem template.
// Derive an occurrence to add client-safe details without changing the template.
//
// Example:
//
//	failure := problem.Forbidden()
//	_ = failure.Status() // 403
func Forbidden() Details {
	return forbiddenProblem
}

// NotFound returns a copy of the 404 Not Found problem template.
// Its title is retained when deriving an occurrence with another status.
//
// Example:
//
//	failure := problem.NotFound()
//	_ = failure.Status() // 404
func NotFound() Details {
	return notFoundProblem
}

// Conflict returns a copy of the 409 Conflict problem template.
//
// Example:
//
//	return problem.Conflict().Derive(func(details *problem.Builder) {
//		details.Detail("The group would have no members.")
//	})
func Conflict() Details {
	return conflictProblem
}

// UnprocessableContent returns a copy of the 422 Unprocessable Content
// problem template.
// Its explicit title is Unprocessable Content, rather than the legacy HTTP
// status phrase Unprocessable Entity.
//
// Example:
//
//	failure := problem.UnprocessableContent()
//	_ = failure.Title() // "Unprocessable Content"
func UnprocessableContent() Details {
	return unprocessableContentProblem
}

// InternalServerError returns a copy of the 500 Internal Server Error problem
// template.
// Its stored type is about:blank, matching an equivalent [New] value.
//
// Example:
//
//	failure := problem.InternalServerError()
//	_ = failure.Status() // 500
func InternalServerError() Details {
	return internalServerErrorProblem
}
