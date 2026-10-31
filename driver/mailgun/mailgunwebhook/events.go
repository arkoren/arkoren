package mailgunwebhook

import (
	"encoding/json/v2"

	"github.com/mailgun/mailgun-go/v5/events"

	"arkoren.dev/core/http/webhook"
)

// Accepted decodes Mailgun's acceptance of a message, not recipient delivery.
// Optional SDK fields retain zero values; no API resources are fetched.
// Example: _ = mailgunwebhook.Accepted.Key() // "accepted"
var Accepted = webhook.NewEvent("accepted", decode[events.Accepted])

// Delivered decodes recipient-server delivery with native message headers and
// delivery status. This does not prove that a human read the message.
// Example: _ = mailgunwebhook.Delivered.Key() // "delivered"
var Delivered = webhook.NewEvent("delivered", decode[events.Delivered])

// Failed decodes both temporary and permanent failures under the native "failed"
// key. Inspect Severity rather than binding dashboard names like permanent_fail.
// Example: _ = mailgunwebhook.Failed.Key() // "failed"
var Failed = webhook.NewEvent("failed", decodeFailure)

// Opened decodes tracking-pixel observations, which may come from automated
// clients; use the native ClientInfo for interpretation, not proof of reading.
// Example: _ = mailgunwebhook.Opened.Key() // "opened"
var Opened = webhook.NewEvent("opened", decode[events.Opened])

// Clicked decodes tracked links including the native Url field. Treat link and
// client information as sensitive, untrusted input and do not fetch links here.
// Example: _ = mailgunwebhook.Clicked.Key() // "clicked"
var Clicked = webhook.NewEvent("clicked", decode[events.Clicked])

// Unsubscribed decodes recipient opt-out observations including their tags.
// The HMAC does not authenticate recipient or subscription scope in event-data.
// Example: _ = mailgunwebhook.Unsubscribed.Key() // "unsubscribed"
var Unsubscribed = webhook.NewEvent("unsubscribed", decode[events.Unsubscribed])

// Complained decodes spam complaint observations with native recipient and
// message metadata; the application decides its suppression policy.
// Example: _ = mailgunwebhook.Complained.Key() // "complained"
var Complained = webhook.NewEvent("complained", decode[events.Complained])

// decode retains native SDK payload types, ignoring additive JSON fields.
// Schema failures use a fixed error so sensitive native values never leak.
func decode[T any](envelope Envelope) (T, error) {
	var payload T

	if err := json.Unmarshal(envelope.Data, &payload); err != nil {
		return payload, errEnvelope
	}

	return payload, nil
}

// decodeFailure requires the documented discriminator so an unfamiliar severity
// is retried as a schema failure rather than silently treated as permanent.
func decodeFailure(envelope Envelope) (events.Failed, error) {
	payload, err := decode[events.Failed](envelope)

	if err != nil {
		return payload, err
	}

	if payload.Severity != "temporary" && payload.Severity != "permanent" {
		return payload, errEnvelope
	}

	return payload, nil
}
