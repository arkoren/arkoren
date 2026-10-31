package llm

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"
	"slices"
)

// Role is who wrote a [Message].
// The empty zero value and unknown values are invalid request roles; the model
// facade rejects them before driver I/O rather than assuming a speaker.
//
//	message := llm.Message{Role: llm.RoleUser, Parts: []llm.Part{llm.Text{Text: "Hello."}}}
//	_ = message.Role
type Role string

const (
	// RoleUser marks messages from the user, including tool results.
	// It is distinct from system instructions, which are Request.Instructions.
	//
	//	message := llm.Message{Role: llm.RoleUser, Parts: []llm.Part{llm.Text{Text: "Hello."}}}
	//	_ = message
	RoleUser Role = "user"

	// RoleAssistant marks messages from the model.
	// Preserve assistant tool calls and reasoning when saving a conversation.
	//
	//	message := llm.Message{Role: llm.RoleAssistant, Parts: []llm.Part{llm.Text{Text: "Hello."}}}
	//	_ = message
	RoleAssistant Role = "assistant"
)

// Part is one piece of a [Message]. The set is sealed: the types are [Text],
// [Image], [Document], [ToolCall], [ToolResult] and [Reasoning]. A driver
// returns [ErrUnsupported] for a part it cannot send.
// Raw part buffers remain shared; configure them before submitting a request.
//
//	parts := []llm.Part{llm.Text{Text: "Describe this image."}, llm.Image{URL: imageURL}}
//	message := llm.User(parts...)
//	_ = message
type Part interface {
	// part seals the interface.
	part()
}

// Text is plain text.
// Use it inside Message.Parts or let UserText construct a single text message.
//
//	message := llm.User(llm.Text{Text: "Summarize this."})
//	_ = message
type Text struct {
	// Text is the content.
	Text string
}

// Image is an image the model can look at, given inline or by URL.
// Provider support and URL fetching policy belong to the selected driver;
// merely constructing this record performs no network access.
//
//	message := llm.User(llm.Text{Text: "Describe this."}, llm.Image{
//		Data: pngBytes, MediaType: "image/png",
//	})
//	_ = message
type Image struct {
	// Data is the encoded image. It is empty when URL is set.
	Data []byte

	// URL locates the image. It is empty when Data is set.
	URL string

	// MediaType is the image's MIME type, such as "image/png". It is
	// required with Data.
	MediaType string
}

// Document is a file, such as a PDF, the model can read.
// Its bytes remain shared during the request. A driver without document support
// returns ErrUnsupported rather than silently dropping it.
//
//	message := llm.User(llm.Document{Data: pdfBytes, MediaType: "application/pdf"})
//	_ = message
type Document struct {
	// Data is the encoded document.
	Data []byte

	// MediaType is the document's MIME type, such as "application/pdf".
	MediaType string
}

// ToolCall is the model asking for a tool to run. It appears in assistant
// messages.
// Treat Arguments as untrusted input; Model.Run checks capability policy and
// typed schema validation before a registered handler can receive them.
//
//	call := llm.ToolCall{ID: "call-1", Name: "lookup-order", Arguments: []byte(`{"number":"A-42"}`)}
//	message := llm.Message{Role: llm.RoleAssistant, Parts: []llm.Part{call}}
//	_ = message
type ToolCall struct {
	// ID pairs the call with its [ToolResult].
	ID string

	// Name is the called tool.
	Name string

	// Arguments is the JSON object the model produced, matching the tool's
	// input schema when the vendor enforces it.
	Arguments []byte
}

// ToolResult answers a [ToolCall]. It appears in user messages.
// Content is sent to the model; never place unrestricted backend diagnostics
// here. Model.Run applies ToolError redaction when it creates tool results.
//
//	message := llm.User(llm.ToolResult{CallID: "call-1", Content: "Order shipped."})
//	_ = message
type ToolResult struct {
	// CallID is the [ToolCall.ID] being answered.
	CallID string

	// Content is the result as text.
	Content string

	// IsError marks Content as a failure message rather than a result.
	IsError bool
}

// Reasoning is the model's reasoning trace, kept so a later turn can send it
// back.
// Do not interpret or rewrite vendor signatures when persisting history.
// The presence of reasoning does not grant tool or resource permissions.
//
//	for _, part := range response.Message.Parts {
//		if reasoning, ok := part.(llm.Reasoning); ok {
//			_ = reasoning.Signature // Persist the original part in history unchanged.
//		}
//	}
type Reasoning struct {
	// Text is the readable reasoning, empty when the vendor hides it.
	Text string

	// Signature is the vendor's opaque proof of the trace. It is sent back
	// unchanged and never interpreted.
	Signature string
}

// part admits text into the sealed message-part family.
func (Text) part() {
}

// part admits images into the sealed message-part family.
func (Image) part() {
}

// part admits documents into the sealed message-part family.
func (Document) part() {
}

// part admits model tool calls into the sealed message-part family.
func (ToolCall) part() {
}

// part admits tool replies into the sealed message-part family.
func (ToolResult) part() {
}

// part admits reasoning into the sealed message-part family.
func (Reasoning) part() {
}

// Message is one turn of a conversation: a [Role] and its [Part] values.
// System text is not a message; it lives in [Request.Instructions].
//
// A Message encodes to JSON as {"role": ..., "parts": [...]} where each part
// carries a "type" tag, and decodes back to the same parts, so a conversation
// can be saved and resumed. Decoding fails on an unknown type.
// Raw parts and their buffers remain mutable application records; do not
// change them during a model call or while consuming its stream.
//
//	message := llm.UserText("Summarize this order.")
//	data, err := json.Marshal(message)
//	if err != nil {
//		return err
//	}
//	_ = data // Save the tagged representation to resume the conversation.
type Message struct {
	// Role is who wrote the message.
	Role Role

	// Parts is the content, in order.
	Parts []Part
}

// UserText returns a user message holding one [Text] part.
//
//	llm.UserText("Why is the sky blue?")
func UserText(text string) Message {
	return Message{
		Role:  RoleUser,
		Parts: []Part{Text{Text: text}},
	}
}

// User returns a user message holding a copy of the parts slice. Replacing
// elements of the caller's slice cannot change the message. Buffers inside raw
// parts, such as Image.Data and ToolCall.Arguments, remain shared; configure
// those before submitting a request and do not mutate them during a call.
//
//	llm.User(llm.Text{Text: "What is in this picture?"}, llm.Image{URL: url})
func User(parts ...Part) Message {
	return Message{
		Role:  RoleUser,
		Parts: slices.Clone(parts),
	}
}

// partJSON is the wire form of every [Part] variant: a flat object whose
// "type" selects the fields that apply.
type partJSON struct {
	// Type identifies which message-part fields the decoder should use.
	Type string `json:"type"`
	// Text holds a text part or the readable portion of reasoning.
	Text string `json:"text,omitzero"`
	// Data contains embedded image or document bytes.
	Data []byte `json:"data,omitzero"`
	// URL references image or document content instead of embedding it.
	URL string `json:"url,omitzero"`
	// MediaType describes the format of embedded content.
	MediaType string `json:"media_type,omitzero"`
	// ID identifies a model-issued tool call.
	ID string `json:"id,omitzero"`
	// Name selects the tool requested by the model.
	Name string `json:"name,omitzero"`
	// Arguments retains the tool's JSON argument value without string encoding.
	Arguments jsontext.Value `json:"arguments,omitzero"`
	// CallID associates a tool result with its original call.
	CallID string `json:"call_id,omitzero"`
	// Content carries the tool's textual result.
	Content string `json:"content,omitzero"`
	// IsError marks tool content that describes a failure.
	IsError bool `json:"is_error,omitzero"`
	// Signature preserves the provider's opaque reasoning signature.
	Signature string `json:"signature,omitzero"`
}

// messageJSON is the wire form of a [Message].
type messageJSON struct {
	// Role identifies the message's speaker.
	Role Role `json:"role"`
	// Parts preserves the order and tagged wire form of message content.
	Parts []partJSON `json:"parts"`
}

// MarshalJSON encodes the message with a "type" tag on every part. It fails
// on a Part type defined outside this package.
// Invalid raw tool-argument JSON also returns an encoding error.
//
//	data, err := llm.UserText("Hello.").MarshalJSON()
//	if err != nil {
//		return err
//	}
//	_ = data
func (message Message) MarshalJSON() ([]byte, error) {
	wire := messageJSON{
		Role:  message.Role,
		Parts: make([]partJSON, len(message.Parts)),
	}

	for i, part := range message.Parts {
		switch part := part.(type) {
		case Text:
			wire.Parts[i] = partJSON{
				Type: "text",
				Text: part.Text,
			}
		case Image:
			wire.Parts[i] = partJSON{
				Type:      "image",
				Data:      part.Data,
				URL:       part.URL,
				MediaType: part.MediaType,
			}
		case Document:
			wire.Parts[i] = partJSON{
				Type:      "document",
				Data:      part.Data,
				MediaType: part.MediaType,
			}
		case ToolCall:
			wire.Parts[i] = partJSON{
				Type:      "tool_call",
				ID:        part.ID,
				Name:      part.Name,
				Arguments: part.Arguments,
			}
		case ToolResult:
			wire.Parts[i] = partJSON{
				Type:    "tool_result",
				CallID:  part.CallID,
				Content: part.Content,
				IsError: part.IsError,
			}
		case Reasoning:
			wire.Parts[i] = partJSON{
				Type:      "reasoning",
				Text:      part.Text,
				Signature: part.Signature,
			}
		default:
			return nil, fmt.Errorf("llm: cannot encode part %d of type %T", i, part)
		}
	}

	return json.Marshal(wire)
}

// UnmarshalJSON decodes a message encoded by [Message.MarshalJSON].
// It publishes a fresh message only on success, so malformed or unknown parts
// leave the previous receiver unchanged.
//
//	var message llm.Message
//	if err := message.UnmarshalJSON([]byte(`{"role":"user","parts":[{"type":"text","text":"Hello."}]}`)); err != nil {
//		return err
//	}
//	_ = message.Parts
func (message *Message) UnmarshalJSON(data []byte) error {
	var wire messageJSON

	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}

	parts := make([]Part, len(wire.Parts))

	for i, part := range wire.Parts {
		switch part.Type {
		case "text":
			parts[i] = Text{Text: part.Text}
		case "image":
			parts[i] = Image{
				Data:      part.Data,
				URL:       part.URL,
				MediaType: part.MediaType,
			}
		case "document":
			parts[i] = Document{
				Data:      part.Data,
				MediaType: part.MediaType,
			}
		case "tool_call":
			parts[i] = ToolCall{
				ID:        part.ID,
				Name:      part.Name,
				Arguments: part.Arguments,
			}
		case "tool_result":
			parts[i] = ToolResult{
				CallID:  part.CallID,
				Content: part.Content,
				IsError: part.IsError,
			}
		case "reasoning":
			parts[i] = Reasoning{
				Text:      part.Text,
				Signature: part.Signature,
			}
		default:
			return fmt.Errorf("llm: unknown part type %q", part.Type)
		}
	}

	*message = Message{
		Role:  wire.Role,
		Parts: parts,
	}

	return nil
}
