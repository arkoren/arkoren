package asyncapi

import (
	"encoding/json/jsontext"
)

// ChannelBuilderFunc configures one channel synchronously, before publication.
// Supplied nil callbacks panic rather than becoming document errors.
//
// Example:
//
//	var configure asyncapi.ChannelBuilderFunc = func(channel *asyncapi.ChannelBuilder) {
//		channel.Description("Invoice events")
//	}
//	_ = configure
type ChannelBuilderFunc = func(*ChannelBuilder)

// ChannelBuilder declares one address and its wire messages. Empty addresses
// mean an unknown address and export as null; address templates are unsupported.
//
// Example:
//
//	_ = asyncapi.New("Events", "1", func(definition *asyncapi.Builder) {
//		definition.Channel("invoices", "billing.invoices", func(channel *asyncapi.ChannelBuilder) {
//			channel.Description("Invoice lifecycle")
//		})
//	})
type ChannelBuilder struct {
	// draft owns channel metadata and detached message registrations.
	draft

	// id namespaces logical payload component IDs across channels.
	id string

	// schemas holds captured logical schemas to publish into root components.
	schemas map[string]jsontext.Value

	// identities detects typed envelope discriminator collisions on this channel.
	identities map[string]bool
}

// Channel registers a unique channel ID and its literal broker address. IDs
// are reference-safe letters, digits, dots, underscores and hyphens. Addresses
// are independent of IDs, and unknown addresses export as null.
//
// Example:
//
//	_ = asyncapi.New("Events", "1", func(definition *asyncapi.Builder) {
//		definition.Channel("invoices", "billing.invoices")
//	})
func (definition *Builder) Channel(id, address string, configure ...ChannelBuilderFunc) *Builder {
	channel := &ChannelBuilder{
		draft: draft{
			object: map[string]any{
				"address":  nil,
				"messages": map[string]any{},
				"bindings": map[string]any{},
			},
		},
		id:         id,
		schemas:    map[string]jsontext.Value{},
		identities: map[string]bool{},
	}

	if address != "" {
		channel.object["address"] = address
	}

	for _, configure := range configure {
		configure(channel)
	}

	definition.insert("channels", id, channel.draft)
	definition.logicalSchemas(channel.schemas)

	return definition
}

// Description replaces the channel's human-readable description. Empty text
// is allowed and does not affect message delivery.
//
// Example:
//
//	var configure asyncapi.ChannelBuilderFunc = func(channel *asyncapi.ChannelBuilder) {
//		channel.Description("Invoice lifecycle")
//	}
//	_ = configure
func (channel *ChannelBuilder) Description(text string) *ChannelBuilder {
	channel.object["description"] = text

	return channel
}

// Servers appends explicit server selections. Every channel must select at
// least one server when the document declares any servers. With no declared
// servers a portable channel may omit this setting. Duplicates are errors.
//
// Example:
//
//	var configure asyncapi.ChannelBuilderFunc = func(channel *asyncapi.ChannelBuilder) {
//		channel.Servers("main")
//	}
//	_ = configure
func (channel *ChannelBuilder) Servers(ids ...string) *ChannelBuilder {
	channel.references("servers", "#/servers/", ids)

	return channel
}

// Message declares the schema of the actual wire payload, not a logical DTO
// hidden inside an envelope. Schemas are captured immediately as draft-07 JSON
// Schema in an explicit Multi Format Schema Object. JSON content type is the
// default; MessageBuilder.ContentType can override it.
//
// Example:
//
//	var configure asyncapi.ChannelBuilderFunc = func(channel *asyncapi.ChannelBuilder) {
//		channel.Message("invoice", asyncapi.RawSchema(`{"type":"object"}`))
//	}
//	_ = configure
func (channel *ChannelBuilder) Message(id string, wirePayload Schema, configure ...MessageBuilderFunc) *ChannelBuilder {
	value, err := captureSchema(wirePayload)
	channel.fail(err)

	return channel.addMessage(id, value, configure...)
}

// addMessage inserts an already captured wire schema. Typed envelope helpers
// use this boundary after constructing their complete envelope schema. A helper
// may set extensions through its final internal MessageBuilderFunc; the child is
// encoded before returning, detaching extensions and example containers.
func (channel *ChannelBuilder) addMessage(
	id string,
	wire jsontext.Value,
	configure ...MessageBuilderFunc,
) *ChannelBuilder {
	message := &MessageBuilder{
		draft: draft{
			object: map[string]any{
				"contentType": "application/json",
				"bindings":    map[string]any{},
				"payload": map[string]any{
					"schemaFormat": "application/schema+json;version=draft-07",
					"schema":       wire,
				},
			},
		},
	}

	for _, configure := range configure {
		configure(message)
	}

	channel.insert("messages", id, message.draft)

	return channel
}

// Binding captures a channel-level protocol binding without applying it to a
// live broker. Duplicate protocol keys become deferred export errors.
//
// Example:
//
//	var configure asyncapi.ChannelBuilderFunc = func(channel *asyncapi.ChannelBuilder) {
//		channel.Binding("kafka", asyncapi.RawObject(`{"bindingVersion":"0.5.0"}`))
//	}
//	_ = configure
func (channel *ChannelBuilder) Binding(protocol string, object RawObject) *ChannelBuilder {
	channel.binding(protocol, object)

	return channel
}
