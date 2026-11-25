package account

import (
	"net/http"

	"arkoren.dev/core/http/router"
	"arkoren.dev/foundation/problem"
	"arkoren.dev/foundation/validate"

	"arkoren.dev/ark/iam/internal/web"
)

// tokenBody is the JSON body of [Directory.VerifyHandler].
type tokenBody struct {
	Token string `json:"token"`
}

// tokenSchema parses the body of [Directory.VerifyHandler]. It fixes the member
// type and documents it; an absent or invalid token is the handler's own
// [ErrInvalidToken].
var tokenSchema = validate.Struct(func(object *validate.StructBuilder[tokenBody]) {
	object.Field("Token", func(token *validate.StringBuilder[string]) {
		token.Describe("The signed link value from the verification email. Required.")
	})
})

// VerifyOperation describes [Directory.VerifyHandler]. It needs a browser
// session of the user the link belongs to and answers 204. Without a session the
// answer is 401, a session of another user is 403 [ErrWrongAccount], and a bad,
// expired or used link is 422 [ErrInvalidToken].
//
// Example:
//
//	web.Post("/email/verify", directory.VerifyHandler(), account.VerifyOperation)
func VerifyOperation(operation *router.OperationBuilder) {
	operation.Summary("Verify an email address")
	operation.Description("Marks the logged-in user's email as verified from the signed link of a verification email. " +
		"Needs a browser session of the user the link belongs to, so someone else's click cannot verify an account they " +
		"do not hold. A link is single use: a second click is invalid.")
	operation.Tags("email")
	web.JSONBody(operation, tokenSchema)
	operation.Response(http.StatusNoContent, "Email verified")
	web.Problems(operation, web.BodyProblems, web.SessionProblems,
		[]problem.Details{ErrWrongAccount, ErrInvalidToken})
}

// ResendOperation describes [Directory.ResendHandler]. It needs a browser
// session and answers 202 whether or not anything will be sent. Requests are
// throttled per user and per client address (429 with Retry-After), and a
// dispatch failure is 500.
//
// Example:
//
//	web.Post("/email/verify/resend", directory.ResendHandler(), account.ResendOperation)
func ResendOperation(operation *router.OperationBuilder) {
	operation.Summary("Resend the verification email")
	operation.Description("Asks for a new verification email for the logged-in user. Answers 202 whether or not " +
		"anything will be sent, because the user may already be verified and the worker decides. " +
		"Needs a browser session. Requests are throttled per user and per client address.")
	operation.Tags("email")
	operation.Response(http.StatusAccepted, "Request accepted")
	operation.ResponseHeader(http.StatusTooManyRequests, "Retry-After",
		router.RawSchema(`{"type":"integer","description":"Seconds until another attempt is counted."}`))
	web.Problems(operation, web.ContentProblems, web.SessionProblems,
		[]problem.Details{ErrThrottled, problem.InternalServerError()})
}
