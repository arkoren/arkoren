package iam

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"time"

	"arkoren.dev/core/auth"
	"arkoren.dev/core/http/response"
	"arkoren.dev/core/http/router"

	"arkoren.dev/ark/iam/account"
)

// AccountBuilder configures the optional members of GET /account in a
// [Builder.Account] call. The subject and the email facts of the user are
// always part of the answer; every other member is off until its method turns
// it on, so the response shows only what the application chose to publish.
// Callbacks run directly once, in order, on one fresh draft and methods return
// the same draft.
//
// Example:
//
//	identity.Account(func(view *iam.AccountBuilder) {
//		view.Permissions(true).TwoFactor(true).Login(true)
//	})
type AccountBuilder struct {
	permissions bool
	twoFactor   bool
	login       bool
}

// AccountBuilderFunc is the callback form of [AccountBuilder].
//
// Example:
//
//	var describe iam.AccountBuilderFunc = func(view *iam.AccountBuilder) {
//		view.Login(true)
//	}
//	identity.Account(describe)
type AccountBuilderFunc = func(*AccountBuilder)

// Permissions turns the "roles" and "permissions" members on or off: the
// roles on the request's identity and the identity's scopes that the permission
// store defines, as [permission.Store.SelfHandler] answers them. Both are
// lists, empty ones as [] and never null. It needs [Builder.Permissions]:
// without it [New] ignores the setting with one warning.
//
// Example:
//
//	view.Permissions(true)
func (view *AccountBuilder) Permissions(enabled bool) *AccountBuilder {
	view.permissions = enabled

	return view
}

// TwoFactor turns the "two_factor" member on or off: whether the user has a
// confirmed authenticator. It costs one lookup per call. It needs
// [Builder.TwoFactor]: without it [New] ignores the setting with one warning.
//
// Example:
//
//	view.TwoFactor(true)
func (view *AccountBuilder) TwoFactor(enabled bool) *AccountBuilder {
	view.twoFactor = enabled

	return view
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
//	view.Login(true)
func (view *AccountBuilder) Login(enabled bool) *AccountBuilder {
	view.login = enabled

	return view
}

// AccountView is what GET /account answers and [IAM.Account] returns: facts
// about the authenticated user that the facade knows. It never holds the
// application's own user value, because IAM encodes no application data; an
// application that wants a profile builds its own route from [IAM.Account] and
// its own user (see [IAM.Account]). Members that are nil, zero or empty are
// omitted from the JSON, so each optional member appears only when it is on.
//
// Example:
//
//	view, err := identity.Account(ctx)
//	if err != nil {
//		return err
//	}
//	fmt.Println(view.Subject, view.Email)
type AccountView struct {
	// Subject is the user's subject.
	Subject string `json:"subject"`

	// Email is the "email" claim of the user record, left out when absent.
	Email string `json:"email,omitzero"`

	// EmailVerified is the "email_verified" claim of the user record, left out
	// when the record carries none.
	EmailVerified *bool `json:"email_verified,omitzero"`

	// VerifiedAt is when a built-in user proved the email, left out for
	// unverified users and for the application's own users.
	VerifiedAt time.Time `json:"verified_at,omitzero"`

	// CreatedAt is when a built-in user was created, left out for the
	// application's own users.
	CreatedAt time.Time `json:"created_at,omitzero"`

	// TwoFactor tells whether an authenticator is confirmed, with
	// [AccountBuilder.TwoFactor]; nil leaves it out.
	TwoFactor *bool `json:"two_factor,omitzero"`

	// Login describes how the request authenticated, with
	// [AccountBuilder.Login]; nil leaves it out.
	Login *AccountLogin `json:"login,omitzero"`

	// Roles are the identity's roles with [AccountBuilder.Permissions]. An
	// enabled and empty list is non-nil and encodes as []; a disabled one is
	// nil and omitted. Keep the two apart: json/v2 encodes a nil slice as [],
	// so the omission rests on omitzero.
	Roles []string `json:"roles,omitzero"`

	// Permissions are the identity's defined permissions with
	// [AccountBuilder.Permissions], with the same nil and empty rules as Roles.
	Permissions []string `json:"permissions,omitzero"`
}

// AccountLogin is the "login" member of [AccountView].
type AccountLogin struct {
	// Session is true for a browser session and false for a token.
	Session bool `json:"session"`

	// Level is "none", "single" or "multi", from [auth.Identity.Level].
	Level string `json:"level"`

	// Methods are the core factor codes of the identity, without repeats. It is
	// never nil, so it encodes as [] when empty.
	Methods []string `json:"methods"`
}

// coreMethods are the factor codes of core that the login member lists.
var coreMethods = []auth.Method{auth.MethodPassword, auth.MethodOTP, auth.MethodHardwareKey, auth.MethodSoftwareKey}

// buildAccount resolves [Builder.Account] once the packages exist: it runs the
// callbacks and drops, with one warning each, the settings whose feature is
// off. The resolved draft is what [IAM.AccountHandler] and [IAM.Account] read.
// Without a [Builder.Account] call nothing is kept and no route is mounted.
func (identity *IAM) buildAccount(settings *Builder) {
	if !settings.accountOn {
		return
	}
	view := &AccountBuilder{}
	for _, apply := range settings.account {
		apply(view)
	}
	if view.permissions && identity.permissions == nil {
		ignore("AccountBuilder.Permissions", "Builder.Permissions is off, so there are no roles or permissions to show")
		view.permissions = false
	}
	if view.twoFactor && identity.factors == nil {
		ignore("AccountBuilder.TwoFactor", "Builder.TwoFactor is off, so there is no authenticator to report")
		view.twoFactor = false
	}
	identity.account = view
}

// Account returns what GET /account answers for the identity in ctx, which
// [IAM.Middleware] stores: the subject, the email facts, and the optional
// members that [Builder.Account] turned on. A context without an identity, or
// whose login still waits for its second factor, returns
// [auth.ErrUnauthenticated]. It works without [Builder.Account], which only
// adds the route and turns on the optional parts: without it the view holds the
// subject and the email facts.
//
// With the built-in users the email facts come from the directory: the email,
// whether it is verified, and the two timestamps. With the application's own
// users they come from the "email" and "email_verified" claims of
// [auth.UserRecord.Claims], and there are no timestamps. An application that
// publishes a profile composes it with this view in its own route:
//
//	type Me struct {
//		iam.AccountView
//		Name string `json:"name"`
//	}
//
// Example:
//
//	web.Get("/me", func(w http.ResponseWriter, r *http.Request) error {
//		view, err := identity.Account(r.Context())
//		if err != nil {
//			return err
//		}
//		profile, err := profiles.Find(r.Context(), view.Subject)
//		if err != nil {
//			return err
//		}
//
//		return response.JSON(w, http.StatusOK, Me{AccountView: view, Name: profile.Name})
//	})
func (identity *IAM) Account(ctx context.Context) (AccountView, error) {
	principal, ok := auth.FromContext(ctx)
	if !ok {
		return AccountView{}, auth.ErrUnauthenticated
	}
	settings := identity.account
	if settings == nil {
		settings = &AccountBuilder{}
	}
	record := principal.Record()
	view := AccountView{Subject: principal.Subject()}
	view.Email, _ = record.Claims["email"].(string)
	if verified, ok := record.Claims["email_verified"].(bool); ok {
		view.EmailVerified = &verified
	}
	if identity.directory != nil && record.Value != nil {
		user, ok := record.Value.(account.Account)
		if !ok {
			err := fmt.Errorf("access: account: user record holds a %T, want account.Account", record.Value)
			slog.ErrorContext(ctx, err.Error())

			return AccountView{}, err
		}
		view.VerifiedAt, view.CreatedAt = user.VerifiedAt, user.CreatedAt
	}
	if settings.permissions {
		defined := identity.permissions.Catalog()
		// append to a non-nil slice: an enabled list that is empty must encode
		// as [], while a disabled one stays nil and is omitted.
		view.Roles = append([]string{}, principal.Roles()...)
		view.Permissions = append([]string{}, slices.DeleteFunc(principal.Scopes(), func(scope string) bool { return !slices.Contains(defined, scope) })...)
	}
	if settings.twoFactor {
		enrolled, err := identity.factors.Enrolled(ctx, principal.Subject())
		if err != nil {
			return AccountView{}, err
		}
		view.TwoFactor = &enrolled
	}
	if settings.login {
		view.Login = loginOf(principal)
	}

	return view, nil
}

// AccountHandler returns the handler of GET /account: 200 with the
// [AccountView] of the request. A request without an identity, including a login
// that still waits for its second factor, answers 401
// ([auth.ErrUnauthenticated]); a session cookie and a token both work. It panics
// unless [Builder.Account] was called.
//
// Example:
//
//	web.Get("/account", identity.AccountHandler())
func (identity *IAM) AccountHandler() router.Handler {
	need(identity.account != nil, "AccountHandler", "Builder.Account")

	return func(w http.ResponseWriter, r *http.Request) error {
		view, err := identity.Account(r.Context())
		if err != nil {
			return err
		}

		return response.JSON(w, http.StatusOK, view)
	}
}

// loginOf describes how principal authenticated.
func loginOf(principal auth.Identity) *AccountLogin {
	login := &AccountLogin{Session: principal.Session(), Level: "none", Methods: []string{}}
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
