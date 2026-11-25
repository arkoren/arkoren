package auth

import (
	"context"
	"crypto/subtle"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"time"

	"arkoren.dev/core/http/response"
	"arkoren.dev/core/http/router"
	"arkoren.dev/core/http/session"
	"arkoren.dev/core/service/cache"
	"arkoren.dev/foundation/problem"
)

// maxCredentialBody is the largest request body, in bytes, that a challenge or
// confirm route reads. Credential input is a short code or an assertion, so a
// small limit protects the route from oversized bodies.
const maxCredentialBody = 64 << 10

// SecondFactorChallenge is the body of the 202 response that tells a client its
// login needs a second factor, and which methods the user may choose from, in
// registration order. [Outcome.Challenge] returns it, and the client answers with
// a request to [Guard.ChallengeHandler].
//
// Example:
//
//	challenge := auth.SecondFactorChallenge{}
//	challenge.SecondFactor.Methods = []auth.Method{auth.MethodOTP}
//	body, err := json.Marshal(challenge) // {"second_factor":{"methods":["otp"]}}
type SecondFactorChallenge struct {
	// SecondFactor holds what the client needs to continue the login.
	SecondFactor struct {
		// Methods lists the user's enrolled second-factor methods, in
		// registration order. It is never empty in a challenge from [Guard.Begin].
		Methods []Method `json:"methods"`
	} `json:"second_factor"`
}

// Outcome is the result of [Guard.Begin]: either the user is logged in, or the
// login is parked until a second factor is verified. A login method returns it to
// the client with [Outcome.Response], so every method answers a parked login the
// same way. The zero Outcome is not complete and has no methods.
//
// Example:
//
//	outcome, err := guard.Begin(r, subject, auth.Factor{Method: auth.MethodPassword, Kind: auth.KindKnowledge})
//	if err != nil {
//		return err
//	}
//	return outcome.Response(w)
type Outcome struct {
	// complete is true when the user is logged in.
	complete bool

	// methods are the user's enrolled second-factor methods of a parked login.
	methods []Method
}

// Complete reports whether the user is logged in. When false, the login is
// parked and the request stays anonymous until a second factor is verified.
//
// Example:
//
//	if outcome.Complete() {
//		// Redirect into the application.
//	}
func (outcome Outcome) Complete() bool {
	return outcome.complete
}

// Methods returns a copy of the user's enrolled second-factor methods, in
// registration order. It is empty when the login is complete.
//
// Example:
//
//	fmt.Println(outcome.Methods()) // [otp]
func (outcome Outcome) Methods() []Method {
	return slices.Clone(outcome.methods)
}

// Challenge returns the body that asks the client for a second factor. For a
// complete login its methods are empty; use [Outcome.Response] unless you render
// the challenge yourself.
//
// Example:
//
//	body := outcome.Challenge()
//	fmt.Println(body.SecondFactor.Methods)
func (outcome Outcome) Challenge() SecondFactorChallenge {
	var challenge SecondFactorChallenge

	challenge.SecondFactor.Methods = outcome.Methods()

	return challenge
}

// Response writes the standard answer of a login route: 204 No Content when the
// login is complete, otherwise 202 Accepted with the JSON of
// [Outcome.Challenge]. The session cookie travels with it through the session
// middleware. It returns the error of the response writer.
//
// Example:
//
//	outcome, err := guard.Begin(r, subject, factor)
//	if err != nil {
//		return err
//	}
//	return outcome.Response(w)
func (outcome Outcome) Response(w http.ResponseWriter) error {
	if outcome.complete {
		return response.Status(w, http.StatusNoContent)
	}

	return response.JSON(w, http.StatusAccepted, outcome.Challenge())
}

// PendingLogin describes a login parked by [Guard.Begin]: who is waiting and
// which second-factor methods they can use.
//
// Example:
//
//	pending, err := guard.Pending(r)
//	if err != nil {
//		return err
//	}
//	fmt.Println(pending.Subject, pending.Methods)
type PendingLogin struct {
	// Subject is the user waiting to finish the login. It is for server-side use
	// only: the pending request is anonymous, so never send it to the client.
	Subject string

	// Methods are the user's enrolled second-factor methods, in registration order,
	// read when Pending ran.
	Methods []Method
}

// Begin is the single entry for every login method. Call it after the method
// verified its credential, passing every factor the login proved: a password
// login passes one, a passkey that proved possession and inherence passes two. The
// guard stamps each factor with its own clock, keeping only a supplied [Factor.At]
// that is earlier than that clock (a claim can age a factor, never refresh it). Begin looks
// subject up with the provider first: a deleted or disabled user is
// [ErrUnauthenticated], and other provider errors, including [ErrInvalidSubject],
// are returned unchanged. Passing no factor is an error.
//
// When the session is already logged in as subject, Begin adds the factors to
// the logged-in session (a factor with the same [Method] and [Kind] replaces the
// older one), keeps the login record, skips the second-factor rules, regenerates
// the session ID and returns a complete outcome. The user already passed every
// check, so a logged-in user who connects a provider or re-authenticates keeps
// the factors they have and is never challenged again. Because the login record
// stays, [Guard.RecentLogin] does not see such a silent re-login as a new login.
// A different subject, an anonymous session and a pending login take the path
// below.
//
// Otherwise, when the factors already reach [LevelMulti], Begin logs the user in
// without a challenge. If not, it asks every credential registered with
// [GuardBuilder.SecondFactor] whether the user is enrolled. With no enrolled
// method it logs the user in; otherwise it replaces any logged-in user with a
// pending login that expires after [GuardBuilder.ChallengeTTL], regenerates the
// session ID and returns an outcome that is not complete: the request stays
// anonymous until [Guard.ChallengeHandler] verifies a method. An error from an
// Enrolled function fails the login with that error and changes nothing.
//
// Logging in creates a [LoginRecord] for the device (see [Guard.Logins]) and
// removes the record of the login the session replaces, so Begin can also return
// the cache or lock errors of the record store; the session is then unchanged.
// A parked login creates its record when the challenge succeeds.
//
// Begin does not verify the credential: call it only after it checked out.
//
// Example:
//
//	outcome, err := guard.Begin(r, subject, auth.Factor{Method: auth.MethodPassword, Kind: auth.KindKnowledge})
//	if err != nil {
//		return err
//	}
//	return outcome.Response(w) // 204, or 202 with the methods to choose from
func (guard *Guard) Begin(r *http.Request, subject string, factors ...Factor) (Outcome, error) {
	if len(factors) == 0 {
		return Outcome{}, errors.New("auth: begin without a factor")
	}

	current, err := guard.session(r)

	if err != nil {
		return Outcome{}, err
	}

	user, err := guard.provider.Find(r.Context(), subject)

	if gone(err) {
		return Outcome{}, ErrUnauthenticated
	}

	if err != nil {
		return Outcome{}, err
	}

	// A session already logged in as this subject passed every check for it, so
	// the new factors join the existing login instead of replacing it.
	// It does not load grants, so a permission store outage cannot break a
	// second-factor or step-up login.
	stored, _, err := guard.active(r)

	if err == nil && stored.Subject == user.Subject {
		if err := guard.confirm(r, factors...); err != nil {
			return Outcome{}, err
		}

		return Outcome{complete: true}, nil
	}

	if err != nil && !errors.Is(err, ErrNoCredentials) {
		return Outcome{}, err
	}

	now := time.Now()

	var stamped []Factor

	for _, factor := range factors {
		stamped = withFactor(stamped, factor, now)
	}

	var methods []Method

	if NewIdentity(user.Subject, func(principal *IdentityBuilder) { principal.Factors(stamped...) }).Level() < LevelMulti {
		methods, err = enrolled(r.Context(), user.Subject, guard.settings.SecondFactors)

		if err != nil {
			return Outcome{}, err
		}
	}

	if len(methods) == 0 {
		if err := guard.start(r, current, user, stamped); err != nil {
			return Outcome{}, err
		}

		return Outcome{complete: true}, nil
	}

	waiting := pendingLogin{Subject: user.Subject, Factors: stamped, Version: hashVersion(user.Version), Expires: now.Add(guard.settings.ChallengeTTL).UTC()}

	if err := guard.release(r.Context(), current); err != nil {
		return Outcome{}, err
	}

	if err := current.Delete(sessionField); err != nil {
		return Outcome{}, err
	}

	if err := current.Put(pendingField, waiting); err != nil {
		return Outcome{}, err
	}

	current.Regenerate()

	return Outcome{methods: methods}, nil
}

// pending returns the unexpired pending login of current. An absent, unreadable
// or expired one is [ErrUnauthenticated]; expiry also clears it so a stale
// challenge cannot linger.
func (guard *Guard) pending(current *session.Session) (pendingLogin, error) {
	waiting, err := current.Get(pendingField)

	if missing(err) {
		return waiting, ErrUnauthenticated
	}

	if err != nil {
		return waiting, err
	}

	if !time.Now().Before(waiting.Expires) {
		if err := current.Delete(pendingField); err != nil {
			return waiting, err
		}

		return waiting, ErrUnauthenticated
	}

	return waiting, nil
}

// pendingUser returns the pending login of current and its user, loaded from the
// provider. A user deleted or disabled during the challenge, or whose
// [UserRecord.Version] differs from the one seen at [Guard.Begin] (compared in
// constant time), discards the pending login and is [ErrUnauthenticated]. It does
// not look at the attempt counter: [Guard.check] reserves attempts and reports
// the lockout, and [Guard.Pending] reads it.
func (guard *Guard) pendingUser(r *http.Request, current *session.Session) (pendingLogin, UserRecord, error) {
	waiting, err := guard.pending(current)

	if err != nil {
		return waiting, UserRecord{}, err
	}

	user, err := guard.provider.Find(r.Context(), waiting.Subject)

	if err != nil && !gone(err) {
		return waiting, UserRecord{}, err
	}

	if gone(err) || subtle.ConstantTimeCompare([]byte(waiting.Version), []byte(hashVersion(user.Version))) != 1 {
		if err := current.Delete(pendingField); err != nil {
			return waiting, UserRecord{}, err
		}

		return waiting, UserRecord{}, ErrUnauthenticated
	}

	return waiting, user, nil
}

// Pending returns the login parked by [Guard.Begin]: the subject and the
// second-factor methods the user can use now. It is read-only: it spends no
// attempt, so a page can call it freely to render the choice of methods (see
// [Guard.PendingHandler]). It returns [ErrUnauthenticated] when there is no
// pending login, it expired (which also clears it), the user was deleted or
// disabled, its [UserRecord.Version] changed (for example by a password change)
// in the meantime (which discards it), or the user is locked out by
// [GuardBuilder.MaxAttempts]. Other provider, store and Enrolled errors pass
// through.
//
// Example:
//
//	pending, err := guard.Pending(r)
//	if err != nil {
//		return err
//	}
//	fmt.Println(pending.Methods)
func (guard *Guard) Pending(r *http.Request) (PendingLogin, error) {
	current, err := guard.session(r)

	if err != nil {
		return PendingLogin{}, err
	}

	waiting, _, err := guard.pendingUser(r, current)

	if err != nil {
		return PendingLogin{}, err
	}

	// Reading, not reserving: the user is locked out once no attempt is left.
	failures, err := guard.store.Get(r.Context(), guard.attempts.Key(waiting.Subject))

	if err != nil && !errors.Is(err, cache.ErrNotFound) {
		return PendingLogin{}, err
	}

	locked, err := guard.LockedOut(r.Context(), waiting.Subject)

	if err != nil {
		return PendingLogin{}, err
	}

	if locked || failures >= int64(guard.settings.MaxAttempts) {
		if err := current.Delete(pendingField); err != nil {
			return PendingLogin{}, err
		}

		return PendingLogin{}, ErrUnauthenticated
	}

	methods, err := enrolled(r.Context(), waiting.Subject, guard.settings.SecondFactors)

	if err != nil {
		return PendingLogin{}, err
	}

	return PendingLogin{Subject: waiting.Subject, Methods: methods}, nil
}

// PendingHandler returns the handler of the route that tells a client which
// second-factor methods it can use: GET answers 200 with {"methods":["otp"]}. It
// calls [Guard.Pending], so it spends no attempt, and returns its errors; an
// anonymous request without a pending login gets [ErrUnauthenticated]. Mount it
// on the same router stage as the login route, without [Require].
//
// Example:
//
//	web.Get("/login/second-factor", guard.PendingHandler(), auth.PendingOperation)
func (guard *Guard) PendingHandler() router.Handler {
	return func(w http.ResponseWriter, r *http.Request) error {
		pending, err := guard.Pending(r)

		if err != nil {
			return err
		}

		return response.JSON(w, http.StatusOK, struct {
			Methods []Method `json:"methods"`
		}{pending.Methods})
	}
}

// check runs one credential check for subject against credentials: it decodes the
// request body, finds the method it names, reserves an attempt, asks the
// credential whether the user is enrolled and calls its Verify. It is the shared
// body of [Guard.ChallengeHandler] and [Guard.ConfirmHandler]. It returns the
// verified factor (method and kind filled from the credential, not yet stamped)
// and the user's attempt count after the reservation.
//
// An unknown method, or one without Verify, is [ErrUnknownMethod] before any
// attempt is reserved. The attempt is then reserved with an atomic increment
// before anything is verified, so concurrent requests each get a distinct count
// and at most [GuardBuilder.MaxAttempts] of them pass: a read followed by a later
// increment would let every request that read the counter before the limit
// through. A count over the limit, or a lockout in force (see [Guard.reject]),
// returns errLockedOut and keeps the reservation. stepUp says the check belongs to
// [Guard.ConfirmHandler]; it only changes how [Guard.reject] counts a wrong code.
// A method the user is not enrolled in is [ErrInvalidCredential], counted like a
// wrong code so the two cannot be told apart. An error wrapping
// [ErrInvalidCredential] from Verify stays counted; any other error from Enrolled
// or Verify refunds the attempt and is returned unchanged.
func (guard *Guard) check(r *http.Request, subject string, credentials []Credential, stepUp bool) (Factor, int64, error) {
	// core/http/request imports this package, so the body is decoded here
	// instead of with request.JSON, with its own size limit. Every decoding
	// failure, including an oversized body, is a 400 here; the body is never
	// partly trusted, and no attempt is reserved yet.
	var body map[string]jsontext.Value

	if err := json.UnmarshalRead(http.MaxBytesReader(nil, r.Body, maxCredentialBody), &body); err != nil {
		return Factor{}, 0, problem.BadRequest().Derive(func(details *problem.Builder) {
			details.Detail("The request body is not a JSON object.")
			details.Error(err)
		})
	}

	var method Method

	if raw, ok := body["method"]; !ok || json.Unmarshal(raw, &method) != nil {
		return Factor{}, 0, ErrUnknownMethod
	}

	index := slices.IndexFunc(credentials, func(credential Credential) bool {
		return credential.Method == method && credential.Verify != nil
	})

	if index < 0 {
		return Factor{}, 0, ErrUnknownMethod
	}

	credential := credentials[index]

	delete(body, "method")

	input, err := json.Marshal(body)

	if err != nil {
		return Factor{}, 0, err
	}

	ctx := r.Context()

	locked, err := guard.LockedOut(ctx, subject)

	if err != nil {
		return Factor{}, 0, err
	}

	if locked {
		return Factor{}, 0, errLockedOut
	}

	failures, _, err := guard.store.IncrementWithTTL(ctx, guard.attempts.Key(subject), 1, guard.settings.ChallengeTTL)

	if err != nil {
		return Factor{}, 0, err
	}

	if failures > int64(guard.settings.MaxAttempts) {
		return Factor{}, failures, errLockedOut
	}

	// An infrastructure failure, unlike a wrong code, is not the user's doing, so
	// the attempt goes back.
	refund := func(cause error) (Factor, int64, error) {
		guard.refund(ctx, subject)

		return Factor{}, failures, cause
	}

	ok, err := credential.Enrolled(ctx, subject)

	if err != nil {
		return refund(err)
	}

	if !ok {
		return Factor{}, failures, guard.reject(ctx, subject, failures, stepUp, ErrInvalidCredential)
	}

	factor, err := credential.Verify(r, subject, input)

	if errors.Is(err, ErrInvalidCredential) {
		return Factor{}, failures, guard.reject(ctx, subject, failures, stepUp, err)
	}

	if err != nil {
		return refund(err)
	}

	factor.Method = credential.Method

	if factor.Kind == "" {
		factor.Kind = credential.Kind
	}

	return factor, failures, nil
}

// ChallengeHandler returns the handler of the route that finishes a parked login
// (see [Guard.Begin]). It takes a JSON body {"method": "otp", ...}: "method"
// names one of the methods registered with [GuardBuilder.SecondFactor] and the
// other members are that method's input, passed to [Credential.Verify]. In order,
// it:
//
//  1. finds the pending login, answering [ErrUnauthenticated] without one;
//  2. decodes the body and answers [ErrUnknownMethod] (422)
//     for a missing, unregistered or redirect-only method;
//  3. reserves one of the user's [GuardBuilder.MaxAttempts] attempts, shared
//     across all methods and sessions, and answers [ErrUnauthenticated] once
//     they are used up, discarding the pending login;
//  4. answers [ErrInvalidCredential] for a method the user is not enrolled in, as
//     for a wrong code;
//  5. calls Verify. A wrong code ([ErrInvalidCredential]) keeps the attempt
//     counted, rotates the session ID, and discards the pending login on the last
//     attempt; any other error refunds the attempt and is returned;
//  6. on success logs the user in with the factors proved at Begin plus the
//     verified one (creating the [LoginRecord]), clears the attempt count,
//     rotates the session ID and answers 204.
//
// The attempt limit lives in the shared store, not the session, so a new
// session, a new Begin or requests forking one pending cookie in parallel cannot
// reset or exceed it. A lockout lets anyone who knows the subject delay that
// user's second factor, so also rate-limit by address. The handler needs
// [session.Middleware] but not [Require].
//
// Example:
//
//	web.Post("/login/second-factor", guard.ChallengeHandler(), auth.ChallengeOperation)
func (guard *Guard) ChallengeHandler() router.Handler {
	return func(w http.ResponseWriter, r *http.Request) error {
		current, err := guard.session(r)

		if err != nil {
			return err
		}

		waiting, user, err := guard.pendingUser(r, current)

		if err != nil {
			return err
		}

		factor, failures, err := guard.check(r, waiting.Subject, guard.settings.SecondFactors, false)

		if errors.Is(err, errLockedOut) {
			if err := current.Delete(pendingField); err != nil {
				return err
			}

			return ErrUnauthenticated
		}

		if errors.Is(err, ErrInvalidCredential) {
			if failures >= int64(guard.settings.MaxAttempts) {
				if deleteErr := current.Delete(pendingField); deleteErr != nil {
					return deleteErr
				}
			}

			current.Regenerate()

			return err
		}

		if err != nil {
			return err
		}

		if err := guard.start(r, current, user, withFactor(waiting.Factors, factor, time.Now())); err != nil {
			return err
		}

		if err := guard.store.Delete(r.Context(), guard.attempts.Key(waiting.Subject)); err != nil {
			return err
		}

		if err := guard.store.Delete(r.Context(), guard.budgets.Key(waiting.Subject)); err != nil {
			return err
		}

		return response.Status(w, http.StatusNoContent)
	}
}

// ConfirmHandler returns the handler of the route that lets a logged-in user
// prove themselves again. It works like [Guard.ChallengeHandler] against the
// methods registered with [GuardBuilder.StepUp], with the same body, order,
// shared attempt count and refund rule, except that there is no pending login:
// it requires an [Identity] with [Identity.Session] in the request context
// (install [Middleware] with [Guard.Authenticator] before it), answering
// [ErrUnauthenticated] otherwise, so a bearer token can never confirm. A user who
// used up their attempts, or who is locked out (the lockout blocks step-up too,
// and lasts up to 24 hours; see [GuardBuilder.MaxAttempts] and [Guard.Unlock]),
// gets [ErrInvalidCredential], like a wrong code. On
// success it records the factor on the session (a factor with the same [Method]
// and [Kind] replaces the older one, so [Guard.Fresh] passes), gives back the
// attempt it reserved, rotates the session ID and answers 204. It leaves the
// count of wrong codes alone, so confirming one method never resets the limit on
// guessing another. The session this guard holds must be logged in as the
// identity's subject, otherwise the answer is [ErrUnauthenticated].
//
// Example:
//
//	web.With(auth.RequireSession()).Post("/confirm", guard.ConfirmHandler(), auth.ConfirmOperation)
func (guard *Guard) ConfirmHandler() router.Handler {
	return func(w http.ResponseWriter, r *http.Request) error {
		identity, ok := FromContext(r.Context())

		if !ok || !identity.Session() {
			return ErrUnauthenticated
		}

		// The identity may come from another guard's authenticator, so the session
		// this guard writes to must hold the same user.
		stored, _, err := guard.valid(r)

		if errors.Is(err, ErrNoCredentials) || err == nil && stored.Subject != identity.Subject() {
			return ErrUnauthenticated
		}

		if err != nil {
			return err
		}

		factor, _, err := guard.check(r, identity.Subject(), guard.settings.StepUps, true)

		if errors.Is(err, errLockedOut) {
			return ErrInvalidCredential
		}

		if err != nil {
			return err
		}

		if err := guard.confirm(r, factor); err != nil {
			return err
		}

		// A confirmation does not clear the count: whoever holds the session and
		// one credential could otherwise reset the limit and keep guessing another.
		// The attempt it reserved goes back instead, so confirming costs nothing.
		guard.refund(r.Context(), identity.Subject())

		return response.Status(w, http.StatusNoContent)
	}
}

// LockedOut reports whether subject is inside a second-factor lockout, the
// escalating block that [Guard.ChallengeHandler] and [Guard.ConfirmHandler]
// start when a user uses up their [GuardBuilder.MaxAttempts] (see
// [SecondFactorLocked]). While it lasts every challenge or confirmation of the
// subject fails without verifying anything, in any session. It only reads the
// lockout entry, which expires on its own: it reserves no attempt, counts no
// failure and never starts or extends a lockout, so it is safe to call on every
// request. A cache failure is returned with false; decide for yourself whether to
// fail closed.
//
// Use it for control flow, such as a login route that must not refund a login
// throttle while the user is locked, because a correct password then only parks
// a pending login that cannot finish. Do not use [SecondFactorLocked] for that:
// [event.Bus.Emit] logs listener errors instead of returning them, so a failing
// listener would fail open.
//
// Example:
//
//	locked, err := guard.LockedOut(ctx, subject)
//	if err != nil {
//		return err
//	}
//	if locked {
//		return errors.New("second factor locked")
//	}
func (guard *Guard) LockedOut(ctx context.Context, subject string) (bool, error) {
	_, err := guard.store.Get(ctx, guard.locks.Key(subject))

	if errors.Is(err, cache.ErrNotFound) {
		return false, nil
	}

	return err == nil, err
}

// reject is called with every wrong credential check of subject and always
// returns cause, so the handler's wrong-code cleanup (discarding the pending
// login, rotating the session ID) runs whatever happens to the cache. Each call
// adds one failure to the subject's 24 hour budget ([budgetTTL]), which is what
// escalates: with n = (budget-1) / MaxAttempts, the lockout lasts the challenge
// window doubled n times, capped at [maxLockout] (or at the window when that is
// longer). A lockout starts when the window's attempts are used up (failures >=
// MaxAttempts) or when the budget reaches a multiple of MaxAttempts, so an
// attacker who stays under the limit in every window is locked too, after
// enough windows.
//
// The lock is written with [cache.Cache.Add] so that, when several requests see
// the condition at once, one creates it and publishes [SecondFactorLocked]; the
// others find it in place and publish nothing. A wrong step-up code (stepUp) only
// counts once it uses up the window's attempts; see the first lines of the body.
// The writes ignore cancellation of ctx. A cache failure while counting or
// locking is logged and swallowed: it must not turn a wrong code into a server
// error, and the window counter has already refused further checks past the
// limit.
func (guard *Guard) reject(ctx context.Context, subject string, failures int64, stepUp bool, cause error) error {
	limit := int64(guard.settings.MaxAttempts)

	// A wrong step-up code of a logged-in user is a typo far more often than a
	// guess, so it escalates only once it has used up the window's attempts,
	// where it is a lockout anyway. Wrong second-factor codes always count.
	if stepUp && failures < limit {
		return cause
	}

	// The counters and the lock are written even when the request is canceled
	// (a client that hangs up after a wrong code must not skip the lockout), like
	// the refund of an attempt.
	ctx = context.WithoutCancel(ctx)

	budget, _, err := guard.store.IncrementWithTTL(ctx, guard.budgets.Key(subject), 1, budgetTTL)

	if err != nil {
		slog.ErrorContext(ctx, "auth: count second-factor failure", "error", err)

		return cause
	}

	if failures < limit && budget%limit != 0 {
		return cause
	}

	window := guard.settings.ChallengeTTL
	longest := max(window, maxLockout)
	duration := window

	for range (budget - 1) / limit {
		if duration >= longest {
			break
		}

		duration *= 2
	}

	duration = min(duration, longest)
	until := time.Now().Add(duration).UTC()

	added, err := guard.store.Add(ctx, guard.locks.Key(subject, func(entry *cache.KeyBuilder) { entry.TTL(duration) }), until)

	if err != nil {
		slog.ErrorContext(ctx, "auth: lock second factor", "error", err)

		return cause
	}

	if added {
		guard.settings.Events.Emit(ctx, SecondFactorLocked, SecondFactorLockedEvent{Subject: subject, Until: until})
	}

	return cause
}

// Unlock ends the second-factor lockout of subject at once and forgets its
// escalation: it deletes the lockout, the 24 hour failure budget and the current
// window's attempt count, so the user has [GuardBuilder.MaxAttempts] fresh
// attempts and the next lockout starts again at one window. Call it where the
// application has proven the user another way: after a password reset, from an
// administrator's console, or from a recovery flow. It is safe to call for a
// subject that is not locked.
//
// A lockout is not only an attacker's slowdown: anyone who holds the password can
// start pending logins and spend the attempts, chaining lockouts of up to 24
// hours each, which locks the real user out of the second factor and of step-up.
// Treat a [SecondFactorLocked] event as a sign that the password is known to
// someone else, and reset it when you unlock. It returns the first cache error.
// The lockout entry is deleted last, so a partial failure leaves the user locked
// (never unlocked with the old escalation still counted); call it again.
//
// Example:
//
//	if err := guard.Unlock(ctx, subject); err != nil {
//		return err
//	}
func (guard *Guard) Unlock(ctx context.Context, subject string) error {
	if err := guard.store.Delete(ctx, guard.budgets.Key(subject)); err != nil {
		return err
	}

	if err := guard.store.Delete(ctx, guard.attempts.Key(subject)); err != nil {
		return err
	}

	return guard.store.Delete(ctx, guard.locks.Key(subject))
}

// refund gives one reserved attempt of subject back. It adds -1 with a TTL
// instead of calling Decrement, which on a counter that expired or was cleared
// meanwhile would create one that never expires. Its error is ignored on
// purpose: a failed refund leaves the attempt counted, which is the safe side,
// and cannot change the outcome of a request that already succeeded or failed.
// The context is detached so a canceled request still gives the attempt back.
func (guard *Guard) refund(ctx context.Context, subject string) {
	_, _, _ = guard.store.IncrementWithTTL(context.WithoutCancel(ctx), guard.attempts.Key(subject), -1, guard.settings.ChallengeTTL)
}

// confirm records freshly verified factors on the logged-in session and
// regenerates the session ID. A factor with the same method and kind as a
// recorded one replaces it (see withFactor). It returns [ErrUnauthenticated] when
// the session holds no logged-in user. It does not verify anything: callers call
// it only after the factors checked out.
func (guard *Guard) confirm(r *http.Request, factors ...Factor) error {
	current, err := guard.session(r)

	if err != nil {
		return err
	}

	stored, err := current.Get(sessionField)

	if missing(err) {
		return ErrUnauthenticated
	}

	if err != nil {
		return err
	}

	now := time.Now()

	for _, factor := range factors {
		stored.Factors = withFactor(stored.Factors, factor, now)
	}

	if err := current.Put(sessionField, stored); err != nil {
		return err
	}

	current.Regenerate()

	return nil
}

// MustAccept panics unless method is registered in the [GuardBuilder.SecondFactor]
// or [GuardBuilder.StepUp] registry. A module that cannot work without its
// method being accepted by the application's guard (a TOTP module whose
// enrolment routes assume challenges accept "otp") calls it at composition time,
// so a miswired application fails at startup instead of leaving users unable to
// log in. It reads only the immutable settings and is safe for concurrent use.
//
// Example:
//
//	guard.MustAccept(auth.MethodOTP)
func (guard *Guard) MustAccept(method Method) {
	registered := func(credential Credential) bool { return credential.Method == method }

	if slices.ContainsFunc(guard.settings.SecondFactors, registered) || slices.ContainsFunc(guard.settings.StepUps, registered) {
		return
	}

	panic(fmt.Sprintf("auth: guard does not accept method %q", method))
}
