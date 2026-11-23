package permission

import (
	"context"
	"errors"
	"net/http"
	"slices"

	"arkoren.dev/core/auth"
	"arkoren.dev/core/auth/gate"
	"arkoren.dev/core/http/request"
	"arkoren.dev/core/http/response"
	"arkoren.dev/core/http/router"

	"arkoren.dev/ark/iam/internal/web"
)

// selfBody is the response of [Store.SelfHandler].
type selfBody struct {
	// Roles are the role names on the request's identity.
	Roles []string `json:"roles"`

	// Permissions are the identity's scopes that name a defined permission.
	Permissions []string `json:"permissions"`
}

// userBody is the response of [Store.UserHandler].
type userBody struct {
	// Roles are the directly assigned roles.
	Roles []string `json:"roles"`

	// Permissions are the directly granted permissions.
	Permissions []string `json:"permissions"`

	// Effective are all the permissions the user holds, from [Store.Load].
	Effective []string `json:"effective"`
}

// SelfHandler returns the handler of GET /me/permissions: 200 with
// {"roles": [...], "permissions": [...]} for the request's identity. It reads
// the identity only, never the table: roles are [auth.Identity.Roles] and
// permissions the identity's scopes that the store defines. A browser session
// therefore shows what the guard loaded through [Store.Load], and a token shows
// its effective scopes and no roles. Without an identity it answers 401
// ([auth.ErrUnauthenticated]).
//
// Example:
//
//	web.Get("/me/permissions", permissions.SelfHandler(), permission.SelfOperation)
func (store *Store) SelfHandler() router.Handler {
	return func(w http.ResponseWriter, r *http.Request) error {
		identity, err := request.Identity(r)
		if err != nil {
			return err
		}
		scopes := slices.DeleteFunc(identity.Scopes(), func(name string) bool { return !store.permissions[name] })

		return response.JSON(w, http.StatusOK, selfBody{Roles: listed(identity.Roles()), Permissions: listed(scopes)})
	}
}

// UserHandler returns the handler of GET /users/{subject}/permissions: 200 with
// {"roles": [...], "permissions": [...], "effective": [...]}, the direct
// assignments ([Store.Assignments]) and every permission the user holds
// ([Store.Load]). The caller needs a browser session (a token is refused with
// 401) whose identity holds the [Builder.Management] permission (otherwise
// gate.ErrForbidden, 403). An unknown subject is [ErrUserNotFound] (404). It
// panics at composition without [Builder.Management].
//
// Example:
//
//	web.Get("/users/{subject}/permissions", permissions.UserHandler(), permission.UserOperation)
func (store *Store) UserHandler() router.Handler {
	store.needManagement("UserHandler")

	return func(w http.ResponseWriter, r *http.Request) error {
		if _, err := store.manager(nil, r); err != nil {
			return err
		}
		subject := r.PathValue("subject")
		if err := store.exists(r.Context(), subject); err != nil {
			return err
		}
		// One uncached read feeds all three lists, so they always agree.
		grants, err := store.read(r.Context(), subject)
		if err != nil {
			return err
		}
		assigned := store.assignments(grants)
		_, effective := store.expand(grants)

		return response.JSON(w, http.StatusOK, userBody{
			Roles:       assigned.Roles,
			Permissions: assigned.Permissions,
			Effective:   listed(effective),
		})
	}
}

// GrantHandler returns the handler of PUT /users/{subject}/permissions/{name}:
// 204 after [Store.Grant]. See [Store.GrantRoleHandler] for the rules every
// management write follows.
//
// Example:
//
//	web.Put("/users/{subject}/permissions/{name}", permissions.GrantHandler(guard), permission.GrantOperation)
func (store *Store) GrantHandler(guard *auth.Guard) router.Handler {
	store.needManagement("GrantHandler")

	return web.RequireJSON(store.change(guard, KindPermission, true))
}

// RevokeHandler returns the handler of DELETE /users/{subject}/permissions/{name}:
// 204 after [Store.Revoke]. See [Store.GrantRoleHandler] for the rules every
// management write follows.
//
// Example:
//
//	web.Delete("/users/{subject}/permissions/{name}", permissions.RevokeHandler(guard), permission.RevokeOperation)
func (store *Store) RevokeHandler(guard *auth.Guard) router.Handler {
	store.needManagement("RevokeHandler")

	return store.change(guard, KindPermission, false)
}

// GrantRoleHandler returns the handler of PUT /users/{subject}/roles/{name}: 204
// after [Store.GrantRole]. Every management write follows the same rules, in
// this order:
//
//   - the caller needs a browser session (a token is refused with 401) and a
//     fresh login ([auth.Guard.Fresh]), so a hijacked old session cannot change
//     authority; PUT also needs Content-Type application/json, which keeps
//     cross-site forms out, but the application still owns CSRF protection;
//   - the caller's identity holds the [Builder.Management] permission, otherwise
//     gate.ErrForbidden (403);
//   - the name is defined by the store, otherwise [ErrUnknownGrant] (422);
//   - the caller holds the permission, or, for a role, every permission of the
//     role and every role it expands to, otherwise [ErrNotGrantable] (403), so
//     no one can hand out more than they have, or take away what they could not
//     give;
//   - the subject is a user, otherwise [ErrUserNotFound] (404);
//   - revoking the last holder of a [Builder.KeepOne] role is [ErrLastHolder]
//     (409).
//
// Writes are idempotent: granting what is held or revoking what is not also
// answers 204.
//
// Example:
//
//	web.Put("/users/{subject}/roles/{name}", permissions.GrantRoleHandler(guard), permission.GrantRoleOperation)
func (store *Store) GrantRoleHandler(guard *auth.Guard) router.Handler {
	store.needManagement("GrantRoleHandler")

	return web.RequireJSON(store.change(guard, KindRole, true))
}

// RevokeRoleHandler returns the handler of DELETE /users/{subject}/roles/{name}:
// 204 after [Store.RevokeRole]. See [Store.GrantRoleHandler] for the rules every
// management write follows.
//
// Example:
//
//	web.Delete("/users/{subject}/roles/{name}", permissions.RevokeRoleHandler(guard), permission.RevokeRoleOperation)
func (store *Store) RevokeRoleHandler(guard *auth.Guard) router.Handler {
	store.needManagement("RevokeRoleHandler")

	return store.change(guard, KindRole, false)
}

// change returns the management write for kind: a grant when grant is true,
// otherwise a revoke, following the rules of [Store.GrantRoleHandler].
func (store *Store) change(guard *auth.Guard, kind Kind, grant bool) router.Handler {
	return func(w http.ResponseWriter, r *http.Request) error {
		caller, err := store.manager(guard, r)
		if err != nil {
			return err
		}
		name, subject := r.PathValue("name"), r.PathValue("subject")
		scopes, roles, err := store.requires(kind, name)
		if err != nil {
			return err
		}
		if !containsAll(caller.Scopes(), scopes) || !containsAll(caller.Roles(), roles) {
			return ErrNotGrantable
		}
		// A grant looks the user up itself; a revoke does not, so it checks here.
		if !grant {
			if err := store.exists(r.Context(), subject); err != nil {
				return err
			}
		}
		if err := store.apply(r.Context(), subject, kind, name, grant); err != nil {
			return missingUser(err)
		}

		return response.Status(w, http.StatusNoContent)
	}
}

// requires returns what a caller must hold to grant or revoke the grant of kind
// called name: for a permission, that permission among their scopes; for a
// role, every permission of the role among their scopes and every role of its
// expansion among their roles, so authority that a role carries only through
// gate.HasRole cannot be handed out either. A name the store does not define is
// [ErrUnknownGrant].
func (store *Store) requires(kind Kind, name string) (scopes, roles []string, err error) {
	if kind == KindPermission {
		if !store.permissions[name] {
			return nil, nil, ErrUnknownGrant
		}

		return []string{name}, nil, nil
	}
	role, ok := store.roles[name]
	if !ok {
		return nil, nil, ErrUnknownGrant
	}

	return names(role.Permissions()), names(role.Expand()), nil
}

// apply runs the store write that change asked for on a defined name.
func (store *Store) apply(ctx context.Context, subject string, kind Kind, name string, grant bool) error {
	switch {
	case kind == KindPermission && grant:
		return store.Grant(ctx, subject, gate.NewPermission(name))
	case kind == KindPermission:
		return store.Revoke(ctx, subject, gate.NewPermission(name))
	case grant:
		return store.GrantRole(ctx, subject, store.roles[name])
	}

	return store.RevokeRole(ctx, subject, store.roles[name])
}

// manager returns the caller's session identity after checking that it holds the
// management permission. With a guard it also requires a fresh login.
func (store *Store) manager(guard *auth.Guard, r *http.Request) (auth.Identity, error) {
	identity, err := web.Session(r)
	if guard != nil {
		identity, err = web.Fresh(guard, r)
	}
	if err != nil {
		return auth.Identity{}, err
	}
	if !slices.Contains(identity.Scopes(), store.management.Name()) {
		return auth.Identity{}, gate.ErrForbidden
	}

	return identity, nil
}

// exists maps an unknown subject to [ErrUserNotFound]; a disabled user exists.
func (store *Store) exists(ctx context.Context, subject string) error {
	_, err := store.provider.Find(ctx, subject)
	if errors.Is(err, auth.ErrUserDisabled) {
		return nil
	}

	return missingUser(err)
}

// missingUser maps the provider's unknown or invalid subject errors to
// [ErrUserNotFound] and returns every other error, nil included, unchanged.
func missingUser(err error) error {
	if errors.Is(err, auth.ErrUserNotFound) || errors.Is(err, auth.ErrInvalidSubject) {
		return ErrUserNotFound
	}

	return err
}

// listed returns names, or an empty list for nil, so JSON shows [] not null.
func listed(names []string) []string {
	if names == nil {
		return []string{}
	}

	return names
}

// needManagement panics at composition when [Builder.Management] is not set,
// because the handler would have no permission to require.
func (store *Store) needManagement(handler string) {
	if !store.managed {
		panic("permission: " + handler + " requires Builder.Management")
	}
}
