// Package asyncapi publishes immutable AsyncAPI 3.1 documents without connecting
// to brokers or changing application behavior. Declare channels and operations,
// then explicitly register Document.Handler or export Document.JSON. Send and
// Receive describe the application's perspective, not the broker's perspective.
// Schemas and examples describe wire data; they do not validate runtime traffic.
// Raw schemas can overlap: callers must ensure their messages are distinguishable.
//
// Example:
//
//	document := asyncapi.New("Billing events", "1.0")
//	data, err := document.JSON()
//	if err != nil {
//		log.Fatal(err)
//	}
//	_ = data
package asyncapi
