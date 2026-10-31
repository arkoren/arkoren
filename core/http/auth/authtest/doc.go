// Package authtest checks [auth.Authenticator] implementations against the
// rules every authenticator shares. Call [Run] from the authenticator's tests
// with factories for a request without credentials, one with valid
// credentials, and, through [WithInvalid], one with rejected credentials.
package authtest
