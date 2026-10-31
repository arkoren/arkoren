---
title: Messages and Schemas
description: Distinguish logical values from wire envelopes, attach examples, and choose a supported schema for each message.
order: 20
---

# Messages and Schemas

## Introduction

A consumer needs the shape of the bytes it receives, not only the Go value your
publisher started with. AsyncAPI message declarations describe the complete
wire payload. The typed helpers also preserve the decoded application schema
so readers can understand both layers.

Start with the typed topic and `Document` in the
[AsyncAPI overview](overview.md#generating-your-first-document). This page changes
that declaration without repeating the broker and operation setup.

## Understanding the wire envelope

`JSONTopic` describes a JSON-encoded Arkoren broadcast envelope. For the
`OrderCreated{OrderID: "ord-1"}` value, a wire payload can look like this:

```json
{
  "name": "order.created",
  "payload": "eyJvcmRlcl9pZCI6Im9yZC0xIn0="
}
```

The `name` is the topic discriminator. The `payload` is a **base64 string**
containing the JSON encoding of `OrderCreated`, not an inline object. The wire
schema includes the name constant and optional envelope metadata. After base64
decoding, the application value is:

```json
{
  "order_id": "ord-1"
}
```

`JSONTopic` separately stores that logical schema under
`components.schemas["orders.created"]`: the component key combines the channel
ID and message ID, not their transport names. The channel message includes this
annotation:

```json
{
  "x-arkoren-logical-payload": "#/components/schemas/orders.created"
}
```

This plain pointer string is an Arkoren annotation, **not a `$ref` object**.
Neither it nor the wire schema's Draft 7 content annotations validates decoded
base64 contents. A standard AsyncAPI viewer may not follow the annotation; read
the referenced component separately for the decoded value's shape.

`JSONJob` follows the same separation for queue envelopes. Its wire schema also
contains the job name, logical queue, and delivery fields; see
[Queues and Operations](queues-and-operations.md#documenting-two-jobs-on-one-queue).

## Adding a wire example

Examples help readers recognize a real delivery, but they must represent the
whole envelope rather than only `OrderCreated`. With the overview's imports and
`Created` topic, replace the original `JSONTopic` call with:

```go
channel.JSONTopic("created", Created, validate.Infer[OrderCreated](), func(message *asyncapi.MessageBuilder) {
	message.Summary("An order was created")
	message.Description("Published after the order is committed.")
	message.WireExample("created-order", map[string]any{
		"name":    "order.created",
		"payload": "eyJvcmRlcl9pZCI6Im9yZC0xIn0=",
	})
})
```

`WireExample` encodes and copies the value when declared. Examples are JSON
documentation values and are not checked against the schema. Unsupported JSON
values become deferred export errors. Keep private data out of examples.

The description records when your application publishes the message; it does not
arrange publication after a commit. Keep that statement aligned with your runtime
publisher.

## Matching a transport's content type

The default content type is `application/json`. If your transport advertises a
different media type, override it to match. For example, Arkoren's MQTT transport
advertises its JSON envelope as `application/octet-stream`; use this message
callback instead:

```go
channel.JSONTopic("created", Created, validate.Infer[OrderCreated](), func(message *asyncapi.MessageBuilder) {
	message.ContentType("application/octet-stream")
})
```

This changes documentation, not transport bytes or headers. Use the typed helper
only when your transport actually carries that JSON envelope.

## Describing another wire format

An integration may send a text status instead of an Arkoren envelope. Use
`Message` with the actual wire schema in an existing channel callback:

```go
channel.Message("status", asyncapi.RawSchema(`{"type":"string"}`), func(message *asyncapi.MessageBuilder) {
	message.ContentType("text/plain")
	message.WireExample("ready", "ready")
})
```

This declares the text payload itself; there is no implicit Arkoren envelope or
separate logical schema. Select `status` in the relevant operation's `Messages`
call. For multiple raw messages on a channel, ensure their schemas distinguish
possible wire values yourself; the exporter does not prove schema disjointness.

## Choosing a schema

Schema providers implement `JSONSchema() ([]byte, error)`. Published validation
schemas and `validate.Infer[T]()` satisfy this interface. During `New`, each
message declaration calls its non-nil provider once and immediately copies the
returned bytes. Reusing a provider in several message declarations calls it once
per declaration. Provider errors are retained for `JSON()` or the handler.

Every provider's output must fit the supported, self-contained Draft 7 subset.
Schema acceptance does not prove equivalence to runtime validation:
normalization, cross-field rules, custom encoders, and domain checks still need
review. The exporter does not perform instance validation or fetch schemas.

`RawSchema` accepts object and boolean schemas, composition, and Draft 7 tuple
`items`. For example, a raw message can accept either a nonempty identifier or
an integer status code:

```go
channel.Message("result", asyncapi.RawSchema(`{
  "oneOf": [
    {"type": "string", "minLength": 1},
    {"type": "integer"}
  ]
}`))
```

The keyword gate checks schema-bearing positions and keyword shapes, not
satisfiability. `$ref`, `$id`, definitions, unknown non-extension keywords, and
other dialects are rejected; even Draft 7 `dependencies` is unsupported.
Arbitrary Draft 2020-12 schemas are not converted. Do not relabel a newer schema
as Draft 7 to bypass these limits. If present, `$schema` must be
`http://json-schema.org/draft-07/schema#`.

`x-` extension contents and values inside `default`, `const`, `enum`, and
`examples` remain opaque data, not nested schemas. Formats and patterns are
annotations here, not executed constraints. Exported messages use an explicit
Multi Format Schema Object with `application/schema+json;version=draft-07`.

### Supported Draft 7 profile

Use this profile when preparing a raw schema or checking a provider's output.
It describes accepted declaration shapes, not runtime instance validation:

| Group | Keywords |
| --- | --- |
| Types and values | `type`, `enum`, `const` |
| Objects | `properties`, `patternProperties`, `additionalProperties`, `propertyNames`, `required`, `minProperties`, `maxProperties` |
| Arrays | `items` (schema or nonempty tuple), `additionalItems`, `contains`, `uniqueItems`, `minItems`, `maxItems` |
| Composition and conditions | `allOf`, `anyOf`, `oneOf`, `not`, `if`, `then`, `else` |
| Numbers | `minimum`, `maximum`, `exclusiveMinimum`, `exclusiveMaximum`, `multipleOf` |
| Strings | `minLength`, `maxLength`, `pattern`, `format`, `contentEncoding`, `contentMediaType` |
| Documentation | `title`, `description`, `default`, `examples`, `readOnly`, `writeOnly`, `$comment` |
| Dialect and extensions | `$schema`, any `x-` keyword |

Composition arrays must be nonempty, counts nonnegative integers, and
`multipleOf` positive. Newer features such as `$defs`, `prefixItems`, and
`contentSchema` are rejected in schema positions. The gate does not execute
constraints or decode encoded content.

For message options, run `go doc arkoren.dev/core/asyncapi.MessageBuilder`
in your application's module, or see the
[`asyncapi` source reference](https://pkg.go.dev/arkoren.dev/core/asyncapi).
