package asyncapi_test

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"strings"
	"testing"

	"arkoren.dev/core/asyncapi"
	"arkoren.dev/core/service/broadcast"
	"arkoren.dev/core/service/queue"
)

// envelopeSchemaProvider retains bytes to expose incorrect borrowing and counts
// calls to ensure helpers capture logical schemas once, before callbacks.
type envelopeSchemaProvider struct {
	// data remains caller-owned after JSONSchema returns.
	data []byte
	// calls records the number of captures.
	calls int
	// err supplies a recognizable provider failure.
	err error
}

// JSONSchema returns retained data and an optional error. Nil receivers panic,
// so typed-nil tests also prove that capture never invokes their provider.
//
// Example:
//
//	provider := &envelopeSchemaProvider{data: []byte(`true`)}
//	_, err := provider.JSONSchema()
//	if err != nil {
//		panic(err)
//	}
func (provider *envelopeSchemaProvider) JSONSchema() ([]byte, error) {
	provider.calls++

	return provider.data, provider.err
}

// envelopeMember reads an object-only JSON path without converting numbers to
// float64. Missing members fail at the lookup rather than hiding behind nil.
func envelopeMember(t *testing.T, data []byte, path ...string) jsontext.Value {
	t.Helper()

	for _, key := range path {
		var object map[string]jsontext.Value

		if err := json.Unmarshal(data, &object); err != nil {
			t.Fatal(err)
		}

		value, ok := object[key]

		if !ok {
			t.Fatalf("missing member %q in %s", key, data)
		}

		data = value
	}

	return jsontext.Value(data)
}

// TestJSONTopicWireEnvelope compares the schema with an actual JSON-marshaled
// broadcast envelope after the logical payload has been JSON encoded.
func TestJSONTopicWireEnvelope(t *testing.T) {
	t.Parallel()

	topic := broadcast.NewTopic[string]("invoice.issued")

	payload, err := json.Marshal("inv-1")

	if err != nil {
		t.Fatal(err)
	}

	fixture := broadcast.Message{Name: topic.Name(), Payload: payload}
	document := asyncapi.New("Events", "1", func(definition *asyncapi.Builder) {
		definition.Channel("events", "billing", func(channel *asyncapi.ChannelBuilder) {
			channel.JSONTopic("issued", topic, asyncapi.RawSchema(`{"type":"string"}`), func(message *asyncapi.MessageBuilder) {
				message.WireExample("issued", fixture)
			})
		})
	})

	data, err := document.JSON()

	if err != nil {
		t.Fatal(err)
	}

	message := envelopeMember(t, data, "channels", "events", "messages", "issued")
	wire := envelopeMember(t, message, "payload", "schema")

	if got := string(envelopeMember(t, wire, "required")); got != `["name","payload"]` {
		t.Fatalf("wrong required fields: %s", got)
	}

	if got := string(envelopeMember(t, wire, "properties", "name", "const")); got != `"invoice.issued"` {
		t.Fatalf("wrong topic discriminator: %s", got)
	}

	if got := string(envelopeMember(t, wire, "properties", "payload")); got != `{"contentEncoding":"base64","contentMediaType":"application/json","type":"string"}` {
		t.Fatalf("wrong encoded payload: %s", got)
	}

	if got := string(envelopeMember(t, wire, "properties", "metadata")); got != `{"additionalProperties":{"type":"string"},"type":"object"}` {
		t.Fatalf("wrong metadata schema: %s", got)
	}

	if got := string(envelopeMember(t, message, "examples")); got != `[{"name":"issued","payload":{"name":"invoice.issued","payload":"Imludi0xIg=="}}]` {
		t.Fatalf("wrong actual wire example: %s", got)
	}

	if got := string(envelopeMember(t, message, "x-arkoren-logical-payload")); got != `"#/components/schemas/events.issued"` {
		t.Fatalf("logical pointer must be a plain string: %s", got)
	}
}

// TestJSONJobsSharePhysicalChannel proves that distinct job constants allow
// multiple messages on one queue and that attempts is not a policy constant.
func TestJSONJobsSharePhysicalChannel(t *testing.T) {
	t.Parallel()

	first := queue.NewJob[string]("send-invoice")
	second := queue.NewJob[string]("cancel-invoice")

	payload, err := json.Marshal("inv-1")

	if err != nil {
		t.Fatal(err)
	}

	fixture := queue.Message{ID: "job-1", Name: first.Name(), Queue: first.Queue(), Payload: payload, Attempts: 3}
	document := asyncapi.New("Jobs", "1", func(definition *asyncapi.Builder) {
		definition.Channel("jobs", first.Queue(), func(channel *asyncapi.ChannelBuilder) {
			channel.JSONJob("send", first, asyncapi.RawSchema(`true`), func(message *asyncapi.MessageBuilder) {
				message.WireExample("send", fixture)
			})
			channel.JSONJob("cancel", second, asyncapi.RawSchema(`false`))
		})
	})

	data, err := document.JSON()

	if err != nil {
		t.Fatal(err)
	}

	message := envelopeMember(t, data, "channels", "jobs", "messages", "send")
	wire := envelopeMember(t, message, "payload", "schema")

	if got := string(envelopeMember(t, wire, "required")); got != `["id","name","queue","payload","attempts"]` {
		t.Fatalf("wrong job required fields: %s", got)
	}

	if got := string(envelopeMember(t, wire, "properties", "attempts")); got != `{"type":"integer"}` {
		t.Fatalf("attempts was restricted: %s", got)
	}

	if got := string(envelopeMember(t, wire, "properties", "queue", "const")); got != `"default"` {
		t.Fatalf("wrong queue: %s", got)
	}

	if got := string(envelopeMember(t, wire, "properties", "name", "const")); got != `"send-invoice"` {
		t.Fatalf("wrong job: %s", got)
	}

	if got := string(envelopeMember(t, message, "examples")); got != `[{"name":"send","payload":{"id":"job-1","name":"send-invoice","queue":"default","payload":"Imludi0xIg==","attempts":3}}]` {
		t.Fatalf("wrong actual queue wire example: %s", got)
	}

	if got := string(envelopeMember(t, data, "components", "schemas", "jobs.cancel")); got != `false` {
		t.Fatalf("boolean logical schema changed: %s", got)
	}

	if got := string(envelopeMember(t, data, "channels", "jobs", "messages", "cancel", "payload", "schema", "properties", "name", "const")); got != `"cancel-invoice"` {
		t.Fatalf("second job discriminator changed: %s", got)
	}
}

// TestJSONEnvelopeCapturesLogicalPayloadBeforeCallbacks verifies one capture,
// exact numeric preservation and detachment from provider and retained drafts.
func TestJSONEnvelopeCapturesLogicalPayloadBeforeCallbacks(t *testing.T) {
	t.Parallel()

	provider := &envelopeSchemaProvider{data: []byte(`{"const":90071992547409931234567890}`)}
	var retained *asyncapi.MessageBuilder
	calls := 0
	document := asyncapi.New("Events", "1", func(definition *asyncapi.Builder) {
		definition.Channel("events", "events", func(channel *asyncapi.ChannelBuilder) {
			channel.JSONTopic("event", broadcast.NewTopic[int]("event.created"), provider, func(message *asyncapi.MessageBuilder) {
				calls++
				provider.data[0] = '!'
				retained = message.ContentType("application/octet-stream")
			})
		})
	})
	retained.ContentType("")

	data, err := document.JSON()

	if err != nil {
		t.Fatal(err)
	}

	if provider.calls != 1 || calls != 1 {
		t.Fatalf("provider=%d callbacks=%d", provider.calls, calls)
	}

	if got := string(envelopeMember(t, data, "components", "schemas", "events.event")); got != `{"const":90071992547409931234567890}` {
		t.Fatalf("logical schema changed: %s", got)
	}

	if got := string(envelopeMember(t, data, "channels", "events", "messages", "event", "contentType")); got != `"application/octet-stream"` {
		t.Fatalf("retained draft changed content type: %s", got)
	}
}

// TestJSONJobCapturesLogicalSchemaOnce verifies that a queue declaration owns
// provider bytes independently of the wire schema and defaults to JSON media.
func TestJSONJobCapturesLogicalSchemaOnce(t *testing.T) {
	t.Parallel()

	provider := &envelopeSchemaProvider{data: []byte(`{"type":"object","properties":{"invoice":{"type":"string"}}}`)}
	document := asyncapi.New("Jobs", "1", func(definition *asyncapi.Builder) {
		definition.Channel("jobs", "default", func(channel *asyncapi.ChannelBuilder) {
			channel.JSONJob("send", queue.NewJob[string]("send"), provider)
		})
	})

	provider.data[0] = '!'

	data, err := document.JSON()

	if err != nil {
		t.Fatal(err)
	}

	if provider.calls != 1 {
		t.Fatalf("provider calls=%d", provider.calls)
	}

	message := envelopeMember(t, data, "channels", "jobs", "messages", "send")

	if got := string(envelopeMember(t, message, "contentType")); got != `"application/json"` {
		t.Fatalf("wrong default content type: %s", got)
	}

	if got := string(envelopeMember(t, data, "components", "schemas", "jobs.send")); got != `{"type":"object","properties":{"invoice":{"type":"string"}}}` {
		t.Fatalf("logical schema changed: %s", got)
	}

	if got := string(envelopeMember(t, message, "payload", "schema", "properties", "payload", "type")); got != `"string"` {
		t.Fatalf("logical schema was substituted for wire payload: %s", got)
	}
}

// TestJSONTopicInvalidNameStillCapturesAndConfigures keeps declaration-time
// documentation errors independent from broadcast's runtime APIs.
func TestJSONTopicInvalidNameStillCapturesAndConfigures(t *testing.T) {
	t.Parallel()

	provider := &envelopeSchemaProvider{data: []byte(`true`)}
	calls := 0
	document := asyncapi.New("Events", "1", func(definition *asyncapi.Builder) {
		definition.Channel("events", "events", func(channel *asyncapi.ChannelBuilder) {
			channel.JSONTopic("event", broadcast.Topic[int]{}, provider, func(message *asyncapi.MessageBuilder) {
				calls++
				message.Summary("invalid topic")
			})
		})
	})

	if _, err := document.JSON(); !errors.Is(err, broadcast.ErrInvalidName) {
		t.Fatalf("expected topic name error, got %v", err)
	}

	if provider.calls != 1 || calls != 1 {
		t.Fatalf("provider=%d callbacks=%d", provider.calls, calls)
	}
}

// TestJSONJobInvalidDefinitionStillConfigures proves zero jobs fail document
// export without suppressing callbacks or mutating the job definition.
func TestJSONJobInvalidDefinitionStillConfigures(t *testing.T) {
	t.Parallel()

	job := queue.Job[int]{}
	calls := 0
	document := asyncapi.New("Jobs", "1", func(definition *asyncapi.Builder) {
		definition.Channel("jobs", "default", func(channel *asyncapi.ChannelBuilder) {
			channel.JSONJob("job", job, asyncapi.RawSchema(`true`), func(message *asyncapi.MessageBuilder) {
				calls++
				message.Summary("invalid job")
			})
		})
	})

	if _, err := document.JSON(); err == nil {
		t.Fatal("accepted zero job")
	}

	if calls != 1 || job.Err() == nil {
		t.Fatalf("callbacks=%d, job error=%v", calls, job.Err())
	}
}

// TestJSONJobTypedNilSchemaStillConfigures ensures typed nil providers become
// deferred schema errors without calling their methods or skipping policies.
func TestJSONJobTypedNilSchemaStillConfigures(t *testing.T) {
	t.Parallel()

	var provider *envelopeSchemaProvider
	calls := 0
	document := asyncapi.New("Jobs", "1", func(definition *asyncapi.Builder) {
		definition.Channel("jobs", "default", func(channel *asyncapi.ChannelBuilder) {
			channel.JSONJob("job", queue.NewJob[int]("send"), provider, func(message *asyncapi.MessageBuilder) {
				calls++
				message.Summary("nil schema")
			})
		})
	})

	if _, err := document.JSON(); err == nil || !strings.Contains(err.Error(), "nil provider") {
		t.Fatalf("expected nil provider error, got %v", err)
	}

	if calls != 1 {
		t.Fatalf("callbacks=%d", calls)
	}
}

// TestJSONEnvelopePreservesFirstSchemaError checks that later callback errors
// cannot replace the provider's original cause and no recapture occurs.
func TestJSONEnvelopePreservesFirstSchemaError(t *testing.T) {
	t.Parallel()

	cause := errors.New("logical payload unavailable")
	provider := &envelopeSchemaProvider{err: cause}
	document := asyncapi.New("Events", "1", func(definition *asyncapi.Builder) {
		definition.Channel("events", "events", func(channel *asyncapi.ChannelBuilder) {
			channel.JSONTopic("event", broadcast.NewTopic[int]("event.created"), provider, func(message *asyncapi.MessageBuilder) {
				message.WireExample("invalid", make(chan int))
			})
		})
	})

	if _, err := document.JSON(); !errors.Is(err, cause) {
		t.Fatalf("lost provider error: %v", err)
	}

	if provider.calls != 1 {
		t.Fatalf("provider calls=%d", provider.calls)
	}
}

// TestJSONTopicRejectsDuplicateWireIdentity rejects aliases for one typed
// topic even when their documentation message IDs differ.
func TestJSONTopicRejectsDuplicateWireIdentity(t *testing.T) {
	t.Parallel()

	topic := broadcast.NewTopic[int]("event.created")
	document := asyncapi.New("Events", "1", func(definition *asyncapi.Builder) {
		definition.Channel("events", "events", func(channel *asyncapi.ChannelBuilder) {
			channel.JSONTopic("first", topic, asyncapi.RawSchema(`true`))
			channel.JSONTopic("second", topic, asyncapi.RawSchema(`true`))
		})
	})

	if _, err := document.JSON(); err == nil || !strings.Contains(err.Error(), "wire discriminator") {
		t.Fatalf("expected duplicate wire identity, got %v", err)
	}
}

// TestJSONJobRejectsDuplicateWireIdentity rejects duplicate queue/name pairs
// independently from the channel's logical documentation message IDs.
func TestJSONJobRejectsDuplicateWireIdentity(t *testing.T) {
	t.Parallel()

	job := queue.NewJob[int]("send")
	document := asyncapi.New("Jobs", "1", func(definition *asyncapi.Builder) {
		definition.Channel("jobs", "default", func(channel *asyncapi.ChannelBuilder) {
			channel.JSONJob("first", job, asyncapi.RawSchema(`true`))
			channel.JSONJob("second", job, asyncapi.RawSchema(`true`))
		})
	})

	if _, err := document.JSON(); err == nil || !strings.Contains(err.Error(), "wire discriminator") {
		t.Fatalf("expected duplicate wire identity, got %v", err)
	}
}

// TestJSONTopicThenJobRejectsSharedName catches cross-family ambiguity when a
// job is registered after a topic, while still running both message callbacks.
func TestJSONTopicThenJobRejectsSharedName(t *testing.T) {
	t.Parallel()

	calls := 0
	configure := func(message *asyncapi.MessageBuilder) {
		calls++
		message.Summary("created")
	}

	document := asyncapi.New("Events", "1", func(definition *asyncapi.Builder) {
		definition.Channel("events", "events", func(channel *asyncapi.ChannelBuilder) {
			channel.JSONTopic("topic", broadcast.NewTopic[int]("created"), asyncapi.RawSchema(`true`), configure)
			channel.JSONJob("job", queue.NewJob[int]("created"), asyncapi.RawSchema(`true`), configure)
		})
	})

	if _, err := document.JSON(); err == nil || !strings.Contains(err.Error(), "overlapping wire discriminator") {
		t.Fatalf("expected cross-family name overlap, got %v", err)
	}

	if calls != 2 {
		t.Fatalf("callbacks=%d", calls)
	}
}

// TestJSONJobThenTopicRejectsSharedName proves overlap detection does not
// depend on which envelope family is declared first on a channel.
func TestJSONJobThenTopicRejectsSharedName(t *testing.T) {
	t.Parallel()

	calls := 0
	configure := func(message *asyncapi.MessageBuilder) {
		calls++
		message.Summary("created")
	}

	document := asyncapi.New("Events", "1", func(definition *asyncapi.Builder) {
		definition.Channel("events", "events", func(channel *asyncapi.ChannelBuilder) {
			channel.JSONJob("job", queue.NewJob[int]("created"), asyncapi.RawSchema(`true`), configure)
			channel.JSONTopic("topic", broadcast.NewTopic[int]("created"), asyncapi.RawSchema(`true`), configure)
		})
	})

	if _, err := document.JSON(); err == nil || !strings.Contains(err.Error(), "overlapping wire discriminator") {
		t.Fatalf("expected cross-family name overlap, got %v", err)
	}

	if calls != 2 {
		t.Fatalf("callbacks=%d", calls)
	}
}

// TestJSONTopicAndJobAllowDifferentNames keeps mixed channels valid when their
// name constants distinguish the complete wire envelopes.
func TestJSONTopicAndJobAllowDifferentNames(t *testing.T) {
	t.Parallel()

	document := asyncapi.New("Events", "1", func(definition *asyncapi.Builder) {
		definition.Channel("events", "events", func(channel *asyncapi.ChannelBuilder) {
			channel.JSONTopic("topic", broadcast.NewTopic[int]("created"), asyncapi.RawSchema(`true`))
			channel.JSONJob("job", queue.NewJob[int]("send"), asyncapi.RawSchema(`true`))
		})
	})

	if _, err := document.JSON(); err != nil {
		t.Fatal(err)
	}
}

// TestJSONJobsAllowSharedNameOnDifferentQueues verifies that job-name markers
// do not reject schemas distinguished by their queue constants.
func TestJSONJobsAllowSharedNameOnDifferentQueues(t *testing.T) {
	t.Parallel()

	first := queue.NewJob[int]("created", func(job *queue.JobBuilder) {
		job.OnQueue("first")
	})

	second := queue.NewJob[int]("created", func(job *queue.JobBuilder) {
		job.OnQueue("second")
	})

	document := asyncapi.New("Jobs", "1", func(definition *asyncapi.Builder) {
		definition.Channel("jobs", "", func(channel *asyncapi.ChannelBuilder) {
			channel.JSONJob("first", first, asyncapi.RawSchema(`true`))
			channel.JSONJob("second", second, asyncapi.RawSchema(`true`))
		})
	})

	data, err := document.JSON()

	if err != nil {
		t.Fatal(err)
	}

	firstWire := envelopeMember(t, data, "channels", "jobs", "messages", "first", "payload", "schema")
	secondWire := envelopeMember(t, data, "channels", "jobs", "messages", "second", "payload", "schema")

	if got := string(envelopeMember(t, firstWire, "properties", "queue", "const")); got != `"first"` {
		t.Fatalf("wrong first queue constant: %s", got)
	}

	if got := string(envelopeMember(t, secondWire, "properties", "queue", "const")); got != `"second"` {
		t.Fatalf("wrong second queue constant: %s", got)
	}
}

// TestJSONEnvelopeNilCallbackPanics preserves ordinary construction-callback
// misuse semantics even when a typed definition is already invalid.
func TestJSONEnvelopeNilCallbackPanics(t *testing.T) {
	t.Parallel()

	defer func() {
		if recover() == nil {
			t.Fatal("nil callback did not panic")
		}
	}()
	asyncapi.New("Jobs", "1", func(definition *asyncapi.Builder) {
		definition.Channel("jobs", "default", func(channel *asyncapi.ChannelBuilder) {
			channel.JSONJob("job", queue.Job[int]{}, asyncapi.RawSchema(`true`), nil)
		})
	})
}
