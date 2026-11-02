---
title: Queues and Operations
description: Describe shared queue destinations and select the jobs each application produces or consumes.
order: 30
summary:
  - Describe multiple typed jobs on one physical destination using their shared logical definitions.
  - Select the messages each application sends or receives and document completion broadcasts separately.
  - Recognize typed discriminator collisions and review raw message overlap yourself.
---

# Queues and Operations

A reporting worker can build new reports and remove expired ones from the same
queue. Model the destination as one channel, each job type as a message, and the
worker's receiving role as an operation selecting those messages. The document
describes that role; it does not dispatch jobs or register handlers.

Reuse your application's typed [job definitions](../queues/producing-jobs.md#defining-jobs)
instead of maintaining separate documentation names. The examples import
`arkoren.dev/core/asyncapi`, `arkoren.dev/core/service/queue`, and
`arkoren.dev/foundation/validate` in an application contract package.

## Shared Job Definitions

Keep the payload specific to each task. Building a report needs its
source account; removing an artifact needs the report's identity:

```go
type BuildReport struct {
	AccountID string `json:"account_id"`
}

type RemoveReport struct {
	ReportID string `json:"report_id"`
}
```

Define both jobs on the logical queue `reports`, sharing these same definitions
with runtime dispatchers and consumers:

```go
var Build = queue.NewJob[BuildReport]("build-report", func(job *queue.JobBuilder) {
	job.OnQueue("reports")
})

var Remove = queue.NewJob[RemoveReport]("remove-report", func(job *queue.JobBuilder) {
	job.OnQueue("reports")
})
```

The logical queue is part of each job definition. It need not equal the physical
destination after your driver's prefixes or routing configuration are applied.
The exporter does not derive that destination from the job.

## Shared Queue Messages

Suppose both jobs travel through `production.report-work`. Describe that physical
destination once and add both job envelopes to it:

```go
var Document = asyncapi.New("Reporting worker", "1.0.0", func(definition *asyncapi.Builder) {
	definition.Channel("work", "production.report-work", func(channel *asyncapi.ChannelBuilder) {
		channel.JSONJob("build", Build, validate.Infer[BuildReport]())
		channel.JSONJob("remove", Remove, validate.Infer[RemoveReport]())
	})
	definition.Receive("process-reports", "work", func(action *asyncapi.OperationBuilder) {
		action.Messages("build", "remove")
	})
})
```

`JSONJob` describes the JSON encoding of `queue.Message`, not every queue
transport. Its wire schema requires `id`, `name`, `queue`, `payload`, and
`attempts`; optional `metadata` maps strings to strings. The name and logical
queue are constants from the job definition. `attempts` remains an integer,
not a constant taken from retry policy, because it can change between deliveries.

The wire `payload` is a base64 string containing the logical value's JSON.
The separate logical components are `work.build` and `work.remove`, linked by
each message's `x-arkoren-logical-payload` annotation. See
[wire and logical schemas](messages-and-schemas.md#wire-and-logical) before
interpreting those components in a viewer.

Generate `Document.JSON()` in a test or build step to check the declaration.
No worker or broker needs to run. Job policies and
[consumer registration](../queues/consuming-jobs.md) still govern execution;
these schemas neither enforce those policies nor guarantee delivery.

## Application Roles

An operation describes the application named by the document. The worker above
receives both jobs. A separate frontend that only requests report creation
should declare a sending operation selecting only `build`. In that producer's
document callback, use this instead of the worker's `Receive`:

```go
definition.Send("request-report", "work", func(action *asyncapi.OperationBuilder) {
	action.Messages("build")
	action.Summary("Request report creation")
})
```

Declaring `remove` on the channel does not automatically add it to the producer's
operation. Every operation must explicitly select at least one message from its
own channel. Selections preserve their supplied order; duplicates, including
duplicates across repeated `Messages` calls, are errors rather than deduplication.

Channel and message declarations may appear after their operation: references
are resolved against the completed document at export. A selection cannot reach
into another channel, and operation IDs must be unique across both directions.

## Completion Events

A worker can receive queue jobs and publish broadcast facts. Keep the actual
destinations separate rather than placing every message on the work channel.
Reuse `Ready` and `ReportReady` from the
[overview's shared definitions](overview.md#shared-definitions), with the same
definitions used by your runtime publisher.

Inside the worker document callback, retain the `work` channel and receiving
operation, then add the completion destination and sending role:

```go
definition.Channel("events", "production.reports", func(channel *asyncapi.ChannelBuilder) {
	channel.JSONTopic("ready", Ready, validate.Infer[ReportReady]())
})
definition.Send("announce-report", "events", func(action *asyncapi.OperationBuilder) {
	action.Messages("ready")
})
```

The document now describes receiving work and sending a completion fact. It
does not connect the two operations, store an artifact, or ensure publication
after successful processing. Implement that sequence and its failure handling
in your application. If you add server declarations, both channels must
[select their servers](bindings-and-security.md#broker-endpoints) explicitly.

## Distinct Wire Messages

Typed helpers reject repeated discriminator identities within one channel:
duplicate topic names and duplicate job name/queue pairs are export errors,
even when their message IDs differ. Two jobs with the same name but different
logical queues are allowed because their queue constants distinguish them.

A topic and job cannot share a wire name on one channel. A job's extra envelope
fields do not keep it from matching the topic schema, so different queue values
cannot resolve that overlap. Raw `Message` schemas are not checked for overlap
with each other or with typed helpers; review those alternatives yourself.

For typed helpers and operation options, see the
[`asyncapi` API reference](https://pkg.go.dev/arkoren.dev/core/asyncapi).
