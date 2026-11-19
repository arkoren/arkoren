package iam

import (
	"fmt"
	"log/slog"
	"net/http"
	"reflect"
	"slices"

	"arkoren.dev/core/auth"
	"arkoren.dev/core/http/request"
	"arkoren.dev/core/http/response"
	"arkoren.dev/core/http/router"

	"arkoren.dev/ark/iam/account"
)

// MeBuilder configures the response of GET /me in a [Builder.Me] call. Every
// section beyond the always present subject and email claims is off until its
// method turns it on, so the response shows only what the application chose to
// publish. Callbacks run directly once, in order, on one fresh draft and
// methods return the same draft.
//
// Example:
//
//	identity.Me(func(me *iam.MeBuilder) {
//		me.Permissions(true).TwoFactor(true).Login(true)
//	})
type MeBuilder struct {
	user        func(value any) (any, error)
	permissions bool
	twoFactor   bool
	login       bool
}

// MeBuilderFunc is the callback form of [MeBuilder].
//
// Example:
//
//	var describe iam.MeBuilderFunc = func(me *iam.MeBuilder) {
//		me.Login(true)
//	}
//	identity.Me(describe)
type MeBuilderFunc = func(*MeBuilder)

// User adds a "user" member with what present returns for the application's own
// user. U is the type the user driver returns as [auth.UserRecord.Value], for
// example the user type of [auth.NewUserDriver], and V is the type to publish.
// IAM never encodes a raw [auth.UserRecord.Value]: the driver's struct often
// carries a credentials stamp, a disabled flag or other fields that must not
// leave the server, so the application projects it onto a type that holds only
// what the client may see. Calls replace earlier ones.
//
// It only applies with the application's own [Builder.Users]. The built-in
// users always present [account.Account], and with them [New] ignores User
// with one warning. A record without a value adds no "user" member. A value that
// is not a U fails the request with 500 and logs the expected and the actual
// type.
//
// Example:
//
//	me.User(func(account Account) PublicAccount {
//		return PublicAccount{ID: account.ID, Name: account.Name, Email: account.Email, Plan: account.Plan}
//	})
func (me *MeBuilder) User[U, V any](present func(user U) V) *MeBuilder {
	me.user = func(value any) (any, error) {
		user, ok := value.(U)
		if !ok {
			return nil, fmt.Errorf("access: GET /me: user record holds a %T, want the %v given to MeBuilder.User", value, reflect.TypeFor[U]())
		}

		return present(user), nil
	}

	return me
}

// Permissions turns the "roles" and "permissions" members on or off: the
// roles on the request's identity and the identity's scopes that the permission
// store defines, as [permission.Store.SelfHandler] answers them. Both are
// lists, empty ones as [] and never null. It needs [Builder.Permissions]:
// without it [New] ignores the setting with one warning.
//
// Example:
//
//	me.Permissions(true)
func (me *MeBuilder) Permissions(enabled bool) *MeBuilder {
	me.permissions = enabled

	return me
}

// TwoFactor turns the "two_factor" member on or off: whether the user has a
// confirmed authenticator. It costs one lookup per request. It needs
// [Builder.TwoFactor]: without it [New] ignores the setting with one warning.
//
// Example:
//
//	me.TwoFactor(true)
func (me *MeBuilder) TwoFactor(enabled bool) *MeBuilder {
	me.twoFactor = enabled

	return me
}

// Login turns the "login" member on or off: how the request authenticated, as
// {"session": bool, "level": "none"|"single"|"multi", "methods": [...]}.
// session is false for a request that carries a token, and such an identity has
// no factors, so its level is "none" and its methods empty. methods lists the
// core factor codes ("pwd", "otp", "hwk", "swk") in the order they were
// verified; factors a module names itself, such as a recovery code or a
// federated login, count toward the level but are not listed.
//
// Example:
//
//	me.Login(true)
func (me *MeBuilder) Login(enabled bool) *MeBuilder {
	me.login = enabled

	return me
}

// meBody is the response of GET /me. Pointers and lists that are nil are left
// out, so each section appears only when it is on; a section that is on and
// empty is still written ([] or false).
type meBody struct {
	// Subject is the user's subject.
	Subject string `json:"subject"`

	// Email is the "email" claim of the user record, left out when absent.
	Email string `json:"email,omitzero"`

	// EmailVerified is the "email_verified" claim, left out when the record
	// carries none.
	EmailVerified *bool `json:"email_verified,omitzero"`

	// Roles are the identity's roles with [MeBuilder.Permissions].
	Roles []string `json:"roles,omitzero"`

	// Permissions are the identity's defined permissions with
	// [MeBuilder.Permissions].
	Permissions []string `json:"permissions,omitzero"`

	// TwoFactor tells whether an authenticator is confirmed, with
	// [MeBuilder.TwoFactor].
	TwoFactor *bool `json:"two_factor,omitzero"`

	// Login describes the authentication with [MeBuilder.Login].
	Login *meLogin `json:"login,omitzero"`

	// User is the projection of the user, built-in or [MeBuilder.User].
	User any `json:"user,omitzero"`
}

// meLogin is the "login" member of [meBody].
type meLogin struct {
	// Session is true for a browser session and false for a token.
	Session bool `json:"session"`

	// Level is "none", "single" or "multi", from [auth.Identity.Level].
	Level string `json:"level"`

	// Methods are the core factor codes of the identity, without repeats.
	Methods []string `json:"methods"`
}

// coreMethods are the factor codes of core that GET /me lists.
var coreMethods = []auth.Method{auth.MethodPassword, auth.MethodOTP, auth.MethodHardwareKey, auth.MethodSoftwareKey}

// buildMe resolves [Builder.Me] once the packages exist: it runs the callbacks
// and drops, with one warning each, the settings whose feature is off. The
// resolved draft is what [IAM.MeHandler] reads. Without [Builder.Me] enabled
// nothing is kept and no route is mounted.
func (identity *IAM) buildMe(settings *Builder) {
	if !settings.meOn {
		return
	}
	me := &MeBuilder{}
	for _, apply := range settings.me {
		apply(me)
	}
	if me.permissions && identity.permissions == nil {
		ignore("MeBuilder.Permissions", "Builder.Permissions is off, so there are no roles or permissions to show")
		me.permissions = false
	}
	if me.twoFactor && identity.factors == nil {
		ignore("MeBuilder.TwoFactor", "Builder.TwoFactor is off, so there is no authenticator to report")
		me.twoFactor = false
	}
	if me.user != nil && identity.directory != nil {
		ignore("MeBuilder.User", "the built-in users always present account.Account")
		me.user = nil
	}
	identity.me = me
}

// MeHandler returns the handler of GET /me: 200 with the identity of the
// request. It always carries the subject and, when the user record has them,
// the "email" and "email_verified" claims. [MeBuilder] adds the rest: "user"
// (the built-in [account.Account], or the application's projection from
// [MeBuilder.User]), "roles" and "permissions", "two_factor" and "login". A
// request without an identity, including a login that still waits for its
// second factor, answers 401 ([auth.ErrUnauthenticated]); a session cookie and
// a token both work. It panics unless [Builder.Me] is enabled.
//
// Example:
//
//	web.Get("/me", identity.MeHandler())
func (identity *IAM) MeHandler() router.Handler {
	need(identity.me != nil, "MeHandler", "Builder.Me")
	me := identity.me
	var defined []string
	if me.permissions {
		defined = identity.permissions.Catalog()
	}

	return func(w http.ResponseWriter, r *http.Request) error {
		principal, err := request.Identity(r)
		if err != nil {
			return err
		}
		record := principal.Record()
		body := meBody{Subject: principal.Subject()}
		body.Email, _ = record.Claims["email"].(string)
		if verified, ok := record.Claims["email_verified"].(bool); ok {
			body.EmailVerified = &verified
		}
		if me.permissions {
			body.Roles = nonNil(principal.Roles())
			body.Permissions = nonNil(slices.DeleteFunc(principal.Scopes(), func(scope string) bool { return !slices.Contains(defined, scope) }))
		}
		if me.twoFactor {
			enrolled, err := identity.factors.Enrolled(r.Context(), principal.Subject())
			if err != nil {
				return err
			}
			body.TwoFactor = &enrolled
		}
		if me.login {
			body.Login = loginOf(principal)
		}
		if body.User, err = identity.meUser(r, me, record.Value); err != nil {
			return err
		}

		return response.JSON(w, http.StatusOK, body)
	}
}

// meUser returns the "user" member for the record value of a request: the
// built-in [account.Account], the projection of [MeBuilder.User], or nil when
// there is nothing to show. A value of the wrong type is logged with both types
// and answered as a plain 500, because it means the application's driver and
// its projection disagree.
func (identity *IAM) meUser(r *http.Request, me *MeBuilder, value any) (any, error) {
	if value == nil {
		return nil, nil
	}
	if identity.directory != nil {
		user, ok := value.(account.Account)
		if !ok {
			err := fmt.Errorf("access: GET /me: user record holds a %T, want account.Account", value)
			slog.ErrorContext(r.Context(), err.Error())

			return nil, err
		}

		return user, nil
	}
	if me.user == nil {
		return nil, nil
	}
	user, err := me.user(value)
	if err != nil {
		slog.ErrorContext(r.Context(), err.Error())
	}

	return user, err
}

// loginOf describes how principal authenticated.
func loginOf(principal auth.Identity) *meLogin {
	login := &meLogin{Session: principal.Session(), Level: "none", Methods: []string{}}
	switch principal.Level() {
	case auth.LevelSingle:
		login.Level = "single"
	case auth.LevelMulti:
		login.Level = "multi"
	}
	for _, factor := range principal.Factors() {
		if slices.Contains(coreMethods, factor.Method) && !slices.Contains(login.Methods, string(factor.Method)) {
			login.Methods = append(login.Methods, string(factor.Method))
		}
	}

	return login
}

// nonNil returns names, or an empty list for nil, so JSON shows [] and not
// null.
func nonNil(names []string) []string {
	if names == nil {
		return []string{}
	}

	return names
}
