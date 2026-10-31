package smtpmail

import (
	"cmp"
	"context"
	"net"

	"arkoren.dev/core/service/mail"
)

// Driver submits [mail.Message] values through an SMTP server. Each Send owns a
// separate connection; construction makes no network request. A configured driver
// can be shared by mail facades, and formatting it never reveals its credentials.
//
// Example:
//
//	driver, err := smtpmail.New("smtp.example.com", 587)
//	if err != nil {
//		panic(err)
//	}
//	_ = mail.New(driver)
type Driver struct {
	// host is the validated SMTP server hostname.
	host string
	// port is the validated SMTP server TCP port.
	port uint16
	// config retains the validated optional SMTP configuration.
	config Config
	// dial opens SMTP network connections and is replaceable by tests.
	dial func(context.Context, string, string) (net.Conn, error)
}

// New creates an SMTP [Driver] for the server at host and port, with optional
// settings from configure.
//
// Callbacks run directly once, synchronously in order on one fresh [Builder],
// before validation. Nil entries panic; expanded nil or empty slices omit edits.
// The completed settings are copied, so retaining the draft cannot change driver.
//
// host is the SMTP server hostname, also used for TLS server name verification
// and message ID generation; port is its TCP port (for example 587 for
// STARTTLS or 465 for implicit TLS). No connection is made until
// [Driver.Send].
//
// The connection is encrypted with STARTTLS unless [Builder.TLSMode] says
// otherwise; plaintext requires an explicit [TLSModeNone].
//
// New returns an error when host is empty, port is zero, the TLS mode is
// invalid, only one of Username and Password is set, or credentials are set
// with [TLSModeNone].
//
// Example:
//
//	driver, err := smtpmail.New("smtp.example.com", 587, func(transport *smtpmail.Builder) {
//		transport.Username("user")
//		transport.Password("secret")
//	})
//	if err != nil {
//		panic(err)
//	}
//	_ = driver
func New(host string, port uint16, configure ...BuilderFunc) (*Driver, error) {
	transport := &Builder{}

	for _, configure := range configure {
		configure(transport)
	}

	config := transport.config
	config.TLSMode = cmp.Or(config.TLSMode, TLSModeStartTLS)

	if err := validateConnectionConfig(host, port, config); err != nil {
		return nil, err
	}

	if err := validateCredentials(config); err != nil {
		return nil, err
	}

	dialer := &net.Dialer{}

	return &Driver{
		host:   host,
		port:   port,
		config: config,
		dial:   dialer.DialContext,
	}, nil
}

var _ mail.Driver = (*Driver)(nil)
