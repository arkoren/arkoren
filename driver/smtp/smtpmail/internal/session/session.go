// Package session owns one SMTP connection, security negotiation, and submission.
package session

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/smtp"
	"strconv"

	"arkoren.dev/core/service/mail"
)

// Client captures one submission's transport policy. The caller retains the
// dialer; Submit owns and closes the connection returned by that dialer.
type Client struct {
	// Host is the configured hostname used for dialing, TLS, and authentication.
	Host string
	// Port is the configured TCP port.
	Port uint16
	// Username is empty when authentication is disabled.
	Username string
	// Password is used only for encrypted SMTP authentication.
	Password string
	// StartTLS requires an advertised STARTTLS upgrade before authentication.
	StartTLS bool
	// ImplicitTLS secures the connection before the initial SMTP greeting.
	ImplicitTLS bool
	// Dial opens the connection without changing the caller's dial policy.
	Dial func(context.Context, string, string) (net.Conn, error)
}

// Submit owns one connection for the complete SMTP exchange. Closing it
// on cancellation interrupts net/smtp, whose operations do not accept contexts.
// The cancellation callback remains active through negotiation, DATA and QUIT;
// stopping it on completion avoids retaining the connection until ctx ends.
func (client *Client) Submit(ctx context.Context, message mail.Message, contents []byte) error {
	connection, err := client.dialConnection(ctx)

	if err != nil {
		return err
	}

	defer connection.Close()

	// stop releases the watcher on ordinary completion. The connection remains
	// owned here; concurrent Close calls from cancellation and cleanup are safe.
	stop := context.AfterFunc(ctx, func() {
		_ = connection.Close() // Best-effort interruption; the operation reports its error.
	})
	defer stop()

	server, err := client.openServer(ctx, connection)

	if err != nil {
		return err
	}

	defer func() {
		_ = server.Quit() // Acceptance is already known; goodbye cannot revoke it.
	}()

	return sendMessage(server, message, contents)
}

// openServer negotiates SMTP over a connection owned by Submit. Its
// deadline bounds socket I/O independently of the cancellation callback; setup
// failures leave connection cleanup to the owner, without a blocking QUIT.
func (client *Client) openServer(ctx context.Context, connection net.Conn) (*smtp.Client, error) {
	if deadline, ok := ctx.Deadline(); ok {
		if err := connection.SetDeadline(deadline); err != nil {
			return nil, err
		}
	}

	server, err := client.newServer(connection)

	if err != nil {
		return nil, err
	}

	if err := client.configureServer(server); err != nil {
		return nil, err
	}

	return server, nil
}

// newServer creates an SMTP client over connection with implicit TLS when required.
func (client *Client) newServer(connection net.Conn) (*smtp.Client, error) {
	if client.ImplicitTLS {
		connection = tls.Client(connection, client.tlsConfig())
	}

	server, err := smtp.NewClient(connection, client.Host)

	if err != nil {
		return nil, fmt.Errorf("create smtp client: %w", err)
	}

	return server, nil
}

// dialConnection opens an SMTP network connection.
func (client *Client) dialConnection(ctx context.Context) (net.Conn, error) {
	connection, err := client.Dial(ctx, "tcp", net.JoinHostPort(client.Host, strconv.Itoa(int(client.Port))))

	if err != nil {
		return nil, fmt.Errorf("dial smtp server: %w", err)
	}

	return connection, nil
}

// configureServer enables configured transport security and authentication.
func (client *Client) configureServer(server *smtp.Client) error {
	if err := client.enableStartTLS(server); err != nil {
		return err
	}

	if err := client.authenticate(server); err != nil {
		return err
	}

	return nil
}

// enableStartTLS upgrades server when the configuration requires STARTTLS.
func (client *Client) enableStartTLS(server *smtp.Client) error {
	if !client.StartTLS {
		return nil
	}

	if ok, _ := server.Extension("STARTTLS"); !ok {
		return errors.New("smtp server does not support starttls")
	}

	if err := server.StartTLS(client.tlsConfig()); err != nil {
		return fmt.Errorf("start smtp tls: %w", err)
	}

	return nil
}

// authenticate authenticates server when SMTP credentials are configured.
func (client *Client) authenticate(server *smtp.Client) error {
	if client.Username == "" {
		return nil
	}

	auth := smtp.PlainAuth("", client.Username, client.Password, client.Host)

	if err := server.Auth(auth); err != nil {
		return fmt.Errorf("authenticate smtp client: %w", err)
	}

	return nil
}

// tlsConfig returns the TLS configuration shared by SMTP transport modes.
func (client *Client) tlsConfig() *tls.Config {
	return &tls.Config{
		ServerName: client.Host,
		MinVersion: tls.VersionTLS12,
	}
}

// sendMessage submits the envelope and serialized contents to server.
func sendMessage(server *smtp.Client, message mail.Message, contents []byte) error {
	if err := setEnvelope(server, message); err != nil {
		return err
	}

	return writeMessage(server, contents)
}

// setEnvelope sets the validated message's SMTP sender and envelope recipients.
// It includes BCC recipients without adding a MIME header and wraps the first
// server rejection with the failed envelope operation.
func setEnvelope(server *smtp.Client, message mail.Message) error {
	if err := server.Mail(message.From().Email); err != nil {
		return fmt.Errorf("set smtp sender: %w", err)
	}

	for _, recipient := range recipients(message) {
		if err := server.Rcpt(recipient.Email); err != nil {
			return fmt.Errorf("set smtp recipient %q: %w", recipient.Email, err)
		}
	}

	return nil
}

// writeMessage writes and completes the SMTP message body.
func writeMessage(server *smtp.Client, contents []byte) error {
	writer, err := server.Data()

	if err != nil {
		return fmt.Errorf("open smtp message body: %w", err)
	}

	if _, err := writer.Write(contents); err != nil {
		return fmt.Errorf("write smtp message body: %w", err)
	}

	if err := writer.Close(); err != nil {
		return fmt.Errorf("complete smtp message body: %w", err)
	}

	return nil
}

// recipients returns a detached envelope list in To, CC, BCC order. The To
// getter already returns owned storage, so appending cannot mutate message.
func recipients(message mail.Message) []mail.Address {
	return append(append(message.To(), message.CC()...), message.BCC()...)
}
