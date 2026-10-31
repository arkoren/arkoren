---
title: Queues and Operations
description: Describe several job types on one physical queue and distinguish the messages each application sends or receives.
order: 30
---

# Queues and Operations

## Introduction

A billing worker may process receipt emails and invoice rebuilds from one queue.
Document one channel for that physical destination, one message for each job
type, and an operation that selects the jobs the application actually handles.
AsyncAPI describes this contract; it does not dispatch work or register workers.

Use your existing typed [job definitions](../queues/producing-jobs.md#defining-a-job)
and [topics](../broadcasting/overview.md#topics) as the source of logical names.
The document is not an inventory of all producers, jobs, or broker resources.

## Documenting two jobs on one queue

In a contract package, define the shared jobs just as you do for runtime
dispatching and registration. Both jobs below use the logical queue `billing`:

```go
package contract

import (
	"arkoren.dev/core/asyncapi"
	"arkoren.dev/core/service/queue"
	"arkoren.dev/foundation/validate"
)

type SendReceipt struct {
	OrderID string `json:"order_id"`
}

type RebuildInvoice struct {
	InvoiceID string `json:"invoice_id"`
}

var Receipt = queue.NewJob[SendReceipt]("send-receipt", func(job *queue.JobBuilder) {
	job.OnQueue("billing")
})

var Invoice = queue.NewJob[RebuildInvoice]("rebuild-invoice", func(job *queue.JobBuilder) {
	job.OnQueue("billing")
})
```

Reuse those definitions to declare the worker's receiving contract in the same
package. Here the deployed driver's physical destination is `production.billing`:

```go
var Document = asyncapi.New("Billing worker", "1.0.0", func(definition *asyncapi.Builder) {
	definition.Server("primary", "broker.example:5672", "amqp")
	definition.Channel("work", "production.billing", func(channel *asyncapi.ChannelBuilder) {
		channel.Servers("primary")
		channel.JSONJob("receipt", Receipt, validate.Infer[SendReceipt]())
		channel.JSONJob("invoice", Invoice, validate.Infer[RebuildInvoice]())
	})
	definition.Receive("process-billing", "work", func(action *asyncapi.OperationBuilder) {
		action.Messages("receipt", "invoice")
	})
})
```

`JSONJob` describes the JSON job envelope, including name and logical queue
constants from the job definition. Its `payload` is a base64 string, not an
inline logical object. Each decoded payload schema has its own component and
`x-arkoren-logical-payload` annotation, as explained in
[Messages and Schemas](messages-and-schemas.md#understanding-the-wire-envelope).

The logical queue `billing` does not replace the physical address
`production.billing`. Retry and timeout policies still belong to your
[job definitions and workers](../queues/consuming-jobs.md). The envelope's
`attempts` remains an integer rather than a constant because deliveries can
change it. Export this `Document` with `JSON()` in a test or build step, using
the [overview's export pattern](overview.md#generating-your-first-document).

## Selecting messages by application role

`Receive` means into the application described by this document. A separate
producer would use `Send` for the same queue. If that producer only dispatches
receipts, its operation should select only `receipt`, even if the channel also
declares `invoice`. Replace the worker's `Receive` declaration for that role:

```go
definition.Send("request-receipt", "work", func(action *asyncapi.OperationBuilder) {
	action.Messages("receipt")
	action.Summary("Request a receipt email")
})
```

Every operation explicitly selects at least one message from its own channel.
Selections preserve declaration order; duplicate selections are errors, not
deduplication. An operation cannot select a message from another channel.
Forward references are allowed, so channels and messages can be declared later
in the construction callbacks.

## Describing both roles in one application

An application may receive a job and then publish a completion fact. Describe
the two actual destinations separately rather than putting every message on
one channel. Add the `arkoren.dev/core/service/broadcast` import and declare the
completion topic next to the shared jobs, so your runtime publisher can reuse it:

```go
var Completed = broadcast.NewTopic[RebuildInvoice]("invoice.rebuilt")
```

Within the existing billing document callback, keep the `work` channel and
`process-billing` operation above, then add this fragment:

```go
definition.Channel("events", "production.invoice-events", func(channel *asyncapi.ChannelBuilder) {
	channel.Servers("primary")
	channel.JSONTopic("rebuilt", Completed, validate.Infer[RebuildInvoice]())
})
definition.Send("announce-rebuild", "events", func(action *asyncapi.OperationBuilder) {
	action.Messages("rebuilt")
})
```

The document now describes receiving jobs on `production.billing` and sending
a fact on `production.invoice-events`. It does not connect these operations or
guarantee that the publish happens after successful processing; implement that
behavior in your worker. Share the topic with the runtime publisher, as with
the topic in the overview.

## Avoiding overlapping messages

Typed helpers reject duplicate envelope discriminator identities on one
channel. A topic and a job cannot share a wire name there because their schemas
overlap. Jobs with the same name on different logical queues are allowed, since
the queue constants distinguish them. Raw `Message` schemas are not checked for
overlap; review those alternatives yourself.

For every operation and typed helper, see the [`asyncapi` API reference](https://pkg.go.dev/arkoren.dev/core/asyncapi).
