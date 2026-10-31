package asyncapi_test

import (
	"bytes"
	"strings"
	"testing"

	"arkoren.dev/core/asyncapi"
)

// TestDocumentDuplicateChannel rejects replacement of a graph node.
func TestDocumentDuplicateChannel(t *testing.T) {
	t.Parallel()

	document := asyncapi.New("Events", "1", func(definition *asyncapi.Builder) {
		definition.Channel("events", "one").Channel("events", "two")
	})

	if _, err := document.JSON(); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("expected duplicate channel, got %v", err)
	}
}

// TestDocumentUnsafeID rejects IDs that cannot appear unescaped in references.
func TestDocumentUnsafeID(t *testing.T) {
	t.Parallel()

	document := asyncapi.New("Events", "1", func(definition *asyncapi.Builder) {
		definition.Channel("events/unsafe", "events")
	})

	if _, err := document.JSON(); err == nil {
		t.Fatal("unsafe id accepted")
	}
}

// TestChannelRequiresExplicitServers prevents a root endpoint from implying a
// connection for channels that never selected it.
func TestChannelRequiresExplicitServers(t *testing.T) {
	t.Parallel()

	document := asyncapi.New("Events", "1", func(definition *asyncapi.Builder) {
		definition.Server("main", "broker.example:9092", "kafka")
		definition.Channel("events", "events")
	})

	if _, err := document.JSON(); err == nil || !strings.Contains(err.Error(), "server selection") {
		t.Fatalf("expected explicit selection error, got %v", err)
	}
}

// TestChannelUnknownAddress exports null rather than an empty broker address.
func TestChannelUnknownAddress(t *testing.T) {
	t.Parallel()

	document := asyncapi.New("Events", "1", func(definition *asyncapi.Builder) {
		definition.Channel("events", "")
	})

	data, err := document.JSON()

	if err != nil {
		t.Fatal(err)
	}

	if !bytes.Contains(data, []byte(`"address":null`)) {
		t.Fatalf("unknown address not null: %s", data)
	}
}

// TestChannelTemplateAddressRejected avoids emitting unresolved parameters.
func TestChannelTemplateAddressRejected(t *testing.T) {
	t.Parallel()

	document := asyncapi.New("Events", "1", func(definition *asyncapi.Builder) {
		definition.Channel("events", "events.{tenant}")
	})

	if _, err := document.JSON(); err == nil {
		t.Fatal("templated address accepted")
	}
}

// TestAnnotationLikeChannelIDsRejectMissingServers checks every declared
// channel's explicit server selections, regardless of its ID's spelling.
func TestAnnotationLikeChannelIDsRejectMissingServers(t *testing.T) {
	t.Parallel()

	for _, id := range []string{"example", "default", "schema", "x-send"} {
		document := asyncapi.New("Events", "1", func(definition *asyncapi.Builder) {
			definition.Server("main", "broker.example:9092", "kafka")
			definition.Channel(id, "events", func(channel *asyncapi.ChannelBuilder) {
				channel.Servers(id)
			})
		})

		if _, err := document.JSON(); err == nil || !strings.Contains(err.Error(), "#/servers/"+id) {
			t.Fatalf("channel %q: expected missing server, got %v", id, err)
		}
	}
}
