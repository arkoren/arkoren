package iam

import (
	"context"
	"fmt"

	"arkoren.dev/core/auth/gate"
	"arkoren.dev/core/http/router"

	"arkoren.dev/ark/iam/permission"
)

// Permissions returns the permission store, or nil without
// [Builder.Permissions].
//
// Example:
//
//	if permissions := identity.Permissions(); permissions != nil {
//		admins, err := permissions.Holders(ctx, Admin)
//	}
func (identity *IAM) Permissions() *permission.Store {
	return identity.permissions
}

// permissionStore returns the permission store, or an error wrapping [ErrInvalidConfig]
// naming the forwarder when [Builder.Permissions] is not set.
func (identity *IAM) permissionStore(method string) (*permission.Store, error) {
	if identity.permissions == nil {
		return nil, fmt.Errorf("%w: %s requires Builder.Permissions", ErrInvalidConfig, method)
	}

	return identity.permissions, nil
}

// Grant gives subject a permission directly; see [permission.Store.Grant].
// Without [Builder.Permissions] it returns an error wrapping
// [ErrInvalidConfig].
//
// Example:
//
//	err := identity.Grant(ctx, subject, BillingExport)
func (identity *IAM) Grant(ctx context.Context, subject string, granted gate.Permission) error {
	store, err := identity.permissionStore("Grant")
	if err != nil {
		return err
	}

	return store.Grant(ctx, subject, granted)
}

// Revoke takes a directly granted permission away from subject; see
// [permission.Store.Revoke]. Without [Builder.Permissions] it returns an error
// wrapping [ErrInvalidConfig].
//
// Example:
//
//	err := identity.Revoke(ctx, subject, BillingExport)
func (identity *IAM) Revoke(ctx context.Context, subject string, revoked gate.Permission) error {
	store, err := identity.permissionStore("Revoke")
	if err != nil {
		return err
	}

	return store.Revoke(ctx, subject, revoked)
}

// GrantRole assigns subject a role; see [permission.Store.GrantRole]. Without
// [Builder.Permissions] it returns an error wrapping [ErrInvalidConfig].
//
// Example:
//
//	err := identity.GrantRole(ctx, subject, Editor)
func (identity *IAM) GrantRole(ctx context.Context, subject string, role gate.Role) error {
	store, err := identity.permissionStore("GrantRole")
	if err != nil {
		return err
	}

	return store.GrantRole(ctx, subject, role)
}

// RevokeRole takes a role away from subject; see [permission.Store.RevokeRole],
// which answers [permission.ErrLastHolder] for the last holder of a KeepOne
// role. Without [Builder.Permissions] it returns an error wrapping
// [ErrInvalidConfig].
//
// Example:
//
//	err := identity.RevokeRole(ctx, subject, Editor)
func (identity *IAM) RevokeRole(ctx context.Context, subject string, role gate.Role) error {
	store, err := identity.permissionStore("RevokeRole")
	if err != nil {
		return err
	}

	return store.RevokeRole(ctx, subject, role)
}

// HasPermission reports whether subject holds every required permission; see
// [permission.Store.HasPermission]. It answers for any user; for the request's
// identity use gate.HasPermission. Without [Builder.Permissions] it returns an
// error wrapping [ErrInvalidConfig].
//
// Example:
//
//	allowed, err := identity.HasPermission(ctx, subject, BillingExport)
func (identity *IAM) HasPermission(ctx context.Context, subject string, required ...gate.Permission) (bool, error) {
	store, err := identity.permissionStore("HasPermission")
	if err != nil {
		return false, err
	}

	return store.HasPermission(ctx, subject, required...)
}

// HasAnyPermission reports whether subject holds at least one of permissions;
// see [permission.Store.HasAnyPermission]. Without [Builder.Permissions] it
// returns an error wrapping [ErrInvalidConfig].
//
// Example:
//
//	allowed, err := identity.HasAnyPermission(ctx, subject, PostsWrite, PostsModerate)
func (identity *IAM) HasAnyPermission(ctx context.Context, subject string, permissions ...gate.Permission) (bool, error) {
	store, err := identity.permissionStore("HasAnyPermission")
	if err != nil {
		return false, err
	}

	return store.HasAnyPermission(ctx, subject, permissions...)
}

// HasRole reports whether subject holds every required role, directly or through
// an including role; see [permission.Store.HasRole]. Without
// [Builder.Permissions] it returns an error wrapping [ErrInvalidConfig].
//
// Example:
//
//	isEditor, err := identity.HasRole(ctx, subject, Editor)
func (identity *IAM) HasRole(ctx context.Context, subject string, required ...gate.Role) (bool, error) {
	store, err := identity.permissionStore("HasRole")
	if err != nil {
		return false, err
	}

	return store.HasRole(ctx, subject, required...)
}

// HasAnyRole reports whether subject holds at least one of roles; see
// [permission.Store.HasAnyRole]. Without [Builder.Permissions] it returns an
// error wrapping [ErrInvalidConfig].
//
// Example:
//
//	staff, err := identity.HasAnyRole(ctx, subject, Editor, Admin)
func (identity *IAM) HasAnyRole(ctx context.Context, subject string, roles ...gate.Role) (bool, error) {
	store, err := identity.permissionStore("HasAnyRole")
	if err != nil {
		return false, err
	}

	return store.HasAnyRole(ctx, subject, roles...)
}

// SelfPermissionsHandler returns the handler of GET /me/permissions; see
// [permission.Store.SelfHandler]. It panics unless [Builder.Permissions] is
// enabled.
//
// Example:
//
//	web.Get("/me/permissions", identity.SelfPermissionsHandler(), permission.SelfOperation)
func (identity *IAM) SelfPermissionsHandler() router.Handler {
	need(identity.permissions != nil, "SelfPermissionsHandler", "Builder.Permissions")

	return identity.permissions.SelfHandler()
}

// UserPermissionsHandler returns the handler of GET /users/{subject}/permissions;
// see [permission.Store.UserHandler]. It panics unless [Builder.Permissions] is
// enabled with [permission.Builder.Management].
//
// Example:
//
//	web.Get("/users/{subject}/permissions", identity.UserPermissionsHandler(), permission.UserOperation)
func (identity *IAM) UserPermissionsHandler() router.Handler {
	identity.needManagement("UserPermissionsHandler")

	return identity.permissions.UserHandler()
}

// GrantPermissionHandler returns the handler of
// PUT /users/{subject}/permissions/{name}; see [permission.Store.GrantHandler]
// and [permission.Store.GrantRoleHandler] for the session, freshness and
// escalation rules. It panics unless [Builder.Permissions] is enabled with
// [permission.Builder.Management].
//
// Example:
//
//	web.Put("/users/{subject}/permissions/{name}", identity.GrantPermissionHandler(), permission.GrantOperation)
func (identity *IAM) GrantPermissionHandler() router.Handler {
	identity.needManagement("GrantPermissionHandler")

	return identity.permissions.GrantHandler(identity.guard)
}

// RevokePermissionHandler returns the handler of
// DELETE /users/{subject}/permissions/{name}; see
// [permission.Store.RevokeHandler]. It panics unless [Builder.Permissions] is
// enabled with [permission.Builder.Management].
//
// Example:
//
//	web.Delete("/users/{subject}/permissions/{name}", identity.RevokePermissionHandler(), permission.RevokeOperation)
func (identity *IAM) RevokePermissionHandler() router.Handler {
	identity.needManagement("RevokePermissionHandler")

	return identity.permissions.RevokeHandler(identity.guard)
}

// GrantRoleHandler returns the handler of PUT /users/{subject}/roles/{name}; see
// [permission.Store.GrantRoleHandler]. It panics unless [Builder.Permissions] is
// enabled with [permission.Builder.Management].
//
// Example:
//
//	web.Put("/users/{subject}/roles/{name}", identity.GrantRoleHandler(), permission.GrantRoleOperation)
func (identity *IAM) GrantRoleHandler() router.Handler {
	identity.needManagement("GrantRoleHandler")

	return identity.permissions.GrantRoleHandler(identity.guard)
}

// RevokeRoleHandler returns the handler of DELETE /users/{subject}/roles/{name};
// see [permission.Store.RevokeRoleHandler]. It panics unless
// [Builder.Permissions] is enabled with [permission.Builder.Management].
//
// Example:
//
//	web.Delete("/users/{subject}/roles/{name}", identity.RevokeRoleHandler(), permission.RevokeRoleOperation)
func (identity *IAM) RevokeRoleHandler() router.Handler {
	identity.needManagement("RevokeRoleHandler")

	return identity.permissions.RevokeRoleHandler(identity.guard)
}

// needManagement panics unless the permission store exists with
// [permission.Builder.Management].
func (identity *IAM) needManagement(handler string) {
	need(identity.permissions != nil, handler, "Builder.Permissions")
	_, managed := identity.permissions.Management()
	need(managed, handler, "permission.Builder.Management")
}
