package asyncapi_test

import (
	"strings"
	"testing"

	"arkoren.dev/core/asyncapi"
)

// TestOperationRequiresMessages rejects implicit all-message operations.
func TestOperationRequiresMessages(t *testing.T) {
	t.Parallel()

	document := asyncapi.New("Events", "1", func(definition *asyncapi.Builder) {
		definition.Channel("events", "events")
		definition.Send("publish", "events")
	})

	if _, err := document.JSON(); err == nil || !strings.Contains(err.Error(), "messages") {
		t.Fatalf("expected selection error, got %v", err)
	}
}

// TestOperationMissingMessage rejects unresolved channel message references.
func TestOperationMissingMessage(t *testing.T) {
	t.Parallel()

	document := asyncapi.New("Events", "1", func(definition *asyncapi.Builder) {
		definition.Channel("events", "events")
		definition.Send("publish", "events", func(action *asyncapi.OperationBuilder) {
			action.Messages("missing")
		})
	})

	if _, err := document.JSON(); err == nil || !strings.Contains(err.Error(), "unresolved") {
		t.Fatalf("expected unresolved reference, got %v", err)
	}
}

// TestOperationDuplicateSelection rejects duplicates across fluent calls.
func TestOperationDuplicateSelection(t *testing.T) {
	t.Parallel()

	document := asyncapi.New("Events", "1", func(definition *asyncapi.Builder) {
		definition.Send("publish", "events", func(action *asyncapi.OperationBuilder) {
			action.Messages("event").Messages("event")
		})
	})

	if _, err := document.JSON(); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("expected duplicate reference, got %v", err)
	}
}

// TestAnnotationLikeGraphIDsAllowForwardReferences proves collection keys are
// identities, not semantic fields to skip. All graph levels use each spelling.
func TestAnnotationLikeGraphIDsAllowForwardReferences(t *testing.T) {
	t.Parallel()

	for _, id := range []string{"example", "default", "schema", "x-send"} {
		document := asyncapi.New("Events", "1", func(definition *asyncapi.Builder) {
			definition.Send(id, id, func(action *asyncapi.OperationBuilder) {
				action.Messages(id).Security(id)
			})
			definition.Channel(id, "events", func(channel *asyncapi.ChannelBuilder) {
				channel.Servers(id).Message(id, asyncapi.RawSchema(`true`))
			})
			definition.Server(id, "broker.example:9092", "kafka", func(listener *asyncapi.ServerBuilder) {
				listener.Security(id)
			})
			definition.SecurityScheme(id, asyncapi.RawObject(`{"type":"userPassword"}`))
		})

		if _, err := document.JSON(); err != nil {
			t.Fatalf("id %q: %v", id, err)
		}
	}
}

// TestAnnotationLikeOperationIDsRejectMissingChannels covers the former walker
// bypass: an operation ID never suppresses validation of its channel edge.
func TestAnnotationLikeOperationIDsRejectMissingChannels(t *testing.T) {
	t.Parallel()

	for _, id := range []string{"example", "default", "schema", "x-send"} {
		document := asyncapi.New("Events", "1", func(definition *asyncapi.Builder) {
			definition.Send(id, "missing", func(action *asyncapi.OperationBuilder) {
				action.Messages("missing")
			})
		})

		if _, err := document.JSON(); err == nil || !strings.Contains(err.Error(), "#/channels/missing") {
			t.Fatalf("operation %q: expected missing channel, got %v", id, err)
		}
	}
}

// TestAnnotationLikeOperationIDsRejectMissingMessages checks message membership
// even when the containing operation ID resembles an annotation field.
func TestAnnotationLikeOperationIDsRejectMissingMessages(t *testing.T) {
	t.Parallel()

	for _, id := range []string{"example", "default", "schema", "x-send"} {
		document := asyncapi.New("Events", "1", func(definition *asyncapi.Builder) {
			definition.Channel("events", "events")
			definition.Send(id, "events", func(action *asyncapi.OperationBuilder) {
				action.Messages(id)
			})
		})

		if _, err := document.JSON(); err == nil || !strings.Contains(err.Error(), "#/channels/events/messages/"+id) {
			t.Fatalf("operation %q: expected missing message, got %v", id, err)
		}
	}
}

// TestAnnotationLikeOperationIDsRejectMissingSecurity checks the operation's
// independent security edge rather than relying on a generic object walk.
func TestAnnotationLikeOperationIDsRejectMissingSecurity(t *testing.T) {
	t.Parallel()

	for _, id := range []string{"example", "default", "schema", "x-send"} {
		document := asyncapi.New("Events", "1", func(definition *asyncapi.Builder) {
			definition.Channel("events", "events", func(channel *asyncapi.ChannelBuilder) {
				channel.Message("event", asyncapi.RawSchema(`true`))
			})
			definition.Send(id, "events", func(action *asyncapi.OperationBuilder) {
				action.Messages("event").Security(id)
			})
		})

		if _, err := document.JSON(); err == nil || !strings.Contains(err.Error(), "#/components/securitySchemes/"+id) {
			t.Fatalf("operation %q: expected missing security scheme, got %v", id, err)
		}
	}
}
