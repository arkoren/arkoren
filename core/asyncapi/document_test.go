package asyncapi_test

import (
	"bytes"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"strings"
	"sync"
	"testing"

	"arkoren.dev/core/asyncapi"
)

// TestDocumentGraph checks wire references, the explicit draft-07 schema
// boundary, application direction and deterministic repeated exports.
func TestDocumentGraph(t *testing.T) {
	t.Parallel()

	document := asyncapi.New("Billing", "1", func(definition *asyncapi.Builder) {
		definition.Send("publish", "invoices", func(action *asyncapi.OperationBuilder) {
			action.Messages("issued")
		})
		definition.Channel("invoices", "billing.invoices", func(channel *asyncapi.ChannelBuilder) {
			channel.Message("issued", asyncapi.RawSchema(`{"type":"object"}`))
		})
	})

	data, err := document.JSON()

	if err != nil {
		t.Fatal(err)
	}

	for _, fragment := range []string{
		`"asyncapi":"3.1.0"`, `"action":"send"`,
		`"$ref":"#/channels/invoices/messages/issued"`,
		`"schemaFormat":"application/schema+json;version=draft-07"`,
		`"contentType":"application/json"`,
	} {
		if !bytes.Contains(data, []byte(fragment)) {
			t.Fatalf("missing %s in %s", fragment, data)
		}
	}
}

// TestDocumentCopiesReturnedJSON ensures callers cannot mutate cached exports.
func TestDocumentCopiesReturnedJSON(t *testing.T) {
	t.Parallel()

	document := asyncapi.New("Events", "1")

	data, err := document.JSON()

	if err != nil {
		t.Fatal(err)
	}

	data[0] = '!'

	next, err := document.JSON()

	if err != nil {
		t.Fatal(err)
	}

	if next[0] != '{' {
		t.Fatalf("caller modified cache: %s", next)
	}
}

// TestDocumentConcurrentExports verifies immutable cache access under the race
// detector without sharing returned mutable bytes between callers.
func TestDocumentConcurrentExports(t *testing.T) {
	t.Parallel()

	document := asyncapi.New("Events", "1")
	var workers sync.WaitGroup

	for range 12 {
		workers.Go(func() {
			data, err := document.JSON()

			if err != nil {
				t.Error(err)

				return
			}

			data[0] = '!'
		})
	}

	workers.Wait()
}

// TestDocumentCachesErrors ensures repeated export returns the same error,
// rather than rerunning validation or observing retained draft changes.
func TestDocumentCachesErrors(t *testing.T) {
	t.Parallel()

	document := asyncapi.New("", "1")

	_, first := document.JSON()

	_, second := document.JSON()

	if first == nil || !errors.Is(first, second) {
		t.Fatalf("errors not cached: %v, %v", first, second)
	}
}

// TestDocumentZeroJSON rejects the unconstructed value without panicking.
func TestDocumentZeroJSON(t *testing.T) {
	t.Parallel()

	var document asyncapi.Document

	if _, err := document.JSON(); err == nil {
		t.Fatal("zero document accepted")
	}
}

// TestDocumentNilJSON rejects a nil receiver without panicking.
func TestDocumentNilJSON(t *testing.T) {
	t.Parallel()

	var document *asyncapi.Document

	if _, err := document.JSON(); err == nil {
		t.Fatal("nil document accepted")
	}
}

// TestDocumentRetainedDrafts verifies publication detaches every child before
// a caller can mutate its retained draft, including its captured examples.
func TestDocumentRetainedDrafts(t *testing.T) {
	t.Parallel()

	var root *asyncapi.Builder
	var channelDraft *asyncapi.ChannelBuilder
	var messageDraft *asyncapi.MessageBuilder
	var serverDraft *asyncapi.ServerBuilder
	var actionDraft *asyncapi.OperationBuilder
	example := map[string]any{"id": "original"}
	document := asyncapi.New("Events", "1", func(definition *asyncapi.Builder) {
		root = definition
		definition.Server("main", "broker.example:9092", "kafka", func(listener *asyncapi.ServerBuilder) {
			serverDraft = listener
		})
		definition.Channel("events", "events", func(channel *asyncapi.ChannelBuilder) {
			channelDraft = channel
			channel.Servers("main").Message("event", asyncapi.RawSchema(`true`), func(message *asyncapi.MessageBuilder) {
				messageDraft = message
				message.WireExample("original", example)
			})
		})
		definition.Receive("consume", "events", func(action *asyncapi.OperationBuilder) {
			actionDraft = action
			action.Messages("event")
		})
	})
	root.Description("mutated")
	channelDraft.Description("mutated")
	messageDraft.Summary("mutated")
	serverDraft.Description("mutated")
	actionDraft.Summary("mutated")

	example["id"] = "mutated"

	data, err := document.JSON()

	if err != nil {
		t.Fatal(err)
	}

	if bytes.Contains(data, []byte("mutated")) {
		t.Fatalf("retained mutable data escaped: %s", data)
	}
}

// TestDocumentCallbacksRunOnceInOrder checks callbacks run even for invalid
// required inputs, without replay during JSON export.
func TestDocumentCallbacksRunOnceInOrder(t *testing.T) {
	t.Parallel()

	var calls []string
	document := asyncapi.New("", "", func(definition *asyncapi.Builder) {
		calls = append(calls, "first")
		definition.Channel("", "", func(channel *asyncapi.ChannelBuilder) {
			calls = append(calls, "child")
		})
	}, func(document *asyncapi.Builder) {
		calls = append(calls, "last")
	})

	_, err := document.JSON()

	if err == nil {
		t.Fatal("invalid declaration accepted")
	}

	if strings.Join(calls, ",") != "first,child,last" {
		t.Fatalf("unexpected callback execution: %v", calls)
	}
}

// TestDocumentNilCallbackPanics preserves direct construction callback misuse.
func TestDocumentNilCallbackPanics(t *testing.T) {
	t.Parallel()

	defer func() {
		if recover() == nil {
			t.Fatal("nil callback did not panic")
		}
	}()
	asyncapi.New("Events", "1", nil)
}

// TestDocumentLargeNumbersRemainExact verifies examples and unknown binding
// metadata never pass through a float64 conversion.
func TestDocumentLargeNumbersRemainExact(t *testing.T) {
	t.Parallel()

	document := asyncapi.New("Events", "1", func(definition *asyncapi.Builder) {
		definition.Channel("events", "", func(channel *asyncapi.ChannelBuilder) {
			channel.Binding("custom", asyncapi.RawObject(`{"large":900719925474099312345}`))
			channel.Message("event", asyncapi.RawSchema(`true`), func(message *asyncapi.MessageBuilder) {
				message.WireExample("large", jsontext.Value(`{"large":900719925474099312345}`))
			})
		})
	})

	data, err := document.JSON()

	if err != nil {
		t.Fatal(err)
	}

	if bytes.Count(data, []byte("900719925474099312345")) != 2 {
		t.Fatalf("large values changed: %s", data)
	}

	var value map[string]jsontext.Value

	if err := json.Unmarshal(data, &value); err != nil {
		t.Fatal(err)
	}
}
