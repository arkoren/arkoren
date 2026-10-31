package smtpmail

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json/v2"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"arkoren.dev/core/service/mail"
	"arkoren.dev/core/service/mail/mailtest"
)

// TestConstructionCallbacksShareDraftOnce verifies order, receiver identity,
// last-write behavior and publication isolation without opening a connection.
func TestConstructionCallbacksShareDraftOnce(t *testing.T) {
	t.Parallel()

	var retained *Builder
	calls := 0

	driver, err := New("smtp.example.test", 587, func(transport *Builder) {
		calls++
		retained = transport

		if transport.Username("old").Password("secret") != transport {
			t.Fatal("settings returned a different draft")
		}

		transport.TLSMode(TLSModeImplicit)
	}, func(transport *Builder) {
		calls++

		if transport != retained || transport.config.Username != "old" {
			t.Fatal("callbacks did not share ordered settings")
		}

		if transport.Username("user").Timeout(time.Second) != transport {
			t.Fatal("settings returned a different draft")
		}
	})

	if err != nil {
		t.Fatal(err)
	}

	if calls != 2 {
		t.Fatalf("callbacks = %d, want 2", calls)
	}

	retained.Username("changed").Password("changed")
	retained.TLSMode(TLSModeNone).Timeout(-1)

	want := Config{
		Username: "user",
		Password: "secret",
		TLSMode:  TLSModeImplicit,
		Timeout:  time.Second,
	}

	if driver.config != want {
		t.Fatalf("published settings changed: %s", driver.config)
	}
}

// TestConstructionDefaultsAndEmptyCallbacks verifies omitted and expanded
// empty policies select identical secure defaults and independent drafts.
func TestConstructionDefaultsAndEmptyCallbacks(t *testing.T) {
	t.Parallel()

	var policies []func(*Builder)

	driver, err := New("smtp.example.test", 587, policies...)

	if err != nil {
		t.Fatal(err)
	}

	other, err := New("smtp.example.test", 587, []func(*Builder){}...)

	if err != nil {
		t.Fatal(err)
	}

	if driver.config != (Config{TLSMode: TLSModeStartTLS}) || other.config != driver.config {
		t.Fatal("empty callbacks changed defaults")
	}

	if driver.config.TLSMode != TLSModeStartTLS {
		t.Fatal("default transport is not STARTTLS")
	}
}

// TestConstructionDraftsAreFresh verifies settings never leak between calls.
func TestConstructionDraftsAreFresh(t *testing.T) {
	t.Parallel()

	var first *Builder

	_, err := New("smtp.example.test", 587, func(transport *Builder) {
		first = transport
		transport.Timeout(-1)
	})

	if err != nil {
		t.Fatal(err)
	}

	_, err = New("smtp.example.test", 587, func(transport *Builder) {
		if transport == first || transport.config != (Config{}) {
			t.Fatal("draft reused")
		}
	})

	if err != nil {
		t.Fatal(err)
	}
}

// TestConstructionNilCallbackPanics verifies nil misuse is not filtered out,
// even when the required connection settings are invalid.
func TestConstructionNilCallbackPanics(t *testing.T) {
	t.Parallel()

	defer func() {
		if recover() == nil {
			t.Fatal("nil callback did not panic")
		}
	}()

	_, _ = New("", 0, nil)
}

// TestConstructionCallbackPanicPropagates verifies application panics are not
// recovered or converted into configuration errors.
func TestConstructionCallbackPanicPropagates(t *testing.T) {
	t.Parallel()

	defer func() {
		if got := recover(); got != "policy panic" {
			t.Fatalf("panic = %v", got)
		}
	}()

	_, _ = New("", 0, func(transport *Builder) {
		panic("policy panic")
	})
}

// TestConstructionCallbacksPrecedeValidation verifies invalid required input
// does not skip construction policies.
func TestConstructionCallbacksPrecedeValidation(t *testing.T) {
	t.Parallel()

	calls := 0

	_, err := New("", 0, func(transport *Builder) {
		calls++
		transport.Timeout(time.Second)
	})

	if err == nil || calls != 1 {
		t.Fatalf("err = %v, callbacks = %d", err, calls)
	}
}

// TestConstructionValidatesCompletedDraft preserves final-state validation:
// later policies can replace an invalid optional TLS mode before publication.
func TestConstructionValidatesCompletedDraft(t *testing.T) {
	t.Parallel()

	driver, err := New("smtp.example.test", 587, func(transport *Builder) {
		transport.TLSMode(TLSMode("unknown"))
	}, func(transport *Builder) {
		transport.TLSMode(TLSModeStartTLS)
	})

	if err != nil {
		t.Fatal(err)
	}

	if driver.config.TLSMode != TLSModeStartTLS {
		t.Fatal("completed draft was not published")
	}
}

// TestDriverConformance verifies the driver passes the mail conformance suite
// against the Mailpit service used by the integration tests.
func TestDriverConformance(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}

	mailtest.Run(t, func(t *testing.T) mail.Driver {
		driver, err := New(mailpitHost, mailpitPort, func(transport *Builder) {
			transport.TLSMode(TLSModeNone)
		})

		if err != nil {
			t.Fatal(err)
		}

		return driver
	}, mailtest.WithConcurrency(2))
}

// TestTLSModeJSONUsesNames guards the public configuration representation of
// transport choices, including the explicit unencrypted opt-in.
func TestTLSModeJSONUsesNames(t *testing.T) {
	t.Parallel()

	data, err := json.Marshal([]TLSMode{TLSModeStartTLS, TLSModeImplicit, TLSModeNone})

	if err != nil {
		t.Fatal(err)
	}

	if string(data) != `["starttls","implicit","none"]` {
		t.Fatalf("TLS mode JSON = %s", data)
	}

	var decoded TLSMode

	if err := json.Unmarshal([]byte(`"implicit"`), &decoded); err != nil {
		t.Fatal(err)
	}

	if decoded != TLSModeImplicit {
		t.Fatalf("decoded mode = %q", decoded)
	}
}

// TestEmptyTLSModeRequiresStartTLS verifies an explicit empty mode remains the
// secure default, rather than silently selecting plaintext or failing validation.
func TestEmptyTLSModeRequiresStartTLS(t *testing.T) {
	t.Parallel()

	driver, err := New("smtp.example.test", 587, func(transport *Builder) {
		transport.TLSMode("")
	})

	if err != nil {
		t.Fatal(err)
	}

	if driver.config.TLSMode != TLSModeStartTLS {
		t.Fatalf("mode = %q, want STARTTLS", driver.config.TLSMode)
	}
}

// Mailpit test settings configure the shared SMTP integration fixture.
const (
	testOperationTimeout = 10 * time.Second
	mailpitHost          = "127.0.0.1"
	mailpitPort          = 1025
	mailpitAPIURL        = "http://127.0.0.1:8025"
)

// mailpitAttachment describes an attachment captured by the Mailpit API.
type mailpitAttachment struct {
	// PartID identifies the attachment through Mailpit's message API.
	PartID string

	// FileName is the attachment name captured by Mailpit.
	FileName string

	// ContentType is the MIME type captured by Mailpit.
	ContentType string
}

// mailpitMessage describes the Mailpit API fields asserted by this test suite.
type mailpitMessage struct {
	// Subject is the captured message subject.
	Subject string

	// Text is the captured text body.
	Text string

	// Attachments contains the captured regular attachments.
	Attachments []mailpitAttachment
}

// TestClientSendDeliversMessageToMailpit verifies plaintext SMTP submission is
// captured by the supported trusted-local Mailpit service.
func TestClientSendDeliversMessageToMailpit(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}

	t.Parallel()

	testMailpitDelivery(t)
}

// testMailpitDelivery verifies SMTP submission reaches the Mailpit API.
func testMailpitDelivery(t *testing.T) {
	t.Helper()

	ctx, cancel := context.WithTimeout(t.Context(), testOperationTimeout)
	t.Cleanup(cancel)

	name := newTestMessageName(t)
	subject := fmt.Sprintf("SMTP driver %s", name)
	textBody := fmt.Sprintf("Mailpit body %s", name)
	attachment := fmt.Appendf(nil, "Mailpit attachment %s", name)
	client := newMailpitClient(t)

	receipt := sendMailpitMessage(t, client, ctx, subject, textBody, attachment, name)

	assertMailpitReceipt(t, receipt)

	captured := latestMailpitMessage(t, ctx)

	assertMailpitMessage(t, captured, subject, textBody)

	capturedAttachment := latestMailpitAttachment(t, ctx, captured.Attachments[0].PartID)

	assertMailpitAttachmentData(t, capturedAttachment, attachment)
}

// newMailpitClient creates an SMTP client configured for the shared Mailpit service.
func newMailpitClient(t *testing.T) *Driver {
	t.Helper()

	client, err := New(mailpitHost, mailpitPort, func(transport *Builder) {
		transport.TLSMode(TLSModeNone)
	})

	if err != nil {
		t.Fatal(err)
	}

	return client
}

// sendMailpitMessage sends a unique message through the shared Mailpit service.
func sendMailpitMessage(t *testing.T, client *Driver, ctx context.Context, subject, text string, attachment []byte, name string) mail.Receipt {
	t.Helper()

	receipt, err := client.Send(ctx, mail.NewMessage(mail.Recipients{
		To: []mail.Address{{
			Email: fmt.Sprintf("recipient-%s@example.test", name),
		}},
	}, mail.Body{Text: text}, func(letter *mail.MessageBuilder) {
		letter.From(mail.Address{Email: fmt.Sprintf("sender-%s@example.test", name)})
		letter.Subject(subject)
		letter.Attachments(mail.Attachment{
			Filename:    "report.txt",
			ContentType: "text/plain",
			Data:        attachment,
		})
	}))

	if err != nil {
		t.Fatal(err)
	}

	return receipt
}

// assertMailpitReceipt verifies successful SMTP delivery returns a receipt ID.
func assertMailpitReceipt(t *testing.T, receipt mail.Receipt) {
	t.Helper()

	if receipt.ID == "" {
		t.Fatal("receipt ID is empty")
	}
}

// latestMailpitMessage retrieves and decodes Mailpit's latest message.
func latestMailpitMessage(t *testing.T, ctx context.Context) mailpitMessage {
	t.Helper()

	response := mailpitResponse(t, ctx, mailpitAPIURL+"/api/v1/message/latest", "message")
	var captured mailpitMessage
	err := json.UnmarshalRead(response.Body, &captured)
	closeErr := response.Body.Close()

	if err != nil {
		t.Fatal(err)
	}

	if closeErr != nil {
		t.Fatal(closeErr)
	}

	return captured
}

// latestMailpitAttachment retrieves the requested part from Mailpit's latest message.
func latestMailpitAttachment(t *testing.T, ctx context.Context, partID string) []byte {
	t.Helper()

	path := fmt.Sprintf("%s/api/v1/message/latest/part/%s", mailpitAPIURL, url.PathEscape(partID))
	response := mailpitResponse(t, ctx, path, "attachment")
	contents, err := io.ReadAll(response.Body)
	closeErr := response.Body.Close()

	if err != nil {
		t.Fatal(err)
	}

	if closeErr != nil {
		t.Fatal(closeErr)
	}

	return contents
}

// mailpitResponse fetches a Mailpit API resource and fails on transport or
// non-OK responses. The caller owns and must close the successful response body.
func mailpitResponse(t *testing.T, ctx context.Context, path, resource string) *http.Response {
	t.Helper()

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, path, nil)

	if err != nil {
		t.Fatal(err)
	}

	response, err := http.DefaultClient.Do(request)

	if err != nil {
		t.Fatal(err)
	}

	if response.StatusCode != http.StatusOK {
		response.Body.Close()
		t.Fatalf("Mailpit %s status = %d", resource, response.StatusCode)
	}

	return response
}

// assertMailpitMessage verifies the relevant message fields captured by Mailpit.
func assertMailpitMessage(t *testing.T, captured mailpitMessage, subject, text string) {
	t.Helper()

	assertMailpitMessageText(t, captured, subject, text)
	assertMailpitAttachment(t, captured.Attachments)
}

// assertMailpitMessageText verifies the subject and text captured by Mailpit.
func assertMailpitMessageText(t *testing.T, captured mailpitMessage, subject, text string) {
	t.Helper()

	if captured.Subject != subject {
		t.Fatalf("captured subject = %q, want %q", captured.Subject, subject)
	}

	if captured.Text != text {
		t.Fatalf("captured text = %q, want %q", captured.Text, text)
	}
}

// assertMailpitAttachment verifies the single regular attachment captured by Mailpit.
func assertMailpitAttachment(t *testing.T, attachments []mailpitAttachment) {
	t.Helper()

	if len(attachments) != 1 {
		t.Fatalf("captured attachments = %#v", attachments)
	}

	attachment := attachments[0]

	if attachment.FileName != "report.txt" || attachment.ContentType != "text/plain" {
		t.Fatalf("captured attachment = %#v", attachment)
	}
}

// assertMailpitAttachmentData verifies Mailpit preserves attachment bytes.
func assertMailpitAttachmentData(t *testing.T, actual, expected []byte) {
	t.Helper()

	if string(actual) != string(expected) {
		t.Fatalf("captured attachment = %q, want %q", actual, expected)
	}
}

// TestNewRejectsMissingHost verifies SMTP configuration requires a host.
func TestNewRejectsMissingHost(t *testing.T) {
	t.Parallel()

	_, err := New("", 25)

	if err == nil {
		t.Fatal("expected missing host error")
	}
}

// TestNewRejectsMissingPort verifies SMTP configuration requires a port.
func TestNewRejectsMissingPort(t *testing.T) {
	t.Parallel()

	_, err := New("smtp.example.test", 0)

	if err == nil {
		t.Fatal("expected missing port error")
	}
}

// TestNewRejectsInvalidTLSMode verifies SMTP configuration accepts only known
// TLS modes.
func TestNewRejectsInvalidTLSMode(t *testing.T) {
	t.Parallel()

	_, err := New("smtp.example.test", 25, func(transport *Builder) {
		transport.TLSMode(TLSMode("unknown"))
	})

	if err == nil {
		t.Fatal("expected invalid TLS mode error")
	}
}

// TestNewRejectsPartialAuthentication verifies SMTP PLAIN authentication
// requires both a username and password.
func TestNewRejectsPartialAuthentication(t *testing.T) {
	t.Parallel()

	_, err := New("smtp.example.test", 25, func(transport *Builder) {
		transport.Username("user")
	})

	if err == nil {
		t.Fatal("expected incomplete authentication error")
	}
}

// TestNewRejectsPlaintextAuthentication verifies SMTP PLAIN authentication
// requires encrypted transport.
func TestNewRejectsPlaintextAuthentication(t *testing.T) {
	t.Parallel()

	_, err := New("smtp.example.test", 25, func(transport *Builder) {
		transport.Username("user")
		transport.Password("password")
		transport.TLSMode(TLSModeNone)
	})

	if err == nil {
		t.Fatal("expected plaintext authentication error")
	}
}

// TestNewAllowsUnauthenticatedPlaintext verifies trusted-local SMTP needs no
// transport encryption when authentication is disabled.
func TestNewAllowsUnauthenticatedPlaintext(t *testing.T) {
	t.Parallel()

	client, err := New("smtp.example.test", 25, func(transport *Builder) {
		transport.TLSMode(TLSModeNone)
	})

	if err != nil {
		t.Fatalf("new unauthenticated plaintext client: %v", err)
	}

	if client == nil {
		t.Fatal("expected unauthenticated plaintext client")
	}
}

// TestSendRequiresStartTLSByDefault verifies a server without STARTTLS is
// refused unless plaintext is opted into.
func TestSendRequiresStartTLSByDefault(t *testing.T) {
	t.Parallel()

	listener, err := net.Listen("tcp", "127.0.0.1:0")

	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		_ = listener.Close()
	})
	go serveGreeting(listener)

	port := uint16(listener.Addr().(*net.TCPAddr).Port)
	client, err := New("127.0.0.1", port)

	if err != nil {
		t.Fatal(err)
	}

	_, err = client.Send(t.Context(), testMessage())

	if err == nil || !strings.Contains(err.Error(), "starttls") {
		t.Fatalf("Send error = %v, want missing starttls", err)
	}
}

// TestSendTimesOutWithoutContextDeadline verifies Config.Timeout bounds a
// server that accepts the connection but never answers.
func TestSendTimesOutWithoutContextDeadline(t *testing.T) {
	t.Parallel()

	listener, err := net.Listen("tcp", "127.0.0.1:0")

	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		_ = listener.Close()
	})
	go func() {
		connection, err := listener.Accept()

		if err == nil {
			t.Cleanup(func() {
				_ = connection.Close()
			})
		}
	}()

	port := uint16(listener.Addr().(*net.TCPAddr).Port)
	client, err := New("127.0.0.1", port, func(transport *Builder) {
		transport.Timeout(50 * time.Millisecond)
	})

	if err != nil {
		t.Fatal(err)
	}

	if _, err := client.Send(t.Context(), testMessage()); err == nil {
		t.Fatal("expected a timeout error")
	}
}

// serveGreeting answers one connection with an SMTP greeting and a plain EHLO
// reply that does not advertise STARTTLS, and ends on QUIT.
func serveGreeting(listener net.Listener) {
	connection, err := listener.Accept()

	if err != nil {
		return
	}

	defer connection.Close()

	reader := bufio.NewReader(connection)
	_, _ = fmt.Fprint(connection, "220 test ESMTP\r\n")

	for {
		line, err := reader.ReadString('\n')

		if err != nil {
			return
		}

		switch {
		case strings.HasPrefix(line, "EHLO"):
			_, _ = fmt.Fprint(connection, "250 test\r\n")
		case strings.HasPrefix(line, "QUIT"):
			_, _ = fmt.Fprint(connection, "221 bye\r\n")

			return
		}
	}
}

// testMessage returns a minimal valid message.
func testMessage() mail.Message {
	return mail.NewMessage(mail.Recipients{
		To: []mail.Address{{
			Email: "to@example.test",
		}},
	}, mail.Body{Text: "body"}, func(letter *mail.MessageBuilder) {
		letter.From(mail.Address{Email: "sender@example.test"})
		letter.Subject("subject")
	})
}

// newTestMessageName returns a sanitized, random name for remote test data.
func newTestMessageName(t *testing.T) string {
	t.Helper()

	bytes := make([]byte, 8)

	if _, err := rand.Read(bytes); err != nil {
		t.Fatal(err)
	}

	var builder strings.Builder
	builder.Grow(len(t.Name()) + 1 + hex.EncodedLen(len(bytes)))

	for _, character := range t.Name() {
		if testNameCharacter(character) {
			builder.WriteRune(character)

			continue
		}

		builder.WriteByte('-')
	}

	builder.WriteByte('-')
	builder.WriteString(hex.EncodeToString(bytes))

	return strings.ToLower(builder.String())
}

// testNameCharacter reports whether character is valid in a generated test name.
func testNameCharacter(character rune) bool {
	return strings.ContainsRune("abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789", character)
}
