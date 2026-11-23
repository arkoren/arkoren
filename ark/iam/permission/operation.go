package permission

import (
	"net/http"

	"arkoren.dev/core/auth/gate"
	"arkoren.dev/core/http/router"
	"arkoren.dev/foundation/problem"
	"arkoren.dev/foundation/validate"

	"arkoren.dev/ark/iam/internal/web"
)

// subjectParameter and nameParameter describe the path parameters of the
// management routes.
const (
	subjectParameter = `{"type":"string","description":"The subject of the user."}`
	nameParameter    = `{"type":"string","description":"The name of a permission or role the store defines."}`
)

// SelfOperation describes [Store.SelfHandler]. It reads the request's identity
// only, so a browser session and a bearer token both work; the 200 body lists
// the identity's roles and its scopes that name a defined permission. Without an
// identity the answer is 401.
//
// Example:
//
//	web.Get("/me/permissions", permissions.SelfHandler(), permission.SelfOperation)
func SelfOperation(operation *router.OperationBuilder) {
	operation.Summary("Own permissions")
	operation.Description("Lists the roles and permissions of the request's identity. It reads the identity, never the " +
		"table: a browser session shows what the guard loaded, and a token shows its effective scopes and no roles.")
	operation.Tags("permissions")
	operation.JSONResponse(http.StatusOK, validate.Infer[selfBody]())
	operation.Response(http.StatusOK, "The identity's roles and permissions")
	web.Problems(operation, web.SessionProblems)
}

// UserOperation describes [Store.UserHandler]. The caller needs a browser
// session whose identity holds the management permission (otherwise 403); an
// unknown subject is 404 [ErrUserNotFound]. The 200 body lists the user's direct
// assignments and every permission the user holds.
//
// Example:
//
//	web.Get("/users/{subject}/permissions", permissions.UserHandler(), permission.UserOperation)
func UserOperation(operation *router.OperationBuilder) {
	operation.Summary("Permissions of a user")
	operation.Description("Lists a user's directly assigned roles and permissions and every permission the user holds. " +
		"The caller needs a browser session (a token is refused) whose identity holds the management permission.")
	operation.Tags("permissions")
	operation.Parameter("subject", "path", router.RawSchema(subjectParameter))
	operation.JSONResponse(http.StatusOK, validate.Infer[userBody]())
	operation.Response(http.StatusOK, "The user's roles and permissions")
	web.Problems(operation, web.SessionProblems, []problem.Details{gate.ErrForbidden, ErrUserNotFound})
}

// manage describes the rules every management write shares, in the order the
// handler applies them. content is true for the PUT routes, which read no body
// but require application/json, and lastHolder is true for revoking a role,
// which can also hit the last holder of a role the store keeps.
func manage(operation *router.OperationBuilder, content, lastHolder bool) {
	operation.Tags("permissions")
	operation.Parameter("subject", "path", router.RawSchema(subjectParameter))
	operation.Parameter("name", "path", router.RawSchema(nameParameter))
	operation.Response(http.StatusNoContent, "Done; the write is idempotent")

	problems := []problem.Details{gate.ErrForbidden, ErrUnknownGrant, ErrNotGrantable, ErrUserNotFound}

	if lastHolder {
		problems = append(problems, ErrLastHolder)
	}

	if content {
		web.Problems(operation, web.ContentProblems, web.FreshProblems, problems)

		return
	}

	web.Problems(operation, web.FreshProblems, problems)
}

// GrantOperation describes [Store.GrantHandler]. Every management write needs
// a browser session, a fresh login and the management permission, refuses a name
// the store does not define (422), refuses to hand out more than the caller
// holds (403 [ErrNotGrantable]) and is idempotent.
//
// Example:
//
//	web.Put("/users/{subject}/permissions/{name}", permissions.GrantHandler(guard), permission.GrantOperation)
func GrantOperation(operation *router.OperationBuilder) {
	operation.Summary("Grant a permission")
	operation.Description("Grants a permission to a user. Needs a browser session, a fresh login and the management " +
		"permission, and the caller must hold the permission it grants. Granting what is held also answers 204. " +
		"The request needs Content-Type application/json although it has no body.")
	manage(operation, true, false)
}

// RevokeOperation describes [Store.RevokeHandler], with the rules of
// [GrantOperation].
//
// Example:
//
//	web.Delete("/users/{subject}/permissions/{name}", permissions.RevokeHandler(guard), permission.RevokeOperation)
func RevokeOperation(operation *router.OperationBuilder) {
	operation.Summary("Revoke a permission")
	operation.Description("Revokes a permission from a user. Needs a browser session, a fresh login and the management " +
		"permission, and the caller must hold the permission it revokes. Revoking what is not held also answers 204.")
	manage(operation, false, false)
}

// GrantRoleOperation describes [Store.GrantRoleHandler], with the rules of
// [GrantOperation]: for a role the caller must hold every permission of the role
// and every role it expands to.
//
// Example:
//
//	web.Put("/users/{subject}/roles/{name}", permissions.GrantRoleHandler(guard), permission.GrantRoleOperation)
func GrantRoleOperation(operation *router.OperationBuilder) {
	operation.Summary("Grant a role")
	operation.Description("Grants a role to a user. Needs a browser session, a fresh login and the management " +
		"permission, and the caller must hold every permission of the role and every role it expands to. " +
		"Granting a role already held also answers 204. The request needs Content-Type application/json although it has no body.")
	manage(operation, true, false)
}

// RevokeRoleOperation describes [Store.RevokeRoleHandler], with the rules of
// [GrantRoleOperation]. Revoking the last holder of a role kept by
// [Builder.KeepOne] is 409 [ErrLastHolder].
//
// Example:
//
//	web.Delete("/users/{subject}/roles/{name}", permissions.RevokeRoleHandler(guard), permission.RevokeRoleOperation)
func RevokeRoleOperation(operation *router.OperationBuilder) {
	operation.Summary("Revoke a role")
	operation.Description("Revokes a role from a user. Needs a browser session, a fresh login and the management " +
		"permission, and the caller must hold every permission of the role and every role it expands to. " +
		"Revoking the last holder of a role the store keeps is refused.")
	manage(operation, false, true)
}
