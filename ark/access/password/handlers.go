package password

import (
	"cmp"
	"context"
	"errors"
	"log/slog"
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
// It counts the attempt before hashing ([ErrThrottled], 429, with Retry-After),
// authenticates, and hands the user to [auth.Guard.Begin]: 204 when logged in,
// or 202 with the second-factor methods when the user has to answer a
// challenge (POST /login/challenge). A failure is [ErrInvalidCredentials]
// (422), the same for every cause.
//
// Only failed logins count. The limits in 15 minutes are 10 per identifier
// and client address together, so someone failing from elsewhere cannot lock
// the real user out; 50 per identifier across all addresses, a looser cap
// against a distributed guess; and 100 per client address. A successful login
// refunds its attempt on all three and clears the identifier-and-address
// counter. A user locked out of an account by a distributed guess waits for
// the window, or resets the password. A user in a second-factor lockout
// ([Verifier.CountLockout]) is not refunded: a correct password only parks a
// pending login that cannot finish, so it counts as a failure.
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
		counters := verifier.countersFor(identifier, r)
		// Each attempt is reserved before the password is checked, so a burst of
		// parallel guesses cannot slip past the limit, and refunded below when
		// it succeeds: only failures count.
		if err := verifier.reserveLogin(ctx, counters); err != nil {
			return err
		}
		subject, err := verifier.Authenticate(ctx, identifier, body.Password)
		if err != nil {
			return err
		}
		if err := verifier.settleLogin(ctx, counters, subject); err != nil {
			return err
		}
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
// [ErrInvalidPassword] (422). A failing [Builder.Registered] hook is logged and
// does not fail the request. Registrations are counted before anything else,
// per identifier as typed (5) and per client address (30) in 15 minutes
// ([ErrThrottled], 429). A new user is unverified, and with the facade cannot
// connect a provider, enroll two-factor or create tokens until the email is
// verified. It panics at composition when [Users.Create] is nil.
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
		if err := verifier.throttleAction(r, "register", identifier, maxRegistrations, maxClientRegistrations); err != nil {
			return err
		}
		if err := verifier.policy(r.Context(), body.Password); err != nil {
			return err
		}
		subject, err := verifier.register(r.Context(), identifier, body.Password)
		if err != nil {
			return err
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
// is 204. It publishes [Changed] and runs [Builder.AfterChange] (the facade
// revokes the user's device tokens there, because a device keeps no session to
// end). It does not rotate the user, so tokens the hook leaves, such as
// personal access tokens, survive a change. It also deletes the user's live
// reset links. If the hook fails the response is its error (500), but the
// password is already changed and the other logins already ended; the client
// should repeat the change so the hook runs again.
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
		subject := identity.Subject()
		if err := verifier.change(r.Context(), subject, body.Current, body.Password); err != nil {
			return err
		}
		if err := verifier.endOtherLogins(guard, r, subject); err != nil {
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

// register creates the user and stores the password in one transaction, then
// runs the [Builder.Registered] hook, whose failure is logged and does not undo
// the registration.
func (verifier *Verifier) register(ctx context.Context, identifier, password string) (string, error) {
	var subject string
	err := verifier.db.WithTransaction(ctx, func(ctx context.Context) error {
		var err error
		if subject, err = verifier.users.Create(ctx, identifier); err != nil {
			return err
		}

		return verifier.save(ctx, subject, password)
	})
	if err != nil {
		return "", err
	}
	if verifier.registered != nil {
		if err := verifier.registered(ctx, subject, identifier); err != nil {
			slog.ErrorContext(ctx, "access: post-registration hook failed", "subject", subject, "error", err)
		}
	}

	return subject, nil
}

// change checks the policy and the current password, stores the new one and
// drops the reset links sent before.
func (verifier *Verifier) change(ctx context.Context, subject, current, password string) error {
	if err := verifier.policy(ctx, password); err != nil {
		return err
	}
	if err := verifier.checkCurrent(ctx, subject, current); err != nil {
		return err
	}
	if err := verifier.save(ctx, subject, password); err != nil {
		return err
	}
	verifier.emit(ctx, Changed, subject)

	// Reset links sent before the change no longer belong to this password.
	return verifier.deleteResets(ctx, subject)
}

// checkCurrent verifies the current password of a user who has one, counting
// the attempt on the user, not on the login counters. A user without a password
// (who logged in with a provider) sets the first one without it.
func (verifier *Verifier) checkCurrent(ctx context.Context, subject, current string) error {
	has, err := verifier.Has(ctx, subject)
	if err != nil || !has {
		return err
	}
	subjectKey := verifier.identifierKey("subject:" + subject)
	if err := verifier.attempt(ctx, subjectKey, maxFailures); err != nil {
		return err
	}
	if err := verifier.Check(ctx, subject, current); err != nil {
		return err
	}
	verifier.clear(ctx, subjectKey)

	return nil
}

// endOtherLogins runs the [Builder.AfterChange] hook and ends the user's other
// logins. Ending them always runs, even when the hook fails: the password has
// changed, so a failing hook must not leave other sessions alive. The hook's
// error is returned after, and the client may retry.
func (verifier *Verifier) endOtherLogins(guard *auth.Guard, r *http.Request, subject string) error {
	var hookErr error
	if verifier.changed != nil {
		hookErr = verifier.changed(r.Context(), subject)
	}
	if err := guard.RevokeOthers(r); err != nil {
		return errors.Join(err, hookErr)
	}

	return hookErr
}

// ForgotHandler returns the handler of POST /password/forgot. The body is
// {"email": "..."} (or "identifier"). It starts a reset ([Verifier.SendReset])
// and answers 202 whether or not the user exists and whether or not the
// delivery works.
//
// The reset runs in the background, after the response, so the time taken says
// nothing about whether the user exists or how fast the mail server is. A
// failure is logged (never with the token) and not shown to the client. Wait for
// the sends with [Verifier.Wait] at shutdown. The email goes to the address
// stored with the user, never to the text of the request.
//
// Requests are counted before the lookup: more than 5 per identifier as typed
// or 30 per client address in 15 minutes is [ErrThrottled] (429), which holds
// whatever the identifier is. Without a delivery function the request fails
// with [ErrNoDelivery] (500).
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
		if verifier.deliver == nil {
			return ErrNoDelivery
		}
		identifier := body.value()
		if err := verifier.throttleAction(r, "forgot", identifier, maxForgot, maxClientForgot); err != nil {
			return err
		}
		verifier.jobs.Go(func(ctx context.Context) {
			if err := verifier.SendReset(ctx, identifier); err != nil {
				slog.ErrorContext(ctx, "access: password reset email failed", "error", err)
			}
		})

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
