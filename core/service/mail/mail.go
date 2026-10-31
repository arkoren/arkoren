package mail

import (
	"context"
	"errors"
	"fmt"
	"mime"
	"net/mail"
	"slices"
	"strings"

	"arkoren.dev/core/event"
	"arkoren.dev/core/internal/clientop"
	"arkoren.dev/core/internal/semconv"
	"arkoren.dev/core/internal/unwrap"
	"arkoren.dev/core/service/telemetry"
)

// ErrInvalidMessage indicates that a Message cannot be submitted.
//
// Validation errors wrap ErrInvalidMessage so callers can classify them with
// errors.Is without inspecting diagnostic text. The error itself does not say
// whether a separate, earlier submission was accepted or delivered.
//
// Example:
//
//	err := mail.NewMessage(mail.Recipients{}, mail.Body{Text: "Hello."}).Err()
//	if errors.Is(err, mail.ErrInvalidMessage) {
//		fmt.Println("correct the message before sending")
//	}
var ErrInvalidMessage = errors.New("invalid mail message")

// Driver submits transactional email messages.
//
// Message values are immutable and may be retained safely. Their slice getters
// return detached address lists, attachment records and payload bytes owned by
// the caller of the getter; changing those results cannot alter the message.
// A successful Send confirms driver acceptance, not final recipient delivery.
//
// Send performs [CheckSend] before any I/O: an invalid message yields an
// error wrapping [ErrInvalidMessage], and a done ctx yields an error wrapping
// ctx.Err(); either way nothing is submitted. Drivers do not apply [Config]
// defaults; [Mailer] does.
//
// Example:
//
//	receipt, err := driver.Send(ctx, message)
//	if err != nil {
//		return err
//	}
//	_ = receipt.ID
type Driver interface {
	// Send submits a completed message and returns an opaque acceptance receipt.
	// CheckSend rejects invalid input or a done context before I/O. Acceptance
	// never establishes final recipient delivery; the caller owns retry policy.
	//
	// Example:
	//
	//	receipt, err := driver.Send(ctx, message)
	//	if err != nil {
	//		return err
	//	}
	//	_ = receipt.ID
	Send(ctx context.Context, message Message) (Receipt, error)
}

// Address identifies a mailbox and its optional display name. It contains only
// scalar strings; NewMessage validates destination addresses and snapshots the
// surrounding recipient lists. An empty address is not a valid destination.
//
// Example:
//
//	address := mail.Address{Name: "Support", Email: "support@example.com"}
//	_ = address.String()
type Address struct {
	// Name is the optional human-readable display name. Empty omits the name;
	// CR or LF is invalid when the address is used in a message.
	//
	// Example:
	//
	//	address := mail.Address{Name: "Support", Email: "support@example.com"}
	//	_ = address.Name
	Name string

	// Email is the required bare mailbox address, without a display name.
	// Message validation rejects empty, malformed, or CR/LF-bearing values.
	//
	// Example:
	//
	//	address := mail.Address{Email: "support@example.com"}
	//	_ = address.Email
	Email string
}

// String renders the mailbox using net/mail's RFC 5322 quoting and name encoding.
// It does not validate the address; NewMessage checks addresses before submission.
// The returned string can contain personal data and is not a telemetry label.
//
// Example:
//
//	address := mail.Address{Name: "Doe, Jane", Email: "jane@example.com"}
//	fmt.Println(address.String()) // "Doe, Jane" <jane@example.com>
func (address Address) String() string {
	return (&mail.Address{
		Name:    address.Name,
		Address: address.Email,
	}).String()
}

// AttachmentDisposition determines how an attachment is represented in MIME.
// Empty input selects AttachmentDispositionAttachment. Message construction
// normalizes this default without changing the caller's attachment records.
//
// Example: _ = mail.Attachment{Disposition: mail.AttachmentDispositionInline}
type AttachmentDisposition string

const (
	// AttachmentDispositionAttachment sends data as a downloadable attachment.
	//
	// Example: _ = mail.Attachment{Disposition: mail.AttachmentDispositionAttachment}
	AttachmentDispositionAttachment AttachmentDisposition = "attachment"

	// AttachmentDispositionInline embeds data in an HTML email through a CID.
	//
	// Example: _ = mail.Attachment{Disposition: mail.AttachmentDispositionInline, ContentID: "logo"}
	AttachmentDispositionInline AttachmentDisposition = "inline"
)

// Attachment is one MIME attachment.
//
// Inline attachments require ContentID. HTML content references them with a
// "cid:" URL, for example: <img src="cid:company-logo">.
// NewMessage copies the attachment records and bytes; later edits to the input
// record or Data do not change a published message. Empty payloads are invalid.
//
// Example:
//
//	file := mail.Attachment{Filename: "notes.txt", ContentType: "text/plain", Data: []byte("notes")}
//	message := mail.NewMessage(recipients, mail.Body{Text: "Notes attached."}, func(letter *mail.MessageBuilder) {
//		letter.Attachments(file)
//	})
//	if err := message.Err(); err != nil {
//		return err
//	}
type Attachment struct {
	// Filename is the required attachment filename presented to recipients.
	// EncodeMIME uses it for both Content-Type's name parameter and
	// Content-Disposition's filename, overriding a name supplied in ContentType.
	// Provider API mappings can impose their own attachment restrictions.
	// EncodeMIME rejects over-limit physical header lines instead of truncating
	// the filename or rewriting its parameter encoding.
	//
	// Example:
	//
	//	_ = mail.Attachment{
	//		Filename:    "notes.txt",
	//		ContentType: "text/plain",
	//		Data:        []byte("notes"),
	//	}
	Filename string

	// ContentType is the required MIME media type, optionally with parameters.
	// Empty is invalid; there is no application/octet-stream fallback.
	// EncodeMIME preserves parameters except name, which comes from Filename.
	// If MIME formatting fails, EncodeMIME returns an error wrapping
	// ErrInvalidMessage and no bytes.
	// Provider API drivers can map media types differently from EncodeMIME.
	//
	// Example:
	//
	//	_ = mail.Attachment{
	//		Filename:    "notes.txt",
	//		ContentType: "text/plain; charset=UTF-8",
	//		Data:        []byte("meeting notes"),
	//	}
	ContentType string

	// Data contains the required unencoded payload; an empty slice is invalid.
	// Construction and Attachments getters copy these bytes independently.
	//
	// Example:
	//
	//	file := mail.Attachment{Filename: "notes.txt", ContentType: "text/plain", Data: []byte("notes")}
	//	_ = len(file.Data)
	Data []byte

	// Disposition selects regular or inline placement. Empty is normalized to
	// AttachmentDispositionAttachment on the message's copied record; unknown
	// nonempty values are invalid. Inline placement requires ContentID.
	//
	// Example:
	//
	//	file := mail.Attachment{
	//		Filename: "logo.png", ContentType: "image/png", Data: []byte("image"),
	//		Disposition: mail.AttachmentDispositionInline, ContentID: "logo",
	//	}
	//	_ = file.Disposition
	Disposition AttachmentDisposition

	// ContentID identifies inline attachment content.
	//
	// ContentID is required when Disposition is AttachmentDispositionInline and
	// must otherwise be empty. CR, LF, spaces and angle brackets are rejected;
	// supply the bare identifier, not a cid: URL or a bracketed header value.
	//
	// Example:
	//
	//	file := mail.Attachment{
	//		Filename: "logo.png", ContentType: "image/png", Data: []byte("image"),
	//		Disposition: mail.AttachmentDispositionInline, ContentID: "logo",
	//	}
	//	body := mail.Body{HTML: `<img src="cid:` + file.ContentID + `">`}
	//	_ = body
	ContentID string
}

// Receipt identifies a submission accepted by a driver.
//
// ID is opaque. SMTP drivers return the generated RFC 5322 Message-ID. API
// drivers return the provider submission identifier when available.
// The record contains scalar data owned by the caller, not a delivery status.
//
// Example:
//
//	receipt, err := mailer.Send(ctx, message)
//	if err != nil {
//		return err
//	}
//	_ = receipt.ID
type Receipt struct {
	// ID identifies the accepted submission when supplied; it can be empty.
	// It is not a delivery confirmation or an idempotency key for retries.
	//
	// Example:
	//
	//	receipt := mail.Receipt{ID: "submission-123"}
	//	_ = receipt.ID
	ID string
}

// Config reports the completed sender defaults and shared reporting services
// of a [Mailer]. Obtain it from [Mailer.Config], which copies ReplyTo. This
// record is not accepted by construction; configure [New] through [Builder].
//
// Example:
//
//	mailer := mail.New(driver)
//	defaults := mailer.Config()
//	_ = defaults.From
type Config struct {
	// From reports the sender used when a message omits it. The initial value
	// is empty, requiring an explicit message sender before submission.
	//
	// Example:
	//
	//	defaults := mail.New(driver).Config()
	//	_ = defaults.From.Email
	From Address

	// ReplyTo reports defaults for messages without explicit reply addresses.
	// It is initially empty. Config returns an independent slice; editing it
	// cannot reconfigure the mailer.
	//
	// Example:
	//
	//	defaults := mail.New(driver).Config()
	//	_ = len(defaults.ReplyTo)
	ReplyTo []Address

	// Telemetry receives the span and metrics of every [Mailer.Send]. Nil uses
	// [telemetry.Default], resolved each time a message is sent, so calling
	// [telemetry.SetDefault] after [New] still takes effect.
	// The service is shared, not cloned with the reporting record.
	//
	// Example:
	//
	//	defaults := mail.New(driver).Config()
	//	_ = defaults.Telemetry // nil follows telemetry.Default per Send.
	Telemetry *telemetry.Telemetry

	// Events reports the shared bus receiving Sent and Failed. Nil, the
	// initial value, publishes nothing. Failure events retain only bounded
	// classifications; Send separately returns original diagnostic errors.
	//
	// Example:
	//
	//	defaults := mail.New(driver).Config()
	//	_ = defaults.Events // nil disables notifications.
	Events *event.Bus
}

// Builder configures the defaults of a Mailer before publication. Methods
// return the same draft. New copies owned reply-to addresses, not services.
// Retaining a draft cannot change the published mailer or its Config snapshots.
//
// Example:
//
//	mailer := mail.New(driver, func(mailer *mail.Builder) {
//		mailer.From(mailAddress)
//	})
//	_ = mailer.Config()
type Builder struct {
	// config holds private construction defaults, never a caller input record.
	config Config
}

// From sets the sender used when a message omits it. Later calls replace it;
// an empty address restores the empty default and Send's normal validation.
// Address validation remains deferred to Send after message defaults apply.
//
// Example:
//
//	mail.New(driver, func(mailer *mail.Builder) {
//		mailer.From(sender)
//	})
func (mailer *Builder) From(from Address) *Builder {
	mailer.config.From = from

	return mailer
}

// ReplyTo replaces the reply-to defaults used when a message omits them. Empty
// input clears them. The input and completed publication each own their slice;
// later input edits or retained draft changes do not affect the Mailer.
//
// Example:
//
//	mail.New(driver, func(mailer *mail.Builder) {
//		mailer.ReplyTo(support)
//	})
func (mailer *Builder) ReplyTo(replyTo ...Address) *Builder {
	mailer.config.ReplyTo = slices.Clone(replyTo)

	return mailer
}

// Telemetry selects the shared service that records sends. Nil follows
// telemetry.Default per operation, including changes after construction.
// No addresses, subjects, contents or receipt values are recorded.
//
// Example:
//
//	mail.New(driver, func(mailer *mail.Builder) {
//		mailer.Telemetry(nil)
//	})
func (mailer *Builder) Telemetry(instrumentation *telemetry.Telemetry) *Builder {
	mailer.config.Telemetry = instrumentation

	return mailer
}

// Events selects the shared bus receiving Sent and Failed after the outcome
// is known. Nil publishes nothing; listeners cannot change the send outcome.
// A driver panic propagates without publishing either event.
// Failure notifications contain only a bounded ErrorClass and counts, not the
// diagnostic error returned by Send, addresses, or message contents.
//
// Example:
//
//	mail.New(driver, func(mailer *mail.Builder) {
//		mailer.Events(nil)
//	})
func (mailer *Builder) Events(events *event.Bus) *Builder {
	mailer.config.Events = events

	return mailer
}

// Mailer completes defaults and validates immutable Message values before
// submitting them to a shared Driver. It owns no lifecycle, retry policy, or
// delivery tracking. Settings are fixed by New; Config is reporting-only.
//
// Example:
//
//	mailer := mail.New(driver, func(mailer *mail.Builder) {
//		mailer.From(mail.Address{Email: "sender@example.com"})
//	})
//	_ = mailer.Config().From
type Mailer struct {
	// driver submits the messages.
	driver Driver

	// defaults holds the sender values and the telemetry setting.
	defaults Config

	// instruments caches the telemetry instruments.
	instruments *telemetry.Instruments[clientop.Instruments]
}

// New creates a Mailer backed by driver. Construction policies set the sender values
// applied to messages that omit them and the telemetry. When telemetry is
// enabled every [Mailer.Send] with a nonnil context emits one "mail send"
// client span and the arkoren.mail.operations and
// arkoren.mail.operation.duration metrics, and costs nothing otherwise.
// Metrics count attempts, including driver panics, not confirmed acceptance.
// Addresses, subject, body, attachments, headers and
// the receipt are never recorded; the span carries the operation, recipient
// and attachment counts, and the class of a returned error, not a panic value.
//
//	mailer := mail.New(driver, func(mailer *mail.Builder) {
//		mailer.Telemetry(app.Telemetry())
//	})
//
// Construction callbacks run directly once, synchronously in order on one
// fresh draft. Nil entries panic; expanded nil or empty slices omit edits.
// Publication detaches owned settings, not live dependencies or captures.
func New(driver Driver, configure ...BuilderFunc) *Mailer {
	mailer := &Builder{}

	for _, configure := range configure {
		configure(mailer)
	}

	return &Mailer{
		driver:      driver,
		defaults:    cloneConfig(mailer.config),
		instruments: telemetry.NewInstruments(clientop.Build(operationNames)),
	}
}

// Driver returns the original shared backend, including any wrapping driver.
// It can be nil if New received nil. Direct use bypasses facade defaults,
// telemetry, and events, not the driver's CheckSend obligation or lifecycle.
//
// Example:
//
//	backend := mail.New(driver).Driver()
//	_ = backend
func (mailer *Mailer) Driver() Driver {
	return mailer.driver
}

// DriverAs returns the first driver of type T in the chain of the
// underlying driver. A driver that wraps another exposes it with an
// Unwrap() Driver method, so wrappers such as prefixing or tracing
// drivers do not hide the concrete driver.
// No match returns T's zero value and false; neither the facade nor a wrapper
// is removed or reconfigured by inspection.
//
// Example:
//
//	backend, ok := mail.New(driver).DriverAs[mail.Driver]()
//	if ok {
//		_ = backend
//	}
func (mailer *Mailer) DriverAs[T any]() (T, bool) {
	return unwrap.As[T](mailer.Driver())
}

// Config returns the completed Mailer defaults with an independent ReplyTo
// slice. Telemetry and event services remain shared. It is a reporting record,
// not a constructor input; use Builder policies to configure a mailer.
//
// Example:
//
//	mailer := mail.New(driver)
//	defaults := mailer.Config()
//	_ = defaults.From
func (mailer *Mailer) Config() Config {
	return cloneConfig(mailer.defaults)
}

// Send completes unset message defaults in a local copy, validates the resulting
// Message, and delegates submission to Driver without replaying construction
// callbacks. The original message remains unchanged. When [Builder.Events] is
// set, returned validation or driver errors publish [Failed]; driver acceptance
// publishes [Sent], not confirmation of recipient delivery. A nil ctx fails
// before telemetry or events.
// Failed notifications carry only a bounded class; the returned error retains
// original validation and provider causes for errors.Is and errors.As.
//
// A driver panic propagates unchanged without publishing either outcome.
// Telemetry still closes the mailer's own span and records attempt metrics,
// without ending the caller's span, classifying the panic or recording its value.
//
// Example:
//
//	events := event.New()
//	mailer := mail.New(driver, func(mailer *mail.Builder) {
//		mailer.Events(events)
//	})
//	receipt, err := mailer.Send(ctx, message)
//	if err != nil {
//		return err
//	}
//	_ = receipt.ID
func (mailer *Mailer) Send(ctx context.Context, message Message) (receipt Receipt, err error) {
	if ctx == nil {
		return Receipt{}, fmt.Errorf("%w: context cannot be nil", ErrInvalidMessage)
	}

	ctx, op := clientop.Begin(ctx, mailer.defaults.Telemetry, mailer.instruments, telemetry.Client, "mail", operationSend,
		telemetry.String(semconv.MailOperation, operationSend))
	recipients := len(message.to) + len(message.cc) + len(message.bcc)
	// driverReturned distinguishes acceptance from an interrupted call whose
	// named error is still nil. Returned validation errors need no driver call.
	driverReturned := false
	defer func() {
		op.End(ctx, err, errorSentinels...)

		if err != nil || driverReturned {
			mailer.emit(ctx, message, receipt, err)
		}
	}()

	op.SetAttrs(
		telemetry.Int(semconv.MailRecipients, recipients),
		telemetry.Int(semconv.MailAttachments, len(message.attachments)))

	if mailer.driver == nil {
		return Receipt{}, fmt.Errorf("%w: driver cannot be nil", ErrInvalidMessage)
	}

	message = mailer.complete(message)

	if err := message.Validate(); err != nil {
		return Receipt{}, err
	}

	receipt, err = mailer.driver.Send(ctx, message)
	driverReturned = true

	return receipt, err
}

// CheckSend performs the checks every [Driver] makes before any I/O: it
// returns an error wrapping ctx.Err() when ctx is done, and the error from
// [Message.Validate] when message is invalid.
// The context must be nonnil. This pure preflight applies no defaults and does
// not submit anything; drivers call it before their provider I/O.
//
// Example:
//
//	if err := mail.CheckSend(ctx, message); err != nil {
//		return mail.Receipt{}, err
//	}
func CheckSend(ctx context.Context, message Message) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("send mail: %w", err)
	}

	return message.Validate()
}

// complete fills omitted sender defaults in a value copy. Published message
// containers are immutable; only the newly supplied reply-to slice needs copying.
func (mailer *Mailer) complete(message Message) Message {
	if message.from == (Address{}) {
		message.from = mailer.defaults.From
	}

	if len(message.replyTo) == 0 {
		message.replyTo = slices.Clone(mailer.defaults.ReplyTo)
	}

	return message
}

// cloneConfig detaches reply-to address storage for publication and reporting
// snapshots. Telemetry and event services remain shared dependencies; address
// values contain only strings and need no further recursive copying.
func cloneConfig(config Config) Config {
	config.ReplyTo = append([]Address(nil), config.ReplyTo...)

	return config
}

// validateAddress rejects malformed and header-injectable mailbox addresses.
func validateAddress(address Address) error {
	if address.Email == "" {
		return errors.New("email is required")
	}

	if strings.ContainsAny(address.Name, "\r\n") || strings.ContainsAny(address.Email, "\r\n") {
		return errors.New("address contains a line break")
	}

	parsed, err := mail.ParseAddress(address.Email)

	if err != nil || parsed.Address != address.Email {
		return errors.New("email is invalid")
	}

	return nil
}

// validateAttachment rejects incomplete and header-injectable attachments.
func validateAttachment(attachment Attachment) error {
	if attachment.Filename == "" || attachment.ContentType == "" || len(attachment.Data) == 0 {
		return errors.New("filename, content type, and data are required")
	}

	if strings.ContainsAny(attachment.Filename, "\r\n") || strings.ContainsAny(attachment.ContentType, "\r\n") {
		return errors.New("filename or content type contains a line break")
	}

	if _, _, err := mime.ParseMediaType(attachment.ContentType); err != nil {
		return fmt.Errorf("content type is invalid: %w", err)
	}

	return validateAttachmentDisposition(attachment)
}

// validateAttachmentDisposition enforces the relationship between placement
// and ContentID. Empty placement retains the regular attachment default;
// unrecognized nonempty placement is rejected, never emitted as a MIME header.
func validateAttachmentDisposition(attachment Attachment) error {
	switch attachment.Disposition {
	case "", AttachmentDispositionAttachment:
		if attachment.ContentID != "" {
			return errors.New("regular attachment has a content ID")
		}
	case AttachmentDispositionInline:
		if attachment.ContentID == "" || strings.ContainsAny(attachment.ContentID, "\r\n <>") {
			return errors.New("inline attachment requires a valid content ID")
		}
	default:
		return errors.New("attachment disposition is invalid")
	}

	return nil
}

// BuilderFunc edits New's fresh construction draft synchronously, once, in
// callback order. Nil entries panic; expanded nil or empty slices omit edits.
// Publication detaches owned settings, not the driver or reporting services.
//
// Example:
//
//	policy := mail.BuilderFunc(func(mailer *mail.Builder) {
//		mailer.From(mail.Address{Email: "sender@example.com"})
//	})
//	mailer := mail.New(driver, policy)
//	_ = mailer.Config().From
type BuilderFunc = func(*Builder)
