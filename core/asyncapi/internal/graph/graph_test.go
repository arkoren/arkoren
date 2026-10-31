package graph_test

import (
	"strings"
	"testing"

	"arkoren.dev/core/asyncapi/internal/graph"
)

// TestOperationMessagesMustBelongToItsChannel checks the validator's membership
// boundary directly; the public builder already constructs same-channel refs.
func TestOperationMessagesMustBelongToItsChannel(t *testing.T) {
	t.Parallel()

	err := graph.Validate([]byte(`{
		"info":{"title":"Events","version":"1"},
		"channels":{
			"one":{"messages":{}},
			"two":{"messages":{"event":{"contentType":"application/json"}}}
		},
		"operations":{"example":{
			"channel":{"$ref":"#/channels/one"},
			"messages":[{"$ref":"#/channels/two/messages/event"}]
		}}
	}`))

	if err == nil || !strings.Contains(err.Error(), "#/channels/two/messages/event") {
		t.Fatalf("expected wrong-channel message rejection, got %v", err)
	}
}

// TestGraphValidationLeavesAnnotationsOpaque checks that the known-edge
// validator does not interpret examples, raw bindings or custom extensions as
// graph references. Logical payload extensions are plain pointer strings.
func TestGraphValidationLeavesAnnotationsOpaque(t *testing.T) {
	t.Parallel()

	err := graph.Validate([]byte(`{
		"info":{"title":"Events","version":"1"},
		"channels":{"example":{"messages":{"schema":{
			"contentType":"application/json",
			"payload":{"schema":{"x-example":{"$ref":"#/opaque-schema"}}},
			"examples":[{"payload":{"$ref":"#/opaque-example"}}],
			"bindings":{"custom":{"nested":{"$ref":"#/opaque-binding"}}},
			"x-arkoren-logical-payload":"#/opaque-logical-pointer",
			"x-custom":{"$ref":"#/opaque-extension"}
		}}}},
		"operations":{"x-send":{
			"channel":{"$ref":"#/channels/example"},
			"messages":[{"$ref":"#/channels/example/messages/schema"}]
		}}
	}`))

	if err != nil {
		t.Fatal(err)
	}
}
