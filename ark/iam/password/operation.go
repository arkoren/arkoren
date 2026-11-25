package password

import (
	"net/http"

	"arkoren.dev/core/auth"
	"arkoren.dev/core/http/router"
	"arkoren.dev/foundation/problem"
	"arkoren.dev/foundation/validate"

	"arkoren.dev/ark/iam/internal/web"
)

// credentialsBody is the JSON body of [Verifier.LoginHandler] and
// [Verifier.RegisterHandler]. The identifier is "email" in the usual case;
// "identifier" is accepted for applications whose users log in with something
// else.
type credentialsBody struct {
	Email      string `json:"email"`
	Identifier string `json:"identifier"`
	Password   string `json:"password"`
}

// changeBody is the JSON body of [Verifier.ChangeHandler].
type changeBody struct {
	Current  string `json:"current_password"`
	Password string `json:"password"`
}

// forgotBody is the JSON body of [Verifier.ForgotHandler].
type forgotBody struct {
	Email      string `json:"email"`
	Identifier string `json:"identifier"`
}

// resetBody is the JSON body of [Verifier.ResetHandler].
type resetBody struct {
	Token    string `json:"token"`
	Password string `json:"password"`
}

// credentialsSchema parses the login and registration bodies. It only fixes the
// member types and documents them: whether the identifier and password are
// present is the handler's own answer, [ErrInvalidCredentials], which must stay
// the same for every cause.
var credentialsSchema = validate.Struct(func(object *validate.StructBuilder[credentialsBody]) {
	object.Field("Email", func(email *validate.StringBuilder[string]) {
		email.Describe("The user's email address. Send either email or identifier.")
	})
	object.Field("Identifier", func(identifier *validate.StringBuilder[string]) {
		identifier.Describe("The login identifier for applications whose users log in with something other than an email. Used when email is empty.")
	})
	object.Field("Password", func(password *validate.StringBuilder[string]) {
		password.Describe("The password. Required.")
	})
})

// changeSchema parses the password change body. Presence and policy are checked
// by the handler, which returns the stable problems of this package.
var changeSchema = validate.Struct(func(object *validate.StructBuilder[changeBody]) {
	object.Field("Current", func(current *validate.StringBuilder[string]) {
		current.Describe("The current password. Required unless the user has no password yet.")
	})
	object.Field("Password", func(password *validate.StringBuilder[string]) {
		password.Describe("The new password. Required; it must satisfy the password policy.")
	})
})

// forgotSchema parses the forgot-password body.
var forgotSchema = validate.Struct(func(object *validate.StructBuilder[forgotBody]) {
	object.Field("Email", func(email *validate.StringBuilder[string]) {
		email.Describe("The email address of the account, exactly as typed. Send either email or identifier.")
	})
	object.Field("Identifier", func(identifier *validate.StringBuilder[string]) {
		identifier.Describe("The login identifier, exactly as typed. Used when email is empty.")
	})
})

// resetSchema parses the reset-password body.
var resetSchema = validate.Struct(func(object *validate.StructBuilder[resetBody]) {
	object.Field("Token", func(token *validate.StringBuilder[string]) {
		token.Describe("The signed reset link value from the reset email. Required.")
	})
	object.Field("Password", func(password *validate.StringBuilder[string]) {
		password.Describe("The new password. Required; it must satisfy the password policy.")
	})
})

// throttled describes the 429 answer of the throttled handlers: the problem and
// the Retry-After header that tells the client when to try again.
func throttled(operation *router.OperationBuilder) {
	operation.ResponseHeader(http.StatusTooManyRequests, "Retry-After",
		router.RawSchema(`{"type":"integer","description":"Seconds until another attempt is counted."}`))
}

// LoginOperation describes [Verifier.LoginHandler]. The body takes email (or
// identifier) and password, and a failure is the same 422
// [ErrInvalidCredentials] for every cause. A complete login is 204; a user with
// a second factor is parked and answered 202 with the methods to use at the
// challenge route. Both set the session cookie. Failed attempts are throttled
// (429 with Retry-After). The annotation changes no handler behavior.
//
// Example:
//
//	web.Post("/login", verifier.LoginHandler(guard), password.LoginOperation)
func LoginOperation(operation *router.OperationBuilder) {
	operation.Summary("Log in with a password")
	operation.Description("Authenticates the user and starts a session. Answers 204 when the login is complete, " +
		"or 202 with the second-factor methods the client can use when the user has to answer a challenge. " +
		"Every failure answers the same 422 problem, so the response never says whether the identifier exists. " +
		"Failed attempts count against limits per identifier and client address, per identifier, and per client address.")
	operation.Tags("authentication")
	web.JSONBody(operation, credentialsSchema)
	operation.Response(http.StatusNoContent, "Logged in")
	web.Cookie(operation, http.StatusNoContent)
	operation.JSONResponse(http.StatusAccepted, validate.Infer[auth.SecondFactorChallenge]())
	operation.Response(http.StatusAccepted, "A second factor is required")
	web.Cookie(operation, http.StatusAccepted)
	web.Problems(operation, web.BodyProblems, []problem.Details{ErrInvalidCredentials, ErrThrottled})
	throttled(operation)
}

// RegisterOperation describes [Verifier.RegisterHandler]. It always answers 202
// with no body and never logs the user in, so the answer does not say whether the
// identifier was free. Registrations are throttled (429 with Retry-After), and a
// password that fails the policy is 422 [ErrInvalidPassword].
//
// Example:
//
//	web.Post("/register", verifier.RegisterHandler(), password.RegisterOperation)
func RegisterOperation(operation *router.OperationBuilder) {
	operation.Summary("Register a user")
	operation.Description("Creates a user with a password. Always answers 202 with no body and never logs the user in, " +
		"so the answer does not reveal whether the identifier was free: the owner of a taken identifier is notified instead. " +
		"A new user is unverified until the email is verified. Registrations are throttled per identifier and per client address.")
	operation.Tags("authentication")
	web.JSONBody(operation, credentialsSchema)
	operation.Response(http.StatusAccepted, "Registration accepted")
	web.Problems(operation, web.BodyProblems, []problem.Details{ErrInvalidPassword, ErrThrottled})
	throttled(operation)
}

// ChangeOperation describes [Verifier.ChangeHandler]. It needs a browser session
// and a fresh login, answers 204 and re-issues the session cookie because the
// other logins of the user end. A wrong current password is 422
// [ErrInvalidCredentials], a password that fails the policy 422
// [ErrInvalidPassword] and too many attempts 429 [ErrThrottled].
//
// Example:
//
//	web.Put("/password", verifier.ChangeHandler(guard), password.ChangeOperation)
func ChangeOperation(operation *router.OperationBuilder) {
	operation.Summary("Change the password")
	operation.Description("Sets a new password for the logged-in user. Needs a browser session and a fresh login; " +
		"current_password is required unless the user has no password yet. Ends the user's other logins and keeps this one. " +
		"Reset links sent before stop working. A failing change hook answers 500 after the password has changed; repeat the request.")
	operation.Tags("authentication")
	web.JSONBody(operation, changeSchema)
	operation.Response(http.StatusNoContent, "Password changed")
	web.Cookie(operation, http.StatusNoContent)
	web.Problems(operation, web.BodyProblems, web.FreshProblems,
		[]problem.Details{ErrInvalidPassword, ErrInvalidCredentials, ErrThrottled, problem.InternalServerError()})
	throttled(operation)
}

// ForgotOperation describes [Verifier.ForgotHandler]. It answers an empty 202
// whether or not the user exists, because the work is stored for a worker, and
// throttles requests per identifier as typed and per client address (429 with
// Retry-After).
//
// Example:
//
//	web.Post("/password/forgot", verifier.ForgotHandler(), password.ForgotOperation)
func ForgotOperation(operation *router.OperationBuilder) {
	operation.Summary("Request a password reset")
	operation.Description("Stores a request to email a reset link and answers an empty 202 whether or not the user exists. " +
		"The lookup, the signing and the email happen in the worker, and the email goes to the address stored with the user. " +
		"A dispatch failure answers 500 because the work was not stored; the client should try again.")
	operation.Tags("authentication")
	web.JSONBody(operation, forgotSchema)
	operation.Response(http.StatusAccepted, "Request accepted")
	web.Problems(operation, web.BodyProblems, []problem.Details{ErrThrottled, problem.InternalServerError()})
	throttled(operation)
}

// ResetOperation describes [Verifier.ResetHandler]. It answers 204, never logs
// the user in, and ends all the user's logins. An invalid, expired or used link
// is 422 [ErrInvalidToken] and a password that fails the policy 422
// [ErrInvalidPassword].
//
// Example:
//
//	web.Post("/password/reset", verifier.ResetHandler(guard), password.ResetOperation)
func ResetOperation(operation *router.OperationBuilder) {
	operation.Summary("Reset the password")
	operation.Description("Sets a new password from the signed link of a reset email. The link is single use and dies " +
		"with any change of the password. The user's sessions, pending logins and API tokens end, and the user is not " +
		"logged in: they log in again afterwards, with their second factor if they have one.")
	operation.Tags("authentication")
	web.JSONBody(operation, resetSchema)
	operation.Response(http.StatusNoContent, "Password reset")
	web.Problems(operation, web.BodyProblems, []problem.Details{ErrInvalidToken, ErrInvalidPassword})
}
