---
title: Messages and Schemas
description: Describe complete wire messages, expose decoded payload shapes, and work within the supported Draft 7 schema profile.
order: 20
summary:
  - Distinguish complete wire envelopes from their separately documented logical payloads.
  - Attach fictional wire examples and match message media types to the actual transport.
  - Choose schema providers within the supported Draft 7 profile without implying runtime validation.
---

# Messages and Schemas

A subscriber receives transport bytes, not your publisher's Go struct. Describe
that complete **wire payload** in the message schema. For Arkoren JSON envelopes,
the typed helpers also expose a **logical payload** schema: the application value
inside the envelope after decoding.

The examples reuse `Ready`, `ReportReady`, and the `reports` channel from the
[overview](overview.md#shared-definitions). They use `asyncapi`, `broadcast`, and
`validate` from `arkoren.dev/core/asyncapi`, `arkoren.dev/core/service/broadcast`,
and `arkoren.dev/foundation/validate`.

## Wire and Logical

`JSONTopic("ready", Ready, validate.Infer[ReportReady]())` describes the JSON
encoding of `broadcast.Message`. A minimal report announcement looks like this
on the wire:

```json
{
  "name": "report.ready",
  "payload": "eyJyZXBvcnRfaWQiOiJycHQtMSJ9"
}
```

The `payload` string is base64-encoded JSON. Decode it to obtain the logical
value `{"report_id":"rpt-1"}`. The wire schema requires `name` and `payload`,
fixes `name` to `report.ready`, and also describes optional `id` and string-valued
`metadata` fields. It does not replace the base64 string with the logical object.

The helper puts the logical schema in
`components.schemas["reports.ready"]`, using the channel ID and message ID, not
the topic name or address. On the channel message it adds:

```json
{
  "x-arkoren-logical-payload": "#/components/schemas/reports.ready"
}
```

This is a plain JSON Pointer string in an Arkoren annotation, not a `$ref`
object. External viewers might not follow it. The wire schema's Draft 7
`contentEncoding` and `contentMediaType` annotations do not decode or validate
the base64 contents; read the logical component separately.

`JSONJob` uses the same separation for a queue message. It also describes job
identity and delivery fields, as shown in
[Queues and Operations](queues-and-operations.md#shared-queue-messages).

## Wire Examples

Examples let consumers recognize deliveries, but they are exported data. Use
fictional values and review schemas for internal field names before distributing
the document. An inferred schema exposes the described fields even if examples
omit them; inference is not a privacy filter.

To attach a report announcement without manually encoding base64, prepare a real
wire envelope. Import `encoding/json/v2` for the logical JSON encoding:

```go
payload, err := json.Marshal(ReportReady{ReportID: "rpt-1"})
```

After successful encoding, construct the envelope with those bytes:

```go
example := broadcast.Message{
	Name:    Ready.Name(),
	Payload: payload,
}
```

Use that value in place of the overview's original `JSONTopic` declaration,
inside the `reports` channel callback:

```go
channel.JSONTopic("ready", Ready, validate.Infer[ReportReady](), func(message *asyncapi.MessageBuilder) {
	message.Summary("A report is ready")
	message.Description("Published after the report artifact is stored.")
	message.WireExample("completed-report", example)
})
```

`WireExample` JSON-encodes and copies the whole envelope at declaration time, so
its byte payload becomes the base64 string shown above. It does not check that
the example satisfies the schema. Unsupported JSON values become deferred export
errors. The description records an application promise; it does not arrange
storage or publication ordering.

## Transport Media Types

Messages default to `application/json`. If the transport advertises another
media type for that same JSON envelope, set the message's content type to match.
For example, Arkoren's MQTT transport advertises `application/octet-stream`:

```go
channel.JSONTopic("ready", Ready, validate.Infer[ReportReady](), func(message *asyncapi.MessageBuilder) {
	message.ContentType("application/octet-stream")
})
```

This is an alternative declaration, not a second registration of `ready`.
`ContentType` changes metadata only; it neither changes the bytes nor configures
transport headers. Do not use `JSONTopic` or `JSONJob` for a transport that carries
a different wire representation.

## Other Wire Formats

An external integration might send a text status directly instead of an Arkoren
envelope. In its channel callback, use `Message` with the actual wire schema:

```go
channel.Message("status", asyncapi.RawSchema(`{"type":"string"}`), func(message *asyncapi.MessageBuilder) {
	message.ContentType("text/plain")
	message.WireExample("completed", "ready")
})
```

This message describes the text itself, with no implicit envelope or separate
logical component. Select `status` in the relevant operation's `Messages` call.
If several raw messages share a channel, you must ensure their wire schemas
distinguish them; the exporter does not prove that raw alternatives are disjoint.

## Schema Providers

A provider implements `JSONSchema() ([]byte, error)`. Published `validate`
schemas and `validate.Infer[T]()` implement that interface. Each declaration
calls its non-nil provider once during document construction and immediately
copies the returned bytes. Sharing a provider across declarations calls it once
per declaration. Errors are retained for `Document.JSON()` or the HTTP handler;
nil and typed-nil providers are errors rather than empty schemas.

All providers must produce the same supported, self-contained Draft 7 profile.
Implementing the interface alone does not make a schema acceptable. See
[Validation's JSON Schema guide](../validation/json-schema.md) for export and
inference limits, including omitted rules and custom JSON encoders. A schema
accepted here does not prove equivalence to runtime validation, normalization,
or domain checks. Providers must not perform network resolution.

For a raw alternative, `RawSchema` accepts object or boolean schemas, including
composition. This message permits a nonempty text result or an integer code:

```go
channel.Message("result", asyncapi.RawSchema(`{
  "oneOf": [
    {"type": "string", "minLength": 1},
    {"type": "integer"}
  ]
}`))
```

The exporter checks keyword shapes in schema positions, not satisfiability or
instance validity. Exported message payloads use an explicit Multi Format Schema
Object with `schemaFormat: "application/schema+json;version=draft-07"`.

## Draft 7 Profile

Use this accepted keyword profile when preparing a provider or raw schema:

| Purpose | Accepted Keywords |
| --- | --- |
| Types and values | `type`, `enum`, `const` |
| Objects | `properties`, `patternProperties`, `additionalProperties`, `propertyNames`, `required`, `minProperties`, `maxProperties` |
| Arrays | `items`, `additionalItems`, `contains`, `uniqueItems`, `minItems`, `maxItems` |
| Composition | `allOf`, `anyOf`, `oneOf`, `not`, `if`, `then`, `else` |
| Numbers | `minimum`, `maximum`, `exclusiveMinimum`, `exclusiveMaximum`, `multipleOf` |
| Strings | `minLength`, `maxLength`, `pattern`, `format`, `contentEncoding`, `contentMediaType` |
| Annotations | `title`, `description`, `default`, `examples`, `readOnly`, `writeOnly`, `$comment` |
| Dialect and extensions | `$schema`, any `x-` keyword |

`items` accepts a schema or a nonempty Draft 7 tuple. Composition arrays must be
nonempty, counts must be nonnegative integers, and `multipleOf` must be positive.
If supplied, `$schema` must equal `http://json-schema.org/draft-07/schema#`.

References and identifiers (`$ref`, `$id`), definitions, `dependencies`, unknown
non-extension keywords, and newer-dialect features such as `$defs`, `prefixItems`,
and `contentSchema` are rejected in schema positions. No schema fetching,
reference resolution, or conversion from Draft 2020-12 occurs. Changing a dialect
label does not convert a schema.

Contents of `x-` extensions and data inside `default`, `const`, `enum`, and
`examples` remain opaque. Property names are not interpreted as keywords.
Formats and patterns are annotations here: the exporter neither executes them
nor converts regular expressions for another validator.

For message and schema options, see the
[`asyncapi` API reference](https://pkg.go.dev/arkoren.dev/core/asyncapi).
