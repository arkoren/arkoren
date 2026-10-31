---
title: AsyncAPI Overview
nav: Overview
description: Describe the messages your application sends and receives, then generate your first AsyncAPI document without a broker connection.
order: 10
---

# AsyncAPI Overview

## Introduction

Other services need to know where your application sends messages, which messages
it accepts, and what those messages contain. Use `arkoren.dev/core/asyncapi` to
declare that contract and export AsyncAPI 3.1.0 JSON. Your declarations describe
the application boundary; they do not publish messages or start consumers.

AsyncAPI documents [broadcasts](../broadcasting/overview.md) and
[queued jobs](../queues/overview.md). It does not make the synchronous
[event bus](../events/overview.md) asynchronous. For HTTP requests and responses,
use [OpenAPI](../openapi/overview.md) instead.

## Understanding a messaging contract

Start with what another application needs to understand. A **message** describes
one complete payload on the wire. A **channel** describes the destination where
that message can travel, such as a broker topic or a work queue. Several message
types can share a channel.

An **operation** says which channel messages your application sends or receives.
`Send` means *from your application*; `Receive` means *into your application*.
A producer usually declares `Send`, and a worker declares `Receive`. The
broker's perspective does not reverse these directions.

A **server** describes the broker endpoint and protocol associated with a
channel, not a live connection. A **document** gathers these declarations under
an application contract title and version. You keep your existing drivers and
application lifecycle: none of these declarations configures a broker.

Keep document identifiers, application names, and transport addresses separate:

| Value | Example | Meaning |
| --- | --- | --- |
| Server ID | `primary` | Local reference inside this document. |
| Server host | `broker.example:9092` | Broker host and optional port, without a URL scheme or credentials. |
| Channel ID | `orders` | Local reference used by operations. |
| Channel address | `production.orders` | Literal physical destination used by your configured transport. |
| Broadcast topic name | `order.created` | Logical fact name carried in the Arkoren envelope. |
| Message ID | `created` | Local reference used to select a channel message. |

A driver's prefixes or routing configuration can make the physical address
different from the logical name. Supply the actual destination yourself; the
exporter does not inspect driver configuration. IDs use nonempty letters,
digits, dots, underscores, or hyphens. An empty channel address exports as `null`
(unknown), not as a default destination. Address templates are not supported.

## Generating your first document

Suppose the order service publishes `order.created` to `production.orders`.
This walkthrough declares that broadcast and writes a document; it needs no
running broker, credentials, or application startup. Install the core module and
the schema inference helper from your application's module directory:

```sh
go get arkoren.dev/core
go get arkoren.dev/foundation/validate
```

First, create `contract/asyncapi.go` with the shared topic used by your runtime
publisher. `validate.Infer` describes the logical Go payload:

```go
package contract

import (
	"os"

	"arkoren.dev/core/asyncapi"
	"arkoren.dev/core/service/broadcast"
	"arkoren.dev/foundation/validate"
)

type OrderCreated struct {
	OrderID string `json:"order_id"`
}

var Created = broadcast.NewTopic[OrderCreated]("order.created")
```

In the same file, declare the broker, channel, wire message, and sending
operation. Use `JSONTopic` only when the transport JSON-encodes Arkoren's
broadcast envelope:

```go
var Document = asyncapi.New("Order events", "1.0.0", func(definition *asyncapi.Builder) {
	definition.Server("primary", "broker.example:9092", "kafka")
	definition.Channel("orders", "production.orders", func(channel *asyncapi.ChannelBuilder) {
		channel.Servers("primary")
		channel.JSONTopic("created", Created, validate.Infer[OrderCreated]())
	})
	definition.Send("announce-order", "orders", func(action *asyncapi.OperationBuilder) {
		action.Messages("created")
	})
})
```

Choose the protocol and address that match your deployment. If the document
declares any servers, every channel must explicitly select at least one with
`Servers`. Each operation must explicitly select at least one message with
`Messages`; it does not automatically select every message on its channel.

`JSONTopic` describes the complete JSON envelope, whose `payload` is a base64
string containing the logical value's JSON. It stores the logical schema
separately; [Messages and Schemas](messages-and-schemas.md#understanding-the-wire-envelope)
explains how to read both shapes. A transport with another representation needs
an explicit wire schema instead of this helper.

Finally, add an export function to the same file. It overwrites an existing
destination after validation succeeds; choose a generated-artifact path, not a
file containing data you need to preserve:

```go
func Export(path string) error {
	data, err := Document.JSON()
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0600)
}
```

Call `contract.Export("asyncapi.json")` from your build tool and handle its
returned error, including file-write failures. On success, the file contains
deterministic AsyncAPI JSON with version `3.1.0`, the `orders` channel, and the
`announce-order` send operation. Validation happens before the file is touched;
an existing file is overwritten. Review broker details and examples before
distributing the artifact.

## Checking declarations early

`New` runs construction callbacks synchronously, once in order, without I/O.
Schema providers are called and their bytes copied during construction, not on
the first HTTP request. Declaration errors are retained for `JSON()` or the
handler to report later. Export in a test or build step so an invalid contract
does not first fail on a documentation request.

The document is immutable and needs no `Start`, `Stop`, or cleanup. Build a new
document when the contract changes. [Serving AsyncAPI Documentation](serving.md)
covers cached exports, protected endpoints, and declaration failures.

The remaining examples in this group are focused changes to existing
declarations. They keep error results visible but omit routine checks unless
error handling is the lesson; handle errors in your application.

For every declaration and option, see the [`asyncapi` API reference](https://pkg.go.dev/arkoren.dev/core/asyncapi).
