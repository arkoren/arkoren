package asyncapi

// RawObject contains an opaque JSON object for protocol bindings or security
// details. Capture validates JSON syntax and the object root, then copies the
// exact bytes, preserving large numbers and whitespace. Nested contents are
// not interpreted as schemas. Empty input, null and nonobjects are invalid.
// Do not mutate the slice concurrently with definition construction.
//
// Example:
//
//	document := asyncapi.New("Events", "1", func(definition *asyncapi.Builder) {
//		definition.Channel("invoices", "billing.invoices", func(channel *asyncapi.ChannelBuilder) {
//			channel.Binding("kafka", asyncapi.RawObject(`{"bindingVersion":"0.5.0"}`))
//		})
//	})
//	if _, err := document.JSON(); err != nil {
//		return err
//	}
type RawObject []byte
