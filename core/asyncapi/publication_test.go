package asyncapi

import (
	"bytes"
	"encoding/json/jsontext"
	"strings"
	"testing"
)

// TestCapturedEnvelopeComponentPublication exercises the private insertion
// boundary shared by typed envelope helpers and graph publication.
func TestCapturedEnvelopeComponentPublication(t *testing.T) {
	t.Parallel()

	logical := jsontext.Value(`{"type":"string"}`)
	document := New("Events", "1", func(definition *Builder) {
		definition.Channel("events", "events", func(channel *ChannelBuilder) {
			channel.claimWireIdentity("event.created")
			channel.addEnvelope("event", jsontext.Value(`{"type":"object"}`), logical, nil)
		})
	})

	logical[0] = '!'

	data, err := document.JSON()

	if err != nil {
		t.Fatal(err)
	}

	if !bytes.Contains(data, []byte(`"x-arkoren-logical-payload":"#/components/schemas/events.event"`)) {
		t.Fatalf("logical payload pointer missing: %s", data)
	}

	if !bytes.Contains(data, []byte(`"schemas":{"events.event":{"type":"string"}}`)) {
		t.Fatalf("logical payload schema missing: %s", data)
	}
}

// TestCapturedEnvelopeDuplicateIdentity rejects typed envelopes that declare
// the same wire discriminator on one channel under different message IDs.
func TestCapturedEnvelopeDuplicateIdentity(t *testing.T) {
	t.Parallel()

	document := New("Events", "1", func(definition *Builder) {
		definition.Channel("events", "events", func(channel *ChannelBuilder) {
			channel.claimWireIdentity("event.created")
			channel.claimWireIdentity("event.created")
		})
	})

	if _, err := document.JSON(); err == nil || !strings.Contains(err.Error(), "wire discriminator") {
		t.Fatalf("expected duplicate discriminator, got %v", err)
	}
}
