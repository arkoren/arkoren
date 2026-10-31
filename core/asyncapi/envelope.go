package asyncapi

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"

	"arkoren.dev/core/service/broadcast"
	"arkoren.dev/core/service/queue"
)

// JSONTopic documents a driver that JSON-marshals broadcast.Message, not every
// possible broadcast transport. The supplied schema describes the decoded
// logical payload; it is captured once into components/schemas/<channel>.<id>.
// The message's x-arkoren-logical-payload annotation is a plain JSON Pointer to
// that component. Standard tools may not display this custom annotation.
//
// The wire payload is a base64 string, not the logical JSON object. Draft 7's
// contentEncoding and contentMediaType are annotations, not decoding validation.
// Invalid topic names and schema errors are deferred to Document.JSON without
// changing publishing behavior. Callbacks run once even for invalid inputs;
// nil callbacks panic. JSON is the default content type; use ContentType for
// transports such as MQTT that advertise application/octet-stream.
// Typed helpers reject duplicate discriminator identities on the channel;
// a topic and job cannot share a name because their wire schemas overlap.
// Overlap with raw Message schemas remains the caller's responsibility.
//
// Example:
//
//	topic := broadcast.NewTopic[string]("invoice.issued")
//	document := asyncapi.New("Billing", "1", func(definition *asyncapi.Builder) {
//		definition.Channel("invoices", topic.Name(), func(channel *asyncapi.ChannelBuilder) {
//			channel.JSONTopic("issued", topic, asyncapi.RawSchema(`{"type":"string"}`))
//		})
//	})
//	if _, err := document.JSON(); err != nil {
//		log.Fatal(err)
//	}
func (channel *ChannelBuilder) JSONTopic[T any](
	id string,
	topic broadcast.Topic[T],
	payload Schema,
	configure ...MessageBuilderFunc,
) *ChannelBuilder {
	channel.fail(broadcast.ValidateName(topic.Name()))

	logical, err := captureSchema(payload)
	channel.fail(err)

	properties := envelopeProperties(topic.Name())
	properties["id"] = map[string]any{"type": "string"}

	wire, err := marshalEnvelope(properties, []string{"name", "payload"})
	channel.fail(err)
	channel.claimTopicIdentity(topic.Name())

	return channel.addEnvelope(id, wire, logical, configure)
}

// JSONJob documents a driver that JSON-marshals queue.Message, not a universal
// queue transport. The supplied schema describes the decoded logical payload,
// captured once into components/schemas/<channel>.<id>. The message's
// x-arkoren-logical-payload annotation points to it as a plain JSON Pointer;
// standard tools may not display that annotation.
//
// The complete wire schema uses a base64 payload string. Its Draft 7 content
// annotations do not validate decoded data. Name and queue are constants from
// the job; attempts remains an integer because deliveries can change it.
// Job.Err and schema errors are deferred to Document.JSON, without changing
// dispatch or registration. Callbacks run once even for invalid definitions;
// nil callbacks panic. ContentType can override the application/json default.
// Typed helpers reject duplicate discriminator identities on the channel;
// a job and topic cannot share a name, but jobs on different queues can.
// Overlap with raw Message schemas remains the caller's responsibility.
//
// Example:
//
//	job := queue.NewJob[string]("send-invoice")
//	document := asyncapi.New("Billing", "1", func(definition *asyncapi.Builder) {
//		definition.Channel("jobs", job.Queue(), func(channel *asyncapi.ChannelBuilder) {
//			channel.JSONJob("send", job, asyncapi.RawSchema(`{"type":"string"}`))
//		})
//	})
//	if _, err := document.JSON(); err != nil {
//		log.Fatal(err)
//	}
func (channel *ChannelBuilder) JSONJob[T any](
	id string,
	job queue.Job[T],
	payload Schema,
	configure ...MessageBuilderFunc,
) *ChannelBuilder {
	channel.fail(job.Err())

	logical, err := captureSchema(payload)
	channel.fail(err)

	properties := envelopeProperties(job.Name())
	properties["id"] = map[string]any{"type": "string"}
	properties["queue"] = map[string]any{"type": "string", "const": job.Queue()}
	properties["attempts"] = map[string]any{"type": "integer"}

	wire, err := marshalEnvelope(properties, []string{"id", "name", "queue", "payload", "attempts"})
	channel.fail(err)
	channel.claimJobIdentity(job.Name(), job.Queue())

	return channel.addEnvelope(id, wire, logical, configure)
}

// claimTopicIdentity reserves a topic name and rejects any previously declared
// job with that name. A job's extra fields do not disqualify it from matching
// the topic schema, so a queue discriminator cannot resolve this overlap.
func (channel *ChannelBuilder) claimTopicIdentity(name string) {
	if channel.identities["queue-name:"+name] {
		channel.fail(fmt.Errorf("overlapping wire discriminator name %q between topic and job", name))
	}

	channel.claimWireIdentity("broadcast:" + name)
}

// claimJobIdentity tracks job names separately from queue/name identities so
// later topics can detect overlap without forbidding disjoint queue constants.
// The queue-name marker namespace cannot collide with exact identity keys.
func (channel *ChannelBuilder) claimJobIdentity(name, destination string) {
	if channel.identities["broadcast:"+name] {
		channel.fail(fmt.Errorf("overlapping wire discriminator name %q between topic and job", name))
	}

	channel.identities["queue-name:"+name] = true
	channel.claimWireIdentity("queue:" + destination + ":" + name)
}

// envelopeProperties describes the shared fields of JSON-marshaled core
// envelopes. Each call owns its maps; metadata and identity presence are kept
// separate from the required field lists of the two envelope families.
func envelopeProperties(name string) map[string]any {
	return map[string]any{
		"name": map[string]any{"type": "string", "const": name},
		"payload": map[string]any{
			"type": "string", "contentEncoding": "base64", "contentMediaType": "application/json",
		},
		"metadata": map[string]any{
			"type": "object", "additionalProperties": map[string]any{"type": "string"},
		},
	}
}

// marshalEnvelope encodes only the complete wire shape, never the logical
// payload schema. JSON encoding errors are returned for sticky draft capture.
func marshalEnvelope(properties map[string]any, required []string) (jsontext.Value, error) {
	data, err := json.Marshal(map[string]any{
		"type": "object", "properties": properties, "required": required,
	}, json.Deterministic(true))

	return jsontext.Value(data), err
}
