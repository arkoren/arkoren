package asyncapi

// OperationBuilderFunc configures one application's send or receive operation.
// Callbacks run once in supplied order, including invalid declarations.
//
// Example:
//
//	var configure asyncapi.OperationBuilderFunc = func(action *asyncapi.OperationBuilder) {
//		action.Messages("invoice")
//	}
//	_ = configure
type OperationBuilderFunc = func(*OperationBuilder)

// OperationBuilder documents an application's send or receive operation.
// Messages must explicitly select at least one message on its channel.
//
// Example:
//
//	var configure asyncapi.OperationBuilderFunc = func(action *asyncapi.OperationBuilder) {
//		action.Messages("invoice").Summary("Publish invoices")
//	}
//	_ = configure
type OperationBuilder struct {
	// draft holds the operation's members and first declaration error.
	draft

	// channel identifies the local channel used to construct message references.
	channel string
}

// Send declares a uniquely named operation that sends from this application.
// The channel and selected messages may be declared later. A message selection
// is required, and duplicate operation IDs are errors rather than replacements.
//
// Example:
//
//	_ = asyncapi.New("Events", "1", func(definition *asyncapi.Builder) {
//		definition.Send("publish-invoice", "invoices", func(action *asyncapi.OperationBuilder) {
//			action.Messages("invoice")
//		})
//	})
func (definition *Builder) Send(id, channel string, configure ...OperationBuilderFunc) *Builder {
	return definition.operation(id, channel, "send", configure)
}

// Receive declares an operation that receives into this application, not a
// broker's direction. Channel and message references are checked at export.
//
// Example:
//
//	_ = asyncapi.New("Events", "1", func(definition *asyncapi.Builder) {
//		definition.Receive("consume-invoice", "invoices", func(action *asyncapi.OperationBuilder) {
//			action.Messages("invoice")
//		})
//	})
func (definition *Builder) Receive(id, channel string, configure ...OperationBuilderFunc) *Builder {
	return definition.operation(id, channel, "receive", configure)
}

// operation constructs the shared operation shape without replaying callbacks.
// Direction is a wire value, not a behavior flag; the callback contract is shared.
func (definition *Builder) operation(id, channel, direction string, configure []OperationBuilderFunc) *Builder {
	action := &OperationBuilder{
		draft: draft{
			object: map[string]any{
				"action":   direction,
				"channel":  map[string]any{"$ref": "#/channels/" + channel},
				"bindings": map[string]any{},
			},
		},
		channel: channel,
	}
	action.fail(validateID(channel))

	for _, configure := range configure {
		configure(action)
	}

	definition.insert("operations", id, action.draft)

	return definition
}

// Messages appends explicit channel message IDs, preserving declaration order.
// Selection is required and duplicates are errors. References always point to
// channel messages, never to an independent component message collection.
//
// Example:
//
//	var configure asyncapi.OperationBuilderFunc = func(action *asyncapi.OperationBuilder) {
//		action.Messages("invoice", "credit")
//	}
//	_ = configure
func (action *OperationBuilder) Messages(ids ...string) *OperationBuilder {
	action.references("messages", "#/channels/"+action.channel+"/messages/", ids)

	return action
}

// Summary replaces the operation's short explanation. This metadata does not
// dispatch messages or register a handler.
//
// Example:
//
//	var configure asyncapi.OperationBuilderFunc = func(action *asyncapi.OperationBuilder) {
//		action.Summary("Publish invoice changes")
//	}
//	_ = configure
func (action *OperationBuilder) Summary(text string) *OperationBuilder {
	action.object["summary"] = text

	return action
}

// Description replaces the operation's detailed explanation. Empty strings
// are accepted and do not affect runtime behavior.
//
// Example:
//
//	var configure asyncapi.OperationBuilderFunc = func(action *asyncapi.OperationBuilder) {
//		action.Description("Runs after the invoice transaction commits")
//	}
//	_ = configure
func (action *OperationBuilder) Description(text string) *OperationBuilder {
	action.object["description"] = text

	return action
}

// Security appends alternative security scheme references (OR). These apply
// in addition to server-level requirements, not in place of them. Duplicates
// and unresolved scheme IDs are deferred export errors.
//
// Example:
//
//	var configure asyncapi.OperationBuilderFunc = func(action *asyncapi.OperationBuilder) {
//		action.Security("publisher-login")
//	}
//	_ = configure
func (action *OperationBuilder) Security(ids ...string) *OperationBuilder {
	action.references("security", "#/components/securitySchemes/", ids)

	return action
}

// Binding captures operation-level protocol metadata independently of server,
// channel and message bindings. Duplicate protocols are deferred errors.
//
// Example:
//
//	var configure asyncapi.OperationBuilderFunc = func(action *asyncapi.OperationBuilder) {
//		action.Binding("kafka", asyncapi.RawObject(`{"bindingVersion":"0.5.0"}`))
//	}
//	_ = configure
func (action *OperationBuilder) Binding(protocol string, object RawObject) *OperationBuilder {
	action.binding(protocol, object)

	return action
}
