package password

import (
	"cmp"
	"context"
	"net/http"

	"arkoren.dev/core/auth"
	"arkoren.dev/core/http/response"
	"arkoren.dev/core/http/router"

	"arkoren.dev/ark/access/internal/web"
)

// requestBody is the JSON body shared by the handlers of this package; each
// reads the members it needs and ignores the rest. The identifier is "email" in
// the usual case; "identifier" is accepted for applications whose users log in
// with something else.
type requestBody struct {
	Email      string `json:"email"`
	Identifier string `json:"identifier"`
	Password   string `json:"password"`
	Current    string `json:"current_password"`
	Token      string `json:"token"`
}

// value returns the identifier the client sent.
func (body requestBody) value() string {
	return cmp.Or(body.Email, body.Identifier)
}

// passwordFactor is the factor a password proof records.
var passwordFactor = auth.Factor{Method: auth.MethodPassword, Kind: auth.KindKnowledge}

// LoginHandler returns the handler of POST /login. The body is
// {"email": "...", "password": "..."} (or "identifier" instead of "email").
// It counts the attempt per identifier and per client address before hashing
// ([ErrThrottled], 429, with Retry-After), authenticates, and hands the user to
// [auth.Guard.Begin]: 204 when logged in, or 202 with the second-factor methods
// when the user has to answer a challenge (POST /login/challenge). A failure is
// [ErrInvalidCredentials] (422), the same for every cause. The limits are 10
// attempts per identifier and 100 per client address in 15 minutes; a
// successful login clears the identifier's counter and refunds the address's.
//
// Example:
//
//	web.Post("/login", verifier.LoginHandler(guard))
func (verifier *Verifier) LoginHandler(guard *auth.Guard) router.Handler {
	return func(w http.ResponseWriter, r *http.Request) error {
		body, err := web.Decode[requestBody](r)
		if err != nil {
			return err
		}
		identifier := body.value()
		if identifier == "" || body.Password == "" {
			return ErrInvalidCredentials
		}
		ctx := r.Context()
		identifierKey, clientKey := verifier.identifierKey(identifier), verifier.clientKey(r)
		if err := verifier.attempt(ctx, identifierKey, maxFailures); err != nil {
			return err
		}
		if err := verifier.attempt(ctx, clientKey, maxClientFailures); err != nil {
			return err
		}
		subject, err := verifier.Authenticate(ctx, identifier, body.Password)
		if err != nil {
			return err
		}
		verifier.clear(ctx, identifierKey)
		// A success is not an attempt against the address: refund it.
		_, _ = verifier.store.Decrement(ctx, clientKey, 1)
		outcome, err := guard.Begin(r, subject, passwordFactor)
		if err != nil {
			return err
		}

		return outcome.Response(w)
	}
}

// RegisterHandler returns the handler of POST /register. The body is
// {"email": "...", "password": "..."} (or "identifier"). It checks the password
// policy, then creates the user ([Users.Create]) and stores the password in one
// transaction, runs the [Builder.Registered] hook, logs the user in and answers
// 201 with {"subject": "..."}. A taken identifier or an invalid one returns the
// error of [Users.Create] (409 and 422 with the directory); a weak password
// [ErrInvalidPassword] (422). It panics at composition when [Users.Create] is
// nil.
//
// Example:
//
//	web.Post("/register", verifier.RegisterHandler(guard))
func (verifier *Verifier) RegisterHandler(guard *auth.Guard) router.Handler {
	if verifier.users.Create == nil {
		panic("password: RegisterHandler requires Users.Create")
	}

	return func(w http.ResponseWriter, r *http.Request) error {
		body, err := web.Decode[requestBody](r)
		if err != nil {
			return err
		}
		identifier := body.value()
		if err := verifier.policy(r.Context(), body.Password); err != nil {
			return err
		}
		var subject string
		err = verifier.db.WithTransaction(r.Context(), func(ctx context.Context) error {
			var err error
			if subject, err = verifier.users.Create(ctx, identifier); err != nil {
				return err
			}

			return verifier.save(ctx, subject, body.Password)
		})
		if err != nil {
			return err
		}
		if verifier.registered != nil {
			if err := verifier.registered(r.Context(), subject, identifier); err != nil {
				return err
			}
		}
		outcome, err := guard.Begin(r, subject, passwordFactor)
		if err != nil {
			return err
		}
		if !outcome.Complete() {
			return outcome.Response(w)
		}

		return response.JSON(w, http.StatusCreated, map[string]string{"subject": subject})
	}
}

// ChangeHandler returns the handler of PUT /password. The body is
// {"current_password": "...", "password": "..."}; "current_password" is
// required unless the user has no password yet (a user created by a social
// login sets their first one). It needs a browser session and a fresh login
// ([auth.Guard.Fresh]), checks the current password with the same throttle as
// login, stores the new one, ends the user's other logins and keeps the
// caller's session, which also records the password as a recent proof. Success
// is 204. It publishes [Changed]. It does not rotate the user, so API tokens
// survive a change.
//
// Example:
//
//	web.Put("/password", verifier.ChangeHandler(guard))
func (verifier *Verifier) ChangeHandler(guard *auth.Guard) router.Handler {
	return func(w http.ResponseWriter, r *http.Request) error {
		identity, err := web.Fresh(guard, r)
		if err != nil {
			return err
		}
		body, err := web.Decode[requestBody](r)
		if err != nil {
			return err
		}
		ctx, subject := r.Context(), identity.Subject()
		if err := verifier.policy(ctx, body.Password); err != nil {
			return err
		}
		has, err := verifier.Has(ctx, subject)
		if err != nil {
			return err
		}
		if has {
			subjectKey := verifier.identifierKey("subject:" + subject)
			if err := verifier.attempt(ctx, subjectKey, maxFailures); err != nil {
				return err
			}
			if err := verifier.Check(ctx, subject, body.Current); err != nil {
				return err
			}
			verifier.clear(ctx, subjectKey)
		}
		if err := verifier.save(ctx, subject, body.Password); err != nil {
			return err
		}
		verifier.emit(ctx, Changed, subject)
		if err := guard.RevokeOthers(r); err != nil {
			return err
		}
		// Begin on the session of the same subject keeps the login and records
		// the password as a factor from now, so the user is fresh for the next
		// credential change.
		outcome, err := guard.Begin(r, subject, passwordFactor)
		if err != nil {
			return err
		}

		return outcome.Response(w)
	}
}

// ForgotHandler returns the handler of POST /password/forgot. The body is
// {"email": "..."} (or "identifier"). It starts a reset ([Verifier.SendReset])
// and answers 202 whether or not the user exists. Put a rate limit in front of
// it: every request can send an email.
//
// Example:
//
//	web.Post("/password/forgot", verifier.ForgotHandler())
func (verifier *Verifier) ForgotHandler() router.Handler {
	return func(w http.ResponseWriter, r *http.Request) error {
		body, err := web.Decode[requestBody](r)
		if err != nil {
			return err
		}
		if err := verifier.SendReset(r.Context(), body.value()); err != nil {
			return err
		}

		return response.Status(w, http.StatusAccepted)
	}
}

// ResetHandler returns the handler of POST /password/reset. The body is
// {"token": "...", "password": "..."}. It runs [Verifier.Reset], which rotates
// the user, then removes the user's login records with
// [auth.Guard.RevokeAll]. It never logs the user in: after a reset the user logs
// in again, with their second factor if they have one. Success is 204 and the
// publisher of [WasReset] has run.
//
// Example:
//
//	web.Post("/password/reset", verifier.ResetHandler(guard))
func (verifier *Verifier) ResetHandler(guard *auth.Guard) router.Handler {
	return func(w http.ResponseWriter, r *http.Request) error {
		body, err := web.Decode[requestBody](r)
		if err != nil {
			return err
		}
		subject, err := verifier.Reset(r.Context(), body.Token, body.Password)
		if err != nil {
			return err
		}
		if err := guard.RevokeAll(r.Context(), subject); err != nil {
			return err
		}

		return response.Status(w, http.StatusNoContent)
	}
}
