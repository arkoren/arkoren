package smtpmail

import (
	"errors"
	"time"
)

// BuilderFunc edits one SMTP construction draft. New runs supplied callbacks
// once in order before validation; nil entries panic. The alias accepts ordinary
// functions and function slices without conversion and transfers no draft ownership.
//
// Example:
//
//	var local smtpmail.BuilderFunc = func(transport *smtpmail.Builder) {
//		transport.TLSMode(smtpmail.TLSModeNone)
//	}
//	driver, err := smtpmail.New("localhost", 1025, local)
//	if err != nil {
//		panic(err)
//	}
//	_ = mail.New(driver)
type BuilderFunc = func(*Builder)

// Builder edits one SMTP construction draft. Its zero value selects STARTTLS
// without authentication. New copies its settings after all callbacks complete;
// retaining this draft does not grant access to the published driver.
//
// Example:
//
//	driver, err := smtpmail.New("smtp.example.com", 587, func(transport *smtpmail.Builder) {
//		transport.Username("user").Password("secret")
//	})
//	if err != nil {
//		panic(err)
//	}
//	_ = driver
type Builder struct {
	// config owns the draft's scalar settings, copied at publication.
	config Config
}

// Username replaces the authentication username and returns the same draft.
// Empty disables authentication only when Password is also empty. Credentials
// require encrypted transport; New checks the completed pair after callbacks.
//
// Example:
//
//	driver, err := smtpmail.New("smtp.example.com", 587, func(transport *smtpmail.Builder) {
//		transport.Username("user").Password("secret")
//	})
//	if err != nil {
//		panic(err)
//	}
//	_ = driver
func (transport *Builder) Username(username string) *Builder {
	transport.config.Username = username

	return transport
}

// Password replaces the authentication password and returns the same draft.
// Empty requires an empty Username. New rejects incomplete credentials and
// plaintext authentication; passwords are never included in Config formatting.
//
// Example:
//
//	driver, err := smtpmail.New("smtp.example.com", 587, func(transport *smtpmail.Builder) {
//		transport.Username("user").Password("secret")
//	})
//	if err != nil {
//		panic(err)
//	}
//	_ = driver
func (transport *Builder) Password(password string) *Builder {
	transport.config.Password = password

	return transport
}

// TLSMode replaces the connection encryption policy and returns the same draft.
// Zero requires STARTTLS. TLSModeNone is only for unauthenticated trusted-local
// delivery; New rejects unknown modes and credentials with plaintext delivery.
//
// Example:
//
//	driver, err := smtpmail.New("localhost", 1025, func(transport *smtpmail.Builder) {
//		transport.TLSMode(smtpmail.TLSModeNone)
//	})
//	if err != nil {
//		panic(err)
//	}
//	_ = driver
func (transport *Builder) TLSMode(mode TLSMode) *Builder {
	transport.config.TLSMode = mode

	return transport
}

// Timeout replaces the bound on dialing and the whole SMTP exchange, returning
// the same draft. Zero uses DefaultTimeout; negative disables this bound.
// An earlier context deadline still takes precedence during Send.
//
// Example:
//
//	driver, err := smtpmail.New("smtp.example.com", 587, func(transport *smtpmail.Builder) {
//		transport.Timeout(10 * time.Second)
//	})
//	if err != nil {
//		panic(err)
//	}
//	_ = driver
func (transport *Builder) Timeout(timeout time.Duration) *Builder {
	transport.config.Timeout = timeout

	return transport
}

// TLSMode determines how [Driver] secures an SMTP connection. String values
// are suitable for configuration and JSON; empty selects [TLSModeStartTLS].
//
// Example:
//
//	mode := smtpmail.TLSMode("implicit")
//	_ = mode == smtpmail.TLSModeImplicit
type TLSMode string

const (
	// TLSModeStartTLS requires the server to upgrade the connection with
	// STARTTLS. It is the default, including when the configured mode is empty.
	//
	// Example:
	//
	//	mode := smtpmail.TLSModeStartTLS
	//	_ = string(mode) // "starttls"
	TLSModeStartTLS TLSMode = "starttls"

	// TLSModeImplicit starts the connection with TLS before SMTP negotiation.
	//
	// Example:
	//
	//	mode := smtpmail.TLSModeImplicit
	//	_ = string(mode) // "implicit"
	TLSModeImplicit TLSMode = "implicit"

	// TLSModeNone sends SMTP commands without transport encryption.
	//
	// It is an explicit opt-in for unauthenticated trusted-local delivery,
	// such as a Mailpit development server.
	//
	// Example:
	//
	//	mode := smtpmail.TLSModeNone
	//	_ = string(mode) // "none"
	TLSModeNone TLSMode = "none"
)

// DefaultTimeout bounds an SMTP exchange when the configured Timeout is zero.
// Zero selects this default; a negative configured value disables the driver's
// limit, but context cancellation and any context deadline still apply.
//
// Example:
//
//	driver, err := smtpmail.New("smtp.example.com", 587, func(transport *smtpmail.Builder) {
//		transport.Timeout(smtpmail.DefaultTimeout)
//	})
//	if err != nil {
//		panic(err)
//	}
//	_ = mail.New(driver)
const DefaultTimeout = 30 * time.Second

// Config is a reporting record for SMTP settings, not a constructor input.
// Configure [New] through [Builder]; its password remains redacted when printed.
//
// Example:
//
//	text := (smtpmail.Config{Username: "user", Password: "secret"}).String()
//	_ = text
type Config struct {
	// Username is the optional SMTP authentication username. It requires
	// encrypted transport. The default is empty, meaning no authentication.
	// Example: _ = smtpmail.Config{Username: "smtp-user"}.Username
	Username string

	// Password is the optional SMTP authentication password. It requires
	// encrypted transport. The default is empty, meaning no authentication.
	// Example: _ = smtpmail.Config{Password: "secret"}.String() // Redacted.
	Password string

	// TLSMode determines the required transport encryption mode. The default is
	// [TLSModeStartTLS]; plaintext needs an explicit [TLSModeNone], which
	// permits only unauthenticated trusted-local delivery.
	// Example: _ = smtpmail.Config{TLSMode: smtpmail.TLSModeStartTLS}.TLSMode
	TLSMode TLSMode

	// Timeout bounds dialing and the whole SMTP exchange when the context
	// has no earlier deadline. Zero uses [DefaultTimeout]; a negative value
	// disables the limit.
	// Example: _ = smtpmail.Config{Timeout: 10 * time.Second}.Timeout
	Timeout time.Duration
}

// validateConnectionConfig verifies the SMTP connection settings.
func validateConnectionConfig(host string, port uint16, config Config) error {
	if host == "" {
		return errors.New("smtp host is required")
	}

	if port == 0 {
		return errors.New("smtp port is required")
	}

	switch config.TLSMode {
	case TLSModeStartTLS, TLSModeImplicit, TLSModeNone:
	default:
		return errors.New("smtp tls mode is invalid")
	}

	return nil
}

// validateCredentials verifies SMTP authentication settings are complete and secure.
func validateCredentials(config Config) error {
	if (config.Username == "") != (config.Password == "") {
		return errors.New("smtp username and password must both be set")
	}

	if config.Username != "" && config.TLSMode == TLSModeNone {
		return errors.New("smtp authentication requires encrypted transport")
	}

	return nil
}
