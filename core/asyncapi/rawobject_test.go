package asyncapi_test

import (
	"encoding/json/v2"
	"fmt"

	"arkoren.dev/core/asyncapi"
)

// ExampleRawObject demonstrates that Binding takes the protocol separately
// from its object, rather than receiving an entire protocol-keyed bindings map.
func ExampleRawObject() {
	document := asyncapi.New("Events", "1", func(definition *asyncapi.Builder) {
		definition.Channel("invoices", "billing.invoices", func(channel *asyncapi.ChannelBuilder) {
			channel.Binding("kafka", asyncapi.RawObject(`{"bindingVersion":"0.5.0"}`))
		})
	})

	data, err := document.JSON()

	if err != nil {
		fmt.Println(err)

		return
	}

	var published struct {
		// Channels exposes only the public document path needed by this example.
		Channels map[string]struct {
			// Bindings maps protocol names to their individual metadata objects.
			Bindings map[string]map[string]string `json:"bindings"`
		} `json:"channels"`
	}

	if err := json.Unmarshal(data, &published); err != nil {
		fmt.Println(err)

		return
	}

	fmt.Println(published.Channels["invoices"].Bindings["kafka"]["bindingVersion"])
	// Output: 0.5.0
}
