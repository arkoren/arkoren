package asyncapi

import (
	"encoding/json/jsontext"
	"fmt"
	"maps"
	"slices"
)

// addEnvelope inserts a captured complete wire envelope and its logical payload
// schema. Logical schemas live at components/schemas/<channel>.<message>, with
// an x-arkoren-logical-payload pointer annotation on the channel message. User callbacks
// run once; internal extension insertion follows them before child capture.
func (channel *ChannelBuilder) addEnvelope(
	id string,
	wire, logical jsontext.Value,
	configure []MessageBuilderFunc,
) *ChannelBuilder {
	component := channel.id + "." + id
	channel.schemas[component] = append(jsontext.Value(nil), logical...)
	callbacks := append([]MessageBuilderFunc(nil), configure...)
	callbacks = append(callbacks, func(message *MessageBuilder) {
		message.object["x-arkoren-logical-payload"] = "#/components/schemas/" + component
	})

	return channel.addMessage(id, wire, callbacks...)
}

// claimWireIdentity reserves a typed envelope's discriminator identity. Helpers
// call this before insertion; raw Message declarations cannot prove overlap.
func (channel *ChannelBuilder) claimWireIdentity(identity string) {
	if channel.identities[identity] {
		channel.fail(fmt.Errorf("duplicate wire discriminator identity %q", identity))
	}

	channel.identities[identity] = true
}

// logicalSchemas publishes detached logical payload schemas captured by typed
// channel helpers. Duplicate component IDs, including dotted-name collisions,
// fail rather than silently replacing the first declaration.
func (definition *Builder) logicalSchemas(schemas map[string]jsontext.Value) {
	components := definition.object["components"].(map[string]any)
	items := components["schemas"].(map[string]any)

	for _, id := range slices.Sorted(maps.Keys(schemas)) {
		if _, exists := items[id]; exists {
			definition.fail(fmt.Errorf("duplicate logical schema id %q", id))
			continue
		}

		items[id] = append(jsontext.Value(nil), schemas[id]...)
	}
}
