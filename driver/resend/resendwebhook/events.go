package resendwebhook

import (
	"encoding/json/v2"
	"time"

	"arkoren.dev/core/http/webhook"
)

// Email contains the email metadata carried by a notification. It is not an
// email body and does not establish that a recipient read or trusted a message.
// Optional provider fields remain zero-valued; payloads contain personal data.
//
// Example:
//
//	accept := func(ctx context.Context, delivery webhook.Delivery[resendwebhook.Email]) error {
//		return emails.RecordDelivery(ctx, delivery.Payload.EmailID)
//	}
//	_ = accept
type Email struct {
	// EmailID identifies the email, not the webhook event used for deduplication.
	// Example: _ = resendwebhook.Email{EmailID: "56761188-7520-42d8-8898-ff6fc54ce618"}
	EmailID string `json:"email_id"`
	// CreatedAt is the email creation time, distinct from the event time.
	// Example: _ = resendwebhook.Email{}.CreatedAt
	CreatedAt string `json:"created_at"`
	// From is the sender, potentially including a display name for sent email.
	// Example: _ = resendwebhook.Email{From: "Acme <onboarding@example.com>"}
	From string `json:"from"`
	// To contains the impacted recipient addresses, in provider order.
	// Example: _ = resendwebhook.Email{To: []string{"customer@example.com"}}
	To []string `json:"to"`
	// CC lists carbon-copy recipients when provided for received email.
	// Example: _ = resendwebhook.Email{}.CC
	CC []string `json:"cc"`
	// BCC lists blind-copy recipients when provided for received email.
	// Example: _ = resendwebhook.Email{}.BCC
	BCC []string `json:"bcc"`
	// Subject is metadata, not a safe logging label or trusted instruction.
	// Example: _ = resendwebhook.Email{Subject: "Your receipt"}
	Subject string `json:"subject"`
	// MessageID is the RFC Message-ID, not Resend's email or event identity.
	// Example: _ = resendwebhook.Email{MessageID: "<receipt@example.com>"}
	MessageID string `json:"message_id"`
	// Tags contains optional caller-assigned metadata from sent email.
	// Example: _ = resendwebhook.Email{Tags: map[string]string{"category": "receipt"}}
	Tags map[string]string `json:"tags"`
	// ReceivedFor contains forwarding addresses from Received header for clauses.
	// Example: _ = resendwebhook.Email{}.ReceivedFor
	ReceivedFor []string `json:"received_for"`
	// Bounce is present for bounced notifications and carries SMTP failure details.
	// Example: _ = resendwebhook.Email{}.Bounce
	Bounce *Bounce `json:"bounce"`
}

// Bounce retains Resend's bounce classification and diagnostic information.
// Values are provider-defined strings, not a closed enum; diagnostics may
// contain personal information and must not be used as metric labels.
//
// Example:
//
//	if delivery.Payload.Bounce != nil {
//		kind := delivery.Payload.Bounce.Type
//		_ = kind
//	}
type Bounce struct {
	// Type is the provider classification, for example Permanent or Temporary.
	// Example: _ = resendwebhook.Bounce{Type: "Permanent"}
	Type string `json:"type"`
	// SubType provides the finer classification, such as Suppressed.
	// Example: _ = resendwebhook.Bounce{SubType: "Suppressed"}
	SubType string `json:"subType"`
	// Message explains the delivery failure without changing its classification.
	// Example: _ = resendwebhook.Bounce{}.Message
	Message string `json:"message"`
	// DiagnosticCode contains optional SMTP diagnostic responses.
	// Example: _ = resendwebhook.Bounce{}.DiagnosticCode
	DiagnosticCode []string `json:"diagnosticCode"`
}

// EmailSent reports a successful sending API request; Resend will attempt
// delivery. The definition contains no endpoint credential and can be reused.
//
// Example:
//
//	receiver.Handle(resendwebhook.EmailSent, accept)
var EmailSent = webhook.NewEvent("email.sent", decodeEmail)

// EmailDelivered reports acceptance by the recipient's mail server, not that
// the email reached an inbox or was read. It decodes typed email metadata.
//
// Example:
//
//	receiver.Handle(resendwebhook.EmailDelivered, accept)
var EmailDelivered = webhook.NewEvent("email.delivered", decodeEmail)

// EmailBounced reports a delivery rejection and includes typed Bounce details.
// The application decides its suppression policy; no mail state changes here.
//
// Example:
//
//	receiver.Handle(resendwebhook.EmailBounced, accept)
var EmailBounced = webhook.NewEvent("email.bounced", decodeEmail)

// EmailComplained reports a spam complaint with the impacted email metadata.
// It does not automatically unsubscribe or suppress any recipient.
//
// Example:
//
//	receiver.Handle(resendwebhook.EmailComplained, accept)
var EmailComplained = webhook.NewEvent("email.complained", decodeEmail)

// EmailReceived reports receipt of an inbound email's metadata. Email bodies,
// headers and attachment content require separate application-owned API calls;
// this decoder performs no network I/O and does not trust email contents.
//
// Example:
//
//	receiver.Handle(resendwebhook.EmailReceived, accept)
var EmailReceived = webhook.NewEvent("email.received", decodeEmail)

// decodeEmail validates only bound email notifications. Unknown fields remain
// forward-compatible; missing email identity, invalid event time or incorrectly
// typed data are processing failures so core webhook returns 503.
func decodeEmail(envelope Envelope) (Email, error) {
	if _, err := time.Parse(time.RFC3339Nano, envelope.CreatedAt); err != nil {
		return Email{}, errEnvelope
	}

	var email Email

	if err := json.Unmarshal(envelope.Data, &email); err != nil {
		return Email{}, errEnvelope
	}

	if email.EmailID == "" {
		return Email{}, errEnvelope
	}

	return email, nil
}
