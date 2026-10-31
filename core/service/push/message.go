package push

import (
	"fmt"
	"maps"
	"unicode/utf8"
)

// Message is an immutable notification declaration with a required recipient
// and title. The zero value is invalid. Err preserves the first invalid input,
// even when a later callback replaces it with a valid value.
//
// Example:
//
//	message := push.NewMessage(push.Token("device-registration"), "Ready",
//		func(notification *push.MessageBuilder) {
//			notification.Body("Collect your order.")
//		})
//	if err := message.Validate(); err != nil { return err }
type Message struct {
	// to retains only validated value variants, never a caller-owned pointer.
	to Recipient

	// title is required and validated before construction callbacks run.
	title string

	// body is optional UTF-8 text, including the empty string.
	body string

	// data is privately owned metadata; accessors always clone it.
	data map[string]string

	// err preserves the earliest invalid declaration input.
	err error
}

// MessageBuilder is a mutable construction-only draft for optional content.
// NewMessage detaches its map at publication. Retained drafts cannot modify a
// published Message, and drafts are not safe for concurrent mutation.
//
// Example:
//
//	message := push.NewMessage(push.Token("device-registration"), "Ready",
//		func(notification *push.MessageBuilder) {
//			notification.Body("Collect your order.").Data(map[string]string{"order": "42"})
//		})
//	if err := message.Err(); err != nil { return err }
type MessageBuilder struct {
	// message accumulates settings and the first error on one shared draft.
	message Message
}

// MessageBuilderFunc is an ordinary function alias for declaration callbacks.
// Callbacks run once, synchronously, in order, even after invalid input; nil
// entries panic. An expanded nil or empty slice means no optional settings.
//
// Example:
//
//	policies := []push.MessageBuilderFunc{
//		func(notification *push.MessageBuilder) {
//			notification.Body("Ready now.")
//		},
//	}
//	message := push.NewMessage(push.Token("device-registration"), "Ready", policies...)
//	if err := message.Err(); err != nil { return err }
type MessageBuilderFunc = func(*MessageBuilder)

// NewMessage publishes an immutable notification without I/O. Recipient and
// title are mandatory and cannot be replaced by callbacks. Only Subscription
// and Token values are accepted; pointer forms are rejected without retention.
// Callbacks run directly once in order on one draft, even for invalid required
// inputs. Callback panics and nil-entry misuse propagate. Maps are copied at
// capture and publication, while strings and canonical recipients are values.
//
// Example:
//
//	message := push.NewMessage(push.Token("device-registration"), "Order ready")
//	if err := message.Err(); err != nil { return err }
func NewMessage(to Recipient, title string, configure ...MessageBuilderFunc) Message {
	notification := &MessageBuilder{message: Message{title: title}}

	notification.message.err = checkRecipient(to)

	if notification.message.err == nil {
		notification.message.to = to

		notification.message.err = checkTitle(title)
	}

	for _, configure := range configure {
		configure(notification)
	}

	message := notification.message
	message.data = maps.Clone(message.data)

	return message
}

// Body replaces optional UTF-8 body text and returns the same draft.
// Empty text is valid. Invalid input remains an error even after replacement.
//
// Example:
//
//	message := push.NewMessage(push.Token("device-registration"), "Ready",
//		func(notification *push.MessageBuilder) {
//			notification.Body("Collect your order.")
//		})
//	if err := message.Err(); err != nil { return err }
func (notification *MessageBuilder) Body(body string) *MessageBuilder {
	notification.message.body = body

	if notification.message.err == nil && !utf8.ValidString(body) {
		notification.message.err = fmt.Errorf("%w: body must be valid UTF-8", ErrInvalidMessage)
	}

	return notification
}

// Data replaces metadata with a copy, never merging with earlier settings.
// Nil clears metadata. Keys and values must be valid UTF-8; driver-specific
// reserved keys and encoded size limits are checked by the configured driver.
//
// Example:
//
//	message := push.NewMessage(push.Token("device-registration"), "Ready",
//		func(notification *push.MessageBuilder) {
//			notification.Data(map[string]string{"order": "42"})
//		})
//	if err := message.Err(); err != nil { return err }
func (notification *MessageBuilder) Data(data map[string]string) *MessageBuilder {
	notification.message.data = maps.Clone(data)

	if notification.message.err == nil {
		notification.message.err = checkData(notification.message.data)
	}

	return notification
}

// To returns a value recipient without mutable references. Invalid messages
// may return nil; callers should check Err before inspecting or sending them.
//
// Example:
//
//	recipient := push.NewMessage(push.Token("device-registration"), "Ready").To()
//	_ = recipient
func (message Message) To() Recipient {
	return message.to
}

// Title returns the declared title, including invalid text for an invalid
// declaration. It contains application content and must not enter telemetry.
//
// Example:
//
//	title := push.NewMessage(push.Token("device-registration"), "Ready").Title()
//	_ = title
func (message Message) Title() string {
	return message.title
}

// Body returns optional notification text; omission returns the empty string.
// Inspecting it does not alter or revalidate the immutable declaration.
//
// Example:
//
//	body := push.NewMessage(push.Token("device-registration"), "Ready").Body()
//	_ = body
func (message Message) Body() string {
	return message.body
}

// Data returns an independent metadata map, or nil when none was declared.
// Mutating it cannot change this message or another caller's inspection.
//
// Example:
//
//	data := push.NewMessage(push.Token("device-registration"), "Ready").Data()
//	_ = data
func (message Message) Data() map[string]string {
	return maps.Clone(message.data)
}

// Err reports the first declaration error, wrapping ErrInvalidMessage.
// The zero Message is invalid; successful construction makes this check cheap
// because only the constructor and draft can modify the validated fields.
//
// Example:
//
//	message := push.NewMessage(push.Token("device-registration"), "Ready")
//	if err := message.Err(); err != nil { return err }
func (message Message) Err() error {
	if message.err != nil {
		return message.err
	}

	if message.to == nil {
		return fmt.Errorf("%w: recipient and title are required", ErrInvalidMessage)
	}

	return nil
}

// Validate reports the same portable declaration error as Err without I/O.
// Drivers add their own capability and encoded-payload checks after CheckSend.
//
// Example:
//
//	message := push.NewMessage(push.Token("device-registration"), "Ready")
//	if err := message.Validate(); err != nil { return err }
func (message Message) Validate() error {
	return message.Err()
}

// checkTitle enforces only required UTF-8 text, not vendor-specific limits.
// Error details never interpolate application content.
func checkTitle(title string) error {
	if title == "" || !utf8.ValidString(title) {
		return fmt.Errorf("%w: title must be nonempty UTF-8", ErrInvalidMessage)
	}

	return nil
}

// checkData rejects malformed UTF-8 without imposing a provider's reserved
// keys or size limit. Empty metadata and empty UTF-8 strings are portable.
func checkData(data map[string]string) error {
	for key, value := range data {
		if !utf8.ValidString(key) || !utf8.ValidString(value) {
			return fmt.Errorf("%w: metadata must be valid UTF-8", ErrInvalidMessage)
		}
	}

	return nil
}
