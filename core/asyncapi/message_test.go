package asyncapi_test

import (
	"testing"

	"arkoren.dev/core/asyncapi"
)

// TestWireExampleReferenceIsLiteral ensures dollar-ref keys inside payloads
// are not mistaken for document graph references.
func TestWireExampleReferenceIsLiteral(t *testing.T) {
	t.Parallel()

	document := asyncapi.New("Events", "1", func(definition *asyncapi.Builder) {
		definition.Channel("events", "events", func(channel *asyncapi.ChannelBuilder) {
			channel.Message("event", asyncapi.RawSchema(`true`), func(message *asyncapi.MessageBuilder) {
				message.WireExample("literal", map[string]string{"$ref": "#/not-a-document-pointer"})
			})
		})
	})

	if _, err := document.JSON(); err != nil {
		t.Fatal(err)
	}
}
