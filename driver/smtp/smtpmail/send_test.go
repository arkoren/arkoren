package smtpmail_test

import (
	"context"
	"errors"
	"net"
	"net/textproto"
	"strings"
	"sync"
	"testing"
	"time"

	"arkoren.dev/core/service/mail"
	"arkoren.dev/driver/smtp/smtpmail"
)

// TestSendCancellationInterruptsEnvelope cancels only after a loopback SMTP
// server reads MAIL FROM, proving the connection and greeting have completed.
// Send must stop a stalled envelope exchange and preserve context.Canceled.
func TestSendCancellationInterruptsEnvelope(t *testing.T) {
	t.Parallel()

	assertSMTPCancellation(t, smtpmail.TLSModeNone, func(server *textproto.Conn) {
		_ = server.PrintfLine("220 fixture ESMTP")
		expectSMTPCommand(t, server, "EHLO ")
		_ = server.PrintfLine("250 fixture")
		expectSMTPCommand(t, server, "MAIL FROM:")
	})
}

// TestSendCancellationInterruptsGreeting checks cancellation before an SMTP
// greeting arrives, including when the configured timeout is disabled.
func TestSendCancellationInterruptsGreeting(t *testing.T) {
	t.Parallel()

	assertSMTPCancellation(t, smtpmail.TLSModeNone, func(_ *textproto.Conn) {
		// Withhold the initial greeting after accepting the TCP connection.
	})
}

// TestSendCancellationInterruptsTLSHandshake cancels after receiving the first
// byte of a TLS ClientHello, rather than relying on a timing assumption about
// negotiation. The fixture neither completes TLS nor receives credentials.
func TestSendCancellationInterruptsTLSHandshake(t *testing.T) {
	t.Parallel()

	assertSMTPCancellation(t, smtpmail.TLSModeStartTLS, func(server *textproto.Conn) {
		_ = server.PrintfLine("220 fixture ESMTP")
		expectSMTPCommand(t, server, "EHLO ")
		_ = server.PrintfLine("250-fixture\r\n250 STARTTLS")
		expectSMTPCommand(t, server, "STARTTLS")
		_ = server.PrintfLine("220 begin TLS")

		if _, err := server.R.ReadByte(); err != nil {
			t.Error(err)
		}
	})
}

// TestSendCancellationInterruptsDataAcknowledgement cancels after the fixture
// receives the complete DATA body but before acknowledging it. The empty receipt
// and context error do not assert that the server failed to accept the message.
func TestSendCancellationInterruptsDataAcknowledgement(t *testing.T) {
	t.Parallel()

	assertSMTPCancellation(t, smtpmail.TLSModeNone, func(server *textproto.Conn) {
		_ = server.PrintfLine("220 fixture ESMTP")
		expectSMTPCommand(t, server, "EHLO ")
		_ = server.PrintfLine("250 fixture")
		expectSMTPCommand(t, server, "MAIL FROM:")
		_ = server.PrintfLine("250 sender accepted")
		expectSMTPCommand(t, server, "RCPT TO:")
		_ = server.PrintfLine("250 recipient accepted")
		expectSMTPCommand(t, server, "DATA")
		_ = server.PrintfLine("354 send message")
		body, err := server.ReadDotBytes()

		if err != nil || !strings.Contains(string(body), "synthetic message") {
			t.Errorf("SMTP DATA = %q, %v", body, err)
		}
	})
}

// TestSendKeepsReceiptAfterAcknowledgedData verifies cancellation during QUIT
// cannot turn known SMTP acceptance into an unknown outcome. The local server
// acknowledges the complete message before canceling the caller's context.
func TestSendKeepsReceiptAfterAcknowledgedData(t *testing.T) {
	t.Parallel()

	listener, err := net.Listen("tcp", "127.0.0.1:0")

	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)

	var workers sync.WaitGroup
	t.Cleanup(func() {
		_ = listener.Close()
		workers.Wait()
	})

	workers.Go(func() {
		connection, err := listener.Accept()

		if err != nil {
			t.Error(err)

			return
		}

		defer connection.Close()

		server := textproto.NewConn(connection)
		_ = server.PrintfLine("220 fixture ESMTP")
		expectSMTPCommand(t, server, "EHLO ")
		_ = server.PrintfLine("250 fixture")
		expectSMTPCommand(t, server, "MAIL FROM:")
		_ = server.PrintfLine("250 sender accepted")
		expectSMTPCommand(t, server, "RCPT TO:")
		_ = server.PrintfLine("250 recipient accepted")
		expectSMTPCommand(t, server, "DATA")
		_ = server.PrintfLine("354 send message")

		if _, err := server.ReadDotBytes(); err != nil {
			t.Error(err)
		}

		_ = server.PrintfLine("250 message accepted")
		expectSMTPCommand(t, server, "QUIT")
		cancel()
	})

	driver, err := smtpmail.New("127.0.0.1", uint16(listener.Addr().(*net.TCPAddr).Port), func(transport *smtpmail.Builder) {
		transport.TLSMode(smtpmail.TLSModeNone)
	})

	if err != nil {
		t.Fatal(err)
	}

	message := mail.NewMessage(mail.Recipients{
		To: []mail.Address{{Email: "person@example.com"}},
	}, mail.Body{Text: "synthetic message"}, func(letter *mail.MessageBuilder) {
		letter.From(mail.Address{Email: "sender@example.com"})
	})

	receipt, err := driver.Send(ctx, message)

	if err != nil || receipt.ID == "" {
		t.Fatalf("acknowledged Send = %#v, %v; want receipt and nil error", receipt, err)
	}
}

// assertSMTPCancellation runs the public Send method against a dedicated TCP
// SMTP fixture with no driver timeout. dialog chooses the protocol boundary
// at which the server cancels the context and withholds its next response.
// The two-second watchdog detects regressions; it is not an expected wait.
func assertSMTPCancellation(t *testing.T, mode smtpmail.TLSMode, dialog func(*textproto.Conn)) {
	t.Helper()

	listener, err := net.Listen("tcp", "127.0.0.1:0")

	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)

	opened := make(chan net.Conn, 1)
	release := make(chan struct{})
	finished := make(chan error, 1)
	var workers sync.WaitGroup
	t.Cleanup(func() {
		_ = listener.Close()
		close(release)
		workers.Wait()
	})

	workers.Go(func() {
		connection, err := listener.Accept()

		if err != nil {
			t.Error(err)
			opened <- nil

			return
		}

		defer connection.Close()

		opened <- connection
		dialog(textproto.NewConn(connection))
		cancel()
		<-release
	})

	driver, err := smtpmail.New("127.0.0.1", uint16(listener.Addr().(*net.TCPAddr).Port), func(transport *smtpmail.Builder) {
		transport.TLSMode(mode)
		transport.Timeout(-1)
	})

	if err != nil {
		t.Fatal(err)
	}

	message := mail.NewMessage(mail.Recipients{
		To: []mail.Address{{Email: "person@example.com"}},
	}, mail.Body{Text: "synthetic message"}, func(letter *mail.MessageBuilder) {
		letter.From(mail.Address{Email: "sender@example.com"})
	})

	workers.Go(func() {
		receipt, err := driver.Send(ctx, message)

		if receipt != (mail.Receipt{}) {
			t.Errorf("canceled Send receipt = %#v, want empty", receipt)
		}

		finished <- err
	})

	connection := <-opened

	if connection == nil {
		t.Fatal("fixture did not accept a connection")
	}

	t.Cleanup(func() {
		_ = connection.Close()
	})

	watchdog := time.NewTimer(2 * time.Second)
	t.Cleanup(func() {
		watchdog.Stop()
	})

	select {
	case err := <-finished:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Send error = %v, want context.Canceled", err)
		}
	case <-watchdog.C:
		t.Fatal("Send ignored cancellation after SMTP connection establishment")
	}
}

// expectSMTPCommand reads one client command and checks its protocol prefix.
// Failure is reported without stopping the server goroutine, so the cancellation
// fixture can still release Send and join both workers during test cleanup.
func expectSMTPCommand(t *testing.T, server *textproto.Conn, prefix string) {
	t.Helper()

	command, err := server.ReadLine()

	if err != nil || !strings.HasPrefix(command, prefix) {
		t.Errorf("SMTP command = %q, %v; want prefix %q", command, err, prefix)
	}
}
