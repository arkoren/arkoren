---
title: AsyncAPI Overview
nav: Overview
description: Generate a messaging contract from typed definitions, payload schemas, destinations, and application operations.
order: 10
summary:
  - Build an AsyncAPI 3.1.0 contract from shared typed definitions and explicit application operations.
  - Generate deterministic JSON without starting a broker or changing runtime messaging behavior.
  - Keep document IDs, logical message names, and physical destinations distinct.
---

# AsyncAPI Overview

Another service needs to know which messages your application sends, where they
travel, and what a delivery contains. `arkoren.dev/core/asyncapi` combines your
typed messaging definitions with explicit documentation metadata to export
AsyncAPI **3.1.0** JSON. Generating that document requires no broker connection.

Use it for [broadcast topics](../broadcasting/overview.md#defining-topics) and
[queued jobs](../queues/producing-jobs.md#defining-jobs). It neither publishes
messages nor registers consumers. The synchronous [event bus](../events/overview.md)
is a different mechanism; [OpenAPI](../openapi/overview.md) describes HTTP contracts.

## Contract Model

A **message** describes one complete wire payload. A **channel** names the
destination where messages travel, such as a topic or queue. Several message
types can share a channel. An **operation** selects the messages your application
sends or receives on that channel: `Send` means out of this application, and
`Receive` means into it, never the broker's perspective.

The typed helpers connect these declarations to application-owned definitions.
`JSONTopic` takes a `broadcast.Topic[T]`; `JSONJob` takes a `queue.Job[T]`. Each
helper uses the definition's logical name to describe an Arkoren JSON envelope
and stores the decoded payload schema separately. You still supply the physical
destination and the application's operations. Nothing discovers producers,
samples traffic, or reads driver configuration.

These are descriptions, not runtime validation or delivery policies. A document
does not establish durable delivery, retries, ordering, or authorization. Keep
those responsibilities in your messaging drivers, handlers, and infrastructure.

## Shared Definitions

Suppose your reporting service announces completed reports. Keep the topic in
your application's shared contract package so the runtime publisher and the
document can use the same identity. With `broadcast` imported from
`arkoren.dev/core/service/broadcast`, define its small logical payload:

```go
type ReportReady struct {
	ReportID string `json:"report_id"`
}

var Ready = broadcast.NewTopic[ReportReady]("report.ready")
```

`Ready` identifies the fact; it does not identify a broker endpoint. Use
`JSONTopic` only if your transport JSON-encodes `broadcast.Message`. A different
wire representation needs an explicit [message schema](messages-and-schemas.md#other-wire-formats).

## Document Generation

Import `arkoren.dev/core/asyncapi` and `arkoren.dev/foundation/validate`. In the
same application package, describe the physical destination and sending role:

```go
var Document = asyncapi.New("Reporting events", "1.0.0", func(definition *asyncapi.Builder) {
	definition.Channel("reports", "production.reports", func(channel *asyncapi.ChannelBuilder) {
		channel.JSONTopic("ready", Ready, validate.Infer[ReportReady]())
	})
	definition.Send("announce-report", "reports", func(action *asyncapi.OperationBuilder) {
		action.Messages("ready")
	})
})
```

`validate.Infer` describes the Go payload's JSON shape. The wire message is a JSON
envelope whose `payload` field contains base64-encoded JSON, not an inline
`ReportReady` object. [Messages and Schemas](messages-and-schemas.md#wire-and-logical)
explains both layers and the supported schema profile.

This portable document omits broker endpoints. If you add
[server declarations](bindings-and-security.md#broker-endpoints), every channel
must explicitly select at least one server. Every operation must select at least
one message from its channel, whether or not servers are declared.

Generate the bytes in a test, build command, or other application code:

```go
data, err := Document.JSON()
```

On success, `data` contains deterministic JSON with `asyncapi: "3.1.0"`, a
`reports` channel, and an `announce-report` send operation. `1.0.0` is your
contract's version, not the AsyncAPI specification version. Handle `err` before
using the bytes; [Serving Documentation](serving.md) covers file exports and HTTP
access. Focused snippets in these guides keep errors visible but omit routine
propagation; complete application code must handle them.

## Stable Names

Choose document IDs independently of runtime names. This example separates four
values with different purposes:

| Value | Example | Purpose |
| --- | --- | --- |
| Channel ID | `reports` | Local reference used by operations. |
| Channel address | `production.reports` | Literal destination used by the deployed transport. |
| Topic name | `report.ready` | Logical discriminator carried in the envelope. |
| Message ID | `ready` | Local selection within the channel. |

Document IDs accept nonempty ASCII letters, digits, dots, underscores, and
hyphens. Duplicate IDs are errors, not replacements. Keep channel/message ID
pairs unambiguous: typed logical schema components use `<channel>.<message>`,
so pairs such as `reports.v1` / `ready` and `reports` / `v1.ready` collide.

The exporter does not apply a driver's destination prefixes or routing rules.
Supply the deployed physical address yourself. An empty address exports as
`null`, meaning unknown; it does not choose a default destination. Address
templates containing braces are unsupported.

## Early Checks

`New` runs supplied construction callbacks synchronously, once in order, and
captures schemas and examples immediately. It retains declaration errors rather
than returning them. `JSON()` checks the completed graph, including forward
references, and caches either the export or its error. Run an export before
deployment so an invalid declaration does not first fail on an HTTP request.

The resulting document is immutable and has no startup or cleanup work. Construct
a new document when the contract changes; changing a retained builder or an
exported byte slice does not update it. The exact
[snapshot and failure behavior](serving.md#snapshot-behavior) also applies when
you serve the document.

For all declarations and options, see the
[`asyncapi` API reference](https://pkg.go.dev/arkoren.dev/core/asyncapi).
