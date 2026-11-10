package auth

import (
	"net/http"
	"slices"
	"time"

	"arkoren.dev/core/http/session"
	"arkoren.dev/foundation/problem"
)

// Session keys written by [SessionIdentity]. A realm appends ":" and its name,
// so the default realm keeps the keys that sessions created before realms
// existed already use.
const (
	// userKey holds the logged-in user's identifier of type K.
	userKey = "arkoren:auth:user"

	// stateKey holds the [storedState]: the factors used and the credential
	// version recorded at login.
	stateKey = "arkoren:auth:state"

	// pendingKey holds a [pendingLogin]: a first factor passed, a second
	// outstanding. The authenticator never reads it.
	pendingKey = "arkoren:auth:pending"
)

// defaultChallengeTTL is how long a pending login waits for its next factor
// when [SessionBuilder.ChallengeTTL] is unset.
const defaultChallengeTTL = 5 * time.Minute

// Method names how a user proved their identity. Values follow the RFC 8176
// authentication method reference registry where one exists; the two values
// marked otherwise are Arkoren's own. The empty string is not a method.
//
// Example:
//
//	err := identity.Login(current, user, auth.MethodPassword, auth.MethodOTP)
type Method string

const (
	// MethodPassword is a knowledge factor checked against a stored password
	// hash (RFC 8176 "pwd").
	MethodPassword Method = "pwd"

	// MethodOTP is a one-time password such as TOTP (RFC 8176 "otp").
	MethodOTP Method = "otp"

	// MethodHardwareKey is a hardware-bound key such as a security key
	// (RFC 8176 "hwk").
	MethodHardwareKey Method = "hwk"

	// MethodSoftwareKey is a software-held key such as a platform passkey
	// (RFC 8176 "swk").
	MethodSoftwareKey Method = "swk"

	// MethodFederated is a login completed by an external identity provider,
	// such as OAuth or OIDC. It is not an RFC 8176 value.
	MethodFederated Method = "federated"

	// MethodRecovery is a single-use recovery code. It is not an RFC 8176 value.
	MethodRecovery Method = "recovery"
)

// Factor records one method a session's user proved and when.
//
// Example:
//
//	state, err := identity.State(current)
//	if err != nil {
//		return err
//	}
//	for _, factor := range state.Factors {
//		_ = factor.Method
//	}
type Factor struct {
	// Method is how the user authenticated.
	Method Method `json:"method"`

	// At is when the method was last proved, in UTC. [SessionIdentity.Confirm]
	// refreshes it.
	At time.Time `json:"at"`
}

// State describes how the current session's user authenticated. It is a
// detached copy: changing it does not change the session.
//
// Example:
//
//	state, err := identity.State(current)
//	if err != nil {
//		return err
//	}
//	_ = len(state.Factors) // zero for a session created before factors existed
type State struct {
	// Factors lists each distinct method once, in the order first proved, with
	// the time it was last proved. It is empty for sessions that carry no state.
	Factors []Factor
}

// Versioned is implemented by user types whose credentials can change. When the
// user type of a [SessionIdentity] implements it, Login stores a SHA-256 digest
// of AuthVersion and the authenticator compares it with the loaded user's on
// every request: a mismatch, or a session without a stored version, is
// [ErrNoCredentials]. Return a value that changes with the credentials, such as
// the time the password last changed, to end every other session of that user.
//
// Enabling it on an existing application ends sessions created before it,
// because they carry no stored version. Pending logins are unaffected until
// completed.
//
// Example:
//
//	func (user User) AuthVersion() string {
//		return user.PasswordChangedAt.UTC().Format(time.RFC3339Nano)
//	}
type Versioned interface {
	// AuthVersion returns an opaque value that changes whenever the user's
	// sessions should end. It is hashed before storage.
	AuthVersion() string
}

// ErrStepUpRequired is the 403 problem returned by [SessionIdentity.RequireMethods]
// and [SessionIdentity.RequireFresh] when an authenticated request has not
// proved the required methods recently enough. A client should prompt for the
// factor and retry. It is 403, not 401, because the user is authenticated and a
// 401 invites login redirects that would discard the session.
//
// Example:
//
//	if errors.Is(err, auth.ErrStepUpRequired) {
//		// Ask the user to confirm, then retry.
//	}
var ErrStepUpRequired = problem.New(func(details *problem.Builder) {
	details.Type("tag:arkoren.dev,2026-11:core:http/auth:step-up-required")
	details.Title("Step-up required")
	details.Detail("Additional authentication is required to access this resource.")
	details.Status(http.StatusForbidden)
})

// storedState is the persisted form of [State] plus the credential version.
type storedState struct {
	// Factors are the proved methods; see [State.Factors].
	Factors []Factor `json:"factors"`

	// Version is the hex SHA-256 of [Versioned.AuthVersion], empty when the
	// user type is not [Versioned].
	Version string `json:"version,omitempty"`
}

// pendingLogin is the persisted first-factor-passed state of [SessionIdentity.Challenge].
type pendingLogin[K any] struct {
	// ID is the identifier of the user awaiting the next factor.
	ID K `json:"id"`

	// Factors are the methods already proved, with their times.
	Factors []Factor `json:"factors"`

	// Expires is when the pending login stops being accepted.
	Expires time.Time `json:"expires"`
}

// record returns factors with each method refreshed to now or appended, so a
// method appears once. It never modifies factors.
func record(factors []Factor, now time.Time, methods ...Method) []Factor {
	factors = slices.Clone(factors)

	for _, method := range methods {
		if i := slices.IndexFunc(factors, func(factor Factor) bool { return factor.Method == method }); i >= 0 {
			factors[i].At = now

			continue
		}

		factors = append(factors, Factor{Method: method, At: now})
	}

	return factors
}

// field returns the session field for base in the identity's realm.
func field[T any](realm string, base string) session.Field[T] {
	if realm != "" {
		base += ":" + realm
	}

	return session.NewField[T](base)
}
