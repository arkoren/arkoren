package twofactor

import (
	"net/http"

	"arkoren.dev/core/http/router"
	"arkoren.dev/foundation/problem"
	"arkoren.dev/foundation/validate"

	"arkoren.dev/ark/iam/internal/web"
)

// codeSchema parses the body of [Factors.EnableHandler]. It fixes the member
// type and documents it; a missing or wrong code is the handler's own
// [ErrInvalidCode], which counts as an attempt.
var codeSchema = validate.Struct(func(object *validate.StructBuilder[codeBody]) {
	object.Field("Code", func(code *validate.StringBuilder[string]) {
		code.Describe("The current code of the pending authenticator. Required.")
	})
})

// SetupOperation describes [Factors.SetupHandler]. It needs a browser session
// and a fresh login, and answers 200 with the secret and otpauth URI of the
// pending enrollment, which are shown only to the enrolling user. An unverified
// email is 403 and an already enabled user 409 [ErrAlreadyEnabled]. The
// annotation changes no handler behavior.
//
// Example:
//
//	web.Post("/two-factor/setup", factors.SetupHandler(guard), twofactor.SetupOperation)
func SetupOperation(operation *router.OperationBuilder) {
	operation.Summary("Start two-factor enrollment")
	operation.Description("Starts enrollment of an authenticator for the logged-in user and returns its secret and " +
		"otpauth:// URI for a QR code. Calling it again before enabling replaces the pending secret. Needs a browser " +
		"session and a fresh login; a user with no step-up method is fresh right after any login. The response " +
		"contains the secret: show it only to the enrolling user and never log it.")
	operation.Tags("two-factor")
	operation.JSONResponse(http.StatusOK, validate.Infer[Enrollment]())
	operation.Response(http.StatusOK, "Enrollment started")
	web.Problems(operation, web.ContentProblems, web.FreshProblems, web.VerifiedProblems,
		[]problem.Details{ErrAlreadyEnabled})
}

// EnableOperation describes [Factors.EnableHandler]. The body carries the first
// code of the pending authenticator; the 200 body shows the recovery codes once.
// Every other login of the user ends and the session cookie is re-issued. A
// wrong code is 422 [ErrInvalidCode], too many are 429 [ErrThrottled] and a user
// who never ran setup gets 409 [ErrNotEnrolling].
//
// Example:
//
//	web.Post("/two-factor", factors.EnableHandler(guard), twofactor.EnableOperation)
func EnableOperation(operation *router.OperationBuilder) {
	operation.Summary("Enable two-factor authentication")
	operation.Description("Confirms the pending authenticator with its first code, enables two-factor and returns the " +
		"recovery codes, the only time they are shown. Every other login of the user ends, so a session opened before " +
		"the second factor existed cannot outlive it; this session records the code as a fresh factor. " +
		"Needs a browser session and a fresh login.")
	operation.Tags("two-factor")
	web.JSONBody(operation, codeSchema)
	operation.JSONResponse(http.StatusOK, validate.Infer[codesBody]())
	operation.Response(http.StatusOK, "Two-factor enabled; the recovery codes are shown once")
	web.Cookie(operation, http.StatusOK)
	web.Problems(operation, web.BodyProblems, web.FreshProblems, web.VerifiedProblems,
		[]problem.Details{ErrInvalidCode, ErrThrottled, ErrNotEnrolling})
}

// DisableOperation describes [Factors.DisableHandler]. It needs a browser
// session, a fresh login (or a recent recovery code) and a recent
// authenticator or recovery code: a password alone is 403 step-up required with
// the methods to confirm. A user without an authenticator gets 409
// [ErrNotEnabled]. Success is 204.
//
// Example:
//
//	web.Delete("/two-factor", factors.DisableHandler(guard), twofactor.DisableOperation)
func DisableOperation(operation *router.OperationBuilder) {
	operation.Summary("Disable two-factor authentication")
	operation.Description("Removes the user's authenticator, any pending enrollment and the recovery codes. " +
		"Needs a browser session and a fresh login, and in addition a recent authenticator or recovery code: " +
		"a password alone, however recent, is answered with a step-up problem listing the methods to confirm with. " +
		"A recovery code used within the window also counts as the fresh login, so a user who lost the device can disable it.")
	operation.Tags("two-factor")
	operation.Response(http.StatusNoContent, "Two-factor disabled")
	web.Problems(operation, web.FreshProblems, []problem.Details{ErrNotEnabled})
}

// RecoveryCodesOperation describes [Factors.RecoveryCodesHandler]. It replaces
// the recovery codes and answers 200 with the new codes, which stop the old
// ones. It follows the rules of [DisableOperation]: a browser session, a fresh
// login and a recent authenticator or recovery code, and 409 [ErrNotEnabled]
// for a user without an authenticator.
//
// Example:
//
//	web.Post("/two-factor/recovery-codes", factors.RecoveryCodesHandler(guard), twofactor.RecoveryCodesOperation)
func RecoveryCodesOperation(operation *router.OperationBuilder) {
	operation.Summary("Replace the recovery codes")
	operation.Description("Replaces the user's recovery codes and returns the new ones, the only time they are shown; " +
		"the old codes stop working. Needs a browser session, a fresh login and a recent authenticator or recovery code, " +
		"like disabling two-factor.")
	operation.Tags("two-factor")
	operation.JSONResponse(http.StatusOK, validate.Infer[codesBody]())
	operation.Response(http.StatusOK, "New recovery codes, shown once")
	web.Problems(operation, web.ContentProblems, web.FreshProblems, []problem.Details{ErrNotEnabled})
}
