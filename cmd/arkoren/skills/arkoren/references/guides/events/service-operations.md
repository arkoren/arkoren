---
title: Service Operation Events
description: Observe payment calls, push submissions, search administration, and secret mutations without treating API results as durable business transitions.
order: 35
---

# Service Operation Events

## Introduction

To monitor outbound calls locally, give the service's construction builder an
event bus through `Events`. Omitted or nil buses publish nothing. These topics
report returned driver outcomes, not settlement, delivery, atomic index changes,
or credential rollout. Their payloads contain bounded classifications instead
of raw provider errors.

Delivery is synchronous, process-local, and best effort. Listener failures cannot
replace API results, but slow listeners delay return; crashes can lose events.
Listeners in another pod receive nothing automatically. Required audit,
reconciliation, and fulfillment work needs an application-owned durable boundary,
not queueing from a best-effort listener. See
[how services publish](built-in-events.md#how-services-publish).

## Payments

With `payment.Builder.Events`, each validated facade call that reaches the
driver and returns a result reports one operation observation:

| Topic | Name | Payload | Fires |
| --- | --- | --- | --- |
| `payment.OperationCompleted` | `payment.operation.completed` | `OperationEvent{Operation, ErrorType}` | the driver returned nil error |
| `payment.OperationFailed` | `payment.operation.failed` | `OperationEvent{Operation, ErrorType}` | the driver returned a non-nil error |

`Operation` is a fixed facade operation name. `ErrorType` is empty on completion
and otherwise a bounded payment/context class or `unknown`, never native error
text. `payment.ErrOutcomeUnknown` takes precedence over joined causes: the
provider may have applied the mutation. Requests rejected before driver I/O
and provider panics publish nothing.

Completion includes reads, pending responses, and idempotent replays; it does
not prove authorization, settlement, or fulfillment eligibility. Payloads omit
IDs, amounts, keys, URLs, references, and handoff secrets. These notifications
are not durable reconciliation or fulfillment triggers. See
[local payment monitoring](../payments/overview.md#optional-notification-handling)
and [the payment API reference](https://pkg.go.dev/arkoren.dev/core/service/payment).

## Push notifications

With `push.Builder.Events`, `Send` reports the returned driver submission:

| Topic | Name | Payload | Fires |
| --- | --- | --- | --- |
| `push.Accepted` | `push.submission.accepted` | `SubmissionEvent{Duration, ErrorClass}` | the driver returned nil error |
| `push.Failed` | `push.submission.failed` | `SubmissionEvent{Duration, ErrorClass}` | the driver returned a non-nil error |

`Duration` measures the driver call, not delivery latency. `ErrorClass` is empty
on acceptance and otherwise a bounded class. `outcome_unknown` takes precedence
over cancellation and means the provider may have accepted the submission.
Preflight rejection and provider panics publish nothing. Recipients, endpoints,
tokens, contents, receipt IDs, and native errors are omitted.

Acceptance is not device delivery or display, and failure is not proof that
delivery could not occur. Retry and registration-cleanup policy remain with
your application. See [Push Notifications](../push/overview.md) and
[the push API reference](https://pkg.go.dev/arkoren.dev/core/service/push).

## Search

With `search.Builder.Events`, the engine reports administrative `Sync` and
`Drop` driver outcomes only:

| Topic | Name | Payload | Fires |
| --- | --- | --- | --- |
| `search.Synced` | `search.index.synced` | `MutationEvent{Operation, Duration, ErrorClass}` | `Sync` returned nil error |
| `search.Dropped` | `search.index.dropped` | `MutationEvent{Operation, Duration, ErrorClass}` | `Drop` returned nil error |
| `search.Failed` | `search.index.failed` | `MutationEvent{Operation, Duration, ErrorClass}` | either administrative driver call returned an error |

`Operation` is `sync` or `drop`; `Duration` excludes listener execution.
`ErrorClass` is empty on success and otherwise a bounded search/context class
or `error`. Payloads omit index names, documents, queries, IDs, and native
errors. `Check`, queries, document writes/deletes, invalid registrations, and
provider panics publish nothing.

A successful sync may change nothing, and dropping an absent index can succeed.
A failed multi-index sync may already have applied changes; these events imply
neither rollback nor an atomic commit. See
[Deploying Search](../search/deploying.md) and
[the search API reference](https://pkg.go.dev/arkoren.dev/core/service/search).

## Secrets

With `secret.Builder.Events`, facade `Put`, `Update`, and `Delete` calls report
their observed driver outcome:

| Topic | Name | Payload | Fires |
| --- | --- | --- | --- |
| `secret.MutationSucceeded` | `secret.mutation.succeeded` | `MutationEvent{Operation, ErrorType}` | the driver returned nil error |
| `secret.MutationFailed` | `secret.mutation.failed` | `MutationEvent{Operation, ErrorType}` | the driver returned a non-nil error |

`Operation` is `put`, `update`, or `delete`. `ErrorType` is empty on success and
otherwise `secret.ErrNotFound`, `context.Canceled`, `context.DeadlineExceeded`,
or `other`. No names, versions, values, sizes, or native errors enter the payload.
Reads and direct driver/native calls publish nothing.

These local observations do not cover provider-console or other-process writes,
prove credential rollout, or replace provider auditing. Failure does not prove
that the provider made no change. See [Managing Secrets](../secrets/managing.md)
and [the secret API reference](https://pkg.go.dev/arkoren.dev/core/service/secret).
