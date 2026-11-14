package auth

import (
	"crypto/sha256"
	"encoding/hex"
	"slices"
	"time"

	"arkoren.dev/core/http/session"
)

// Session fields written by [Guard]. The logged-in user and the pending login
// live under separate keys so the authenticator never reads a half-finished
// login. Sessions written under the former "arkoren:auth:user" key are not read
// and are anonymous.
var (
	// sessionField holds the [sessionRecord] of the logged-in user.
	sessionField = session.NewField[sessionRecord]("arkoren:auth:session")

	// pendingField holds the [pendingLogin] waiting for a second factor.
	pendingField = session.NewField[pendingLogin]("arkoren:auth:pending")
)

// sessionRecord is everything a logged-in session stores, as one value under one
// key so that subject, factors and version can never be written apart.
type sessionRecord struct {
	// Subject is the logged-in user's subject.
	Subject string `json:"subject"`

	// Factors are the verified factors, one per [Method], with the time each was
	// last verified.
	Factors []Factor `json:"factors"`

	// Version is the hex SHA-256 of [UserRecord.Version] at login. The
	// authenticator ends the session when the user's version no longer hashes to
	// it. The hash keeps the raw value out of session storage.
	Version string `json:"version"`

	// LoginID identifies the session's [LoginRecord]. It survives session ID
	// rotation; the authenticator ends the session when the record is gone.
	LoginID string `json:"login_id,omitempty"`
}

// pendingLogin is a login whose first factor passed and whose second is awaited.
type pendingLogin struct {
	// Subject is the user awaiting the second factor.
	Subject string `json:"subject"`

	// Factors are the factors verified so far.
	Factors []Factor `json:"factors"`

	// Expires is when the pending login stops being accepted.
	Expires time.Time `json:"expires"`

	// Version is the hex SHA-256 of [UserRecord.Version] seen at [Guard.Begin].
	// The pending login is discarded when the user's version no longer matches.
	Version string `json:"version"`
}

// hashVersion returns the hex SHA-256 of a [UserRecord.Version]. All hashes have
// the same length, which keeps the comparison in the authenticator constant time.
func hashVersion(version string) string {
	sum := sha256.Sum256([]byte(version))

	return hex.EncodeToString(sum[:])
}

// withFactor returns a copy of factors in which factor replaces the factor of
// the same method, or is appended, so each method appears once with its latest
// time. A zero At becomes now, in UTC. It never modifies factors.
func withFactor(factors []Factor, factor Factor, now time.Time) []Factor {
	if factor.At.IsZero() {
		factor.At = now
	}

	factor.At = factor.At.UTC()
	factors = slices.Clone(factors)

	if i := slices.IndexFunc(factors, func(existing Factor) bool { return existing.Method == factor.Method }); i >= 0 {
		factors[i] = factor

		return factors
	}

	return append(factors, factor)
}
