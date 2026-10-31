package mime

// Input is a synchronous, read-only projection of validated visible message
// content. Address strings retain the parent's native RFC 5322 formatting.
// Attachment bytes are borrowed only for the duration of Render.
type Input struct {
	// From is the already formatted sender mailbox.
	From string

	// To contains formatted primary recipient mailboxes.
	To []string

	// CC contains formatted visible copy mailboxes.
	CC []string

	// ReplyTo contains formatted reply mailboxes.
	ReplyTo []string

	// Subject retains the caller's validated single-line subject.
	Subject string

	// TextBody is the optional plain-text representation.
	TextBody string

	// HTMLBody is the optional HTML representation.
	HTMLBody string

	// Attachments retains ordered attachment metadata and borrowed payloads.
	Attachments []Attachment

	// invalid is supplied by Render and preserves the parent's error identity.
	invalid error
}

// Attachment projects MIME metadata independently of the parent's public type.
type Attachment struct {
	// Filename is authoritative for both MIME filename parameters.
	Filename string

	// ContentType retains the declared media type and its parameters.
	ContentType string

	// Data is the borrowed, unencoded attachment payload.
	Data []byte

	// Inline selects CID placement instead of a regular attachment.
	Inline bool

	// ContentID is the validated bare identifier of inline content.
	ContentID string
}
