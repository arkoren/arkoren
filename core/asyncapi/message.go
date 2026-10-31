package asyncapi

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
)

// MessageBuilderFunc configures a wire message before its containers are copied.
// Examples are encoded at capture, not when the document is later exported.
//
// Example:
//
//	var configure asyncapi.MessageBuilderFunc = func(message *asyncapi.MessageBuilder) {
//		message.ContentType("application/json")
//	}
//	_ = configure
type MessageBuilderFunc = func(*MessageBuilder)

// MessageBuilder configures documentation for a complete wire message. Values
// are captured immediately; retaining the draft cannot mutate publication.
//
// Example:
//
//	var configure asyncapi.MessageBuilderFunc = func(message *asyncapi.MessageBuilder) {
//		message.Summary("An invoice was issued").WireExample("issued", map[string]any{"id": "inv-1"})
//	}
//	_ = configure
type MessageBuilder struct {
	// draft owns message members and its first capture error.
	draft
}

// Summary replaces the short message explanation. Empty text is permitted and
// has no effect on the actual wire payload.
//
// Example:
//
//	var configure asyncapi.MessageBuilderFunc = func(message *asyncapi.MessageBuilder) {
//		message.Summary("Invoice issued")
//	}
//	_ = configure
func (message *MessageBuilder) Summary(text string) *MessageBuilder {
	message.object["summary"] = text

	return message
}

// Description replaces the detailed wire-message explanation. It documents
// semantics without adding or validating runtime fields.
//
// Example:
//
//	var configure asyncapi.MessageBuilderFunc = func(message *asyncapi.MessageBuilder) {
//		message.Description("Published after an invoice is committed")
//	}
//	_ = configure
func (message *MessageBuilder) Description(text string) *MessageBuilder {
	message.object["description"] = text

	return message
}

// ContentType replaces the default application/json wire media type. Empty
// media types are invalid and are reported when the document is exported.
//
// Example:
//
//	var configure asyncapi.MessageBuilderFunc = func(message *asyncapi.MessageBuilder) {
//		message.ContentType("application/cloudevents+json")
//	}
//	_ = configure
func (message *MessageBuilder) ContentType(media string) *MessageBuilder {
	message.object["contentType"] = media

	return message
}

// WireExample appends a named complete wire payload, encoding and copying it
// immediately. Unsupported JSON values become deferred document errors. These
// examples are annotations and are not checked against the payload schema.
//
// Example:
//
//	var configure asyncapi.MessageBuilderFunc = func(message *asyncapi.MessageBuilder) {
//		message.WireExample("issued", map[string]any{"id": "inv-1"})
//	}
//	_ = configure
func (message *MessageBuilder) WireExample(name string, value any) *MessageBuilder {
	data, err := json.Marshal(value, json.Deterministic(true))
	message.fail(err)

	examples, _ := message.object["examples"].([]any)
	message.object["examples"] = append(examples, map[string]any{
		"name": name, "payload": jsontext.Value(data),
	})

	return message
}

// Binding captures message-level protocol metadata. Raw object syntax is
// checked, but protocol-specific behavior remains the caller's responsibility.
//
// Example:
//
//	var configure asyncapi.MessageBuilderFunc = func(message *asyncapi.MessageBuilder) {
//		message.Binding("kafka", asyncapi.RawObject(`{"bindingVersion":"0.5.0"}`))
//	}
//	_ = configure
func (message *MessageBuilder) Binding(protocol string, object RawObject) *MessageBuilder {
	message.binding(protocol, object)

	return message
}
